package library

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SettingThumbWorkers is the settings key holding how many thumbnails are
// generated concurrently during a scan. Fewer workers = lower CPU pressure so
// the web server stays responsive while a scan runs. Default is 1.
const SettingThumbWorkers = "thumbnail_workers"

// defaultThumbWorkers is the conservative default concurrency for the thumbnail
// phase: serial generation, leaving CPU headroom for serving requests.
const defaultThumbWorkers = 1

// maxThumbWorkers caps the configurable concurrency to avoid pegging every core.
const maxThumbWorkers = 8

// Scanner walks library roots to populate collections and albums.
type Scanner struct {
	db     *sql.DB
	store  *Store
	thumbs *Thumbnailer

	mu       sync.Mutex
	progress map[string]*ScanProgress

	thumbWorkers int
}

// ScanProgress is a snapshot of an in-flight (or finished) library scan.
// A scan runs in two phases: "index" (walk + DB upsert) then "thumbnails"
// (pre-generate thumbnails for every photo). The Thumb* fields describe the
// second phase so the UI can render a separate progress bar.
type ScanProgress struct {
	LibraryID  string `json:"libraryId"`
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Phase      string `json:"phase"` // "index" | "thumbnails" | ""
	Total      int    `json:"total"`
	Current    int    `json:"current"`
	CurrentDir string `json:"currentDir"`
	ThumbTotal int    `json:"thumbTotal"`
	ThumbDone  int    `json:"thumbDone"`
	Error      string `json:"error,omitempty"`
}

// NewScanner builds a scanner. Photo paths are derived from each library's own
// root, so there is no global photos root.
func NewScanner(db *sql.DB, store *Store) *Scanner {
	return &Scanner{db: db, store: store, progress: map[string]*ScanProgress{}, thumbWorkers: defaultThumbWorkers}
}

// SetThumbnailer wires the thumbnailer so scans can pre-generate thumbnails as a
// second phase. If unset, scans skip thumbnail generation (lazy-only behavior).
func (sc *Scanner) SetThumbnailer(t *Thumbnailer) { sc.thumbs = t }

// ThumbWorkers returns the configured thumbnail generation concurrency.
func (sc *Scanner) ThumbWorkers() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.thumbWorkers
}

// SetThumbWorkers clamps and sets the thumbnail generation concurrency used by
// future scans. Values are clamped to [1, maxThumbWorkers].
func (sc *Scanner) SetThumbWorkers(n int) {
	if n < 1 {
		n = 1
	}
	if n > maxThumbWorkers {
		n = maxThumbWorkers
	}
	sc.mu.Lock()
	sc.thumbWorkers = n
	sc.mu.Unlock()
}

// LoadThumbWorkers reads the persisted thumbnail worker count (falling back to
// the default) and applies it. Called at startup.
func (sc *Scanner) LoadThumbWorkers() {
	n := defaultThumbWorkers
	if sc.store != nil {
		if v, err := sc.store.GetSetting(SettingThumbWorkers, strconv.Itoa(defaultThumbWorkers)); err == nil {
			if parsed, perr := strconv.Atoi(strings.TrimSpace(v)); perr == nil {
				n = parsed
			}
		}
	}
	sc.SetThumbWorkers(n)
}

// Progress returns the latest scan progress snapshot for a library, if any.
func (sc *Scanner) Progress(libraryID string) (ScanProgress, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	p, ok := sc.progress[libraryID]
	if !ok {
		return ScanProgress{}, false
	}
	return *p, true
}

func (sc *Scanner) setProgress(p ScanProgress) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	cp := p
	sc.progress[p.LibraryID] = &cp
}

func (sc *Scanner) updateProgress(libraryID string, fn func(*ScanProgress)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	p, ok := sc.progress[libraryID]
	if !ok {
		p = &ScanProgress{LibraryID: libraryID}
		sc.progress[libraryID] = p
	}
	fn(p)
}

// Scan walks a library's root folder two levels deep, syncing collections
// (level 1) and albums (level 2) into the database. Stale entries are removed.
// It also reports progress so callers can render a live banner.
func (sc *Scanner) Scan(lib *Library, source string) error {
	sc.setProgress(ScanProgress{LibraryID: lib.ID, Running: true, Phase: "index"})

	err := sc.scan(lib)

	// Phase 2: pre-generate thumbnails for the whole library so a finished scan
	// means thumbnails are ready (rather than lazily generated on first view).
	if err == nil && sc.thumbs != nil {
		sc.generateThumbnails(lib)
	}

	sc.updateProgress(lib.ID, func(p *ScanProgress) {
		p.Running = false
		p.Done = true
		p.Phase = ""
		p.CurrentDir = ""
		if err != nil {
			p.Error = err.Error()
		}
	})
	// Persist failures so admins can review them later (the in-memory progress
	// is ephemeral and background/auto-scan failures otherwise only hit stdout).
	if err != nil && sc.store != nil {
		if recErr := sc.store.RecordScanError(lib.ID, lib.Name, source, err.Error()); recErr != nil {
			log.Printf("record scan error for %s: %v", lib.ID, recErr)
		}
	}
	return err
}

func (sc *Scanner) scan(lib *Library) error {
	tx, err := sc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	seen := map[string]bool{}

	// Count the total directories up front for progress reporting.
	allDirs, err := allSubtreeDirs(lib.RootPath)
	if err != nil {
		return fmt.Errorf("read library root %q: %w", lib.RootPath, err)
	}
	sc.updateProgress(lib.ID, func(p *ScanProgress) { p.Total = len(allDirs) })

	// Walk the tree recursively, creating one node per directory. A node can be
	// both a collection (has child dirs) and an album (has direct images).
	var walk func(dir, parentID string, depth int) error
	walk = func(dir, parentID string, depth int) error {
		children, err := subdirs(dir)
		if err != nil {
			return fmt.Errorf("read dir %q: %w", dir, err)
		}
		count, err := countImages(dir)
		if err != nil {
			return fmt.Errorf("count images %q: %w", dir, err)
		}
		name := filepath.Base(dir)
		rel, _ := filepath.Rel(lib.RootPath, dir)
		sc.updateProgress(lib.ID, func(p *ScanProgress) {
			p.Current++
			p.CurrentDir = filepath.ToSlash(rel)
		})
		id, err := sc.upsertNode(tx, lib.ID, parentID, name, dir, depth, count, len(children) > 0)
		if err != nil {
			return err
		}
		seen[id] = true
		for _, child := range children {
			if err := walk(filepath.Join(dir, child), id, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	topDirs, err := subdirs(lib.RootPath)
	if err != nil {
		return fmt.Errorf("read library root %q: %w", lib.RootPath, err)
	}
	for _, name := range topDirs {
		if err := walk(filepath.Join(lib.RootPath, name), "", 0); err != nil {
			return err
		}
	}

	if err := pruneNodes(tx, lib.ID, seen); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE libraries SET last_scan = ? WHERE id = ?`, time.Now(), lib.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// generateThumbnails is scan phase 2: it pre-generates a thumbnail for every
// photo in the library so the gallery renders instantly after a scan. Work is
// spread across a small worker pool; failures on individual images are logged
// but do not abort the phase. Progress is reported via ThumbTotal/ThumbDone.
func (sc *Scanner) generateThumbnails(lib *Library) {
	albumDirs, err := findAlbumDirs(lib.RootPath)
	if err != nil {
		log.Printf("thumbnail phase: list albums for %s: %v", lib.ID, err)
		return
	}

	var rels []string
	for _, dir := range albumDirs {
		photos, err := sc.ListPhotos(dir)
		if err != nil {
			continue
		}
		for _, p := range photos {
			rels = append(rels, p.Path)
		}
	}

	sc.updateProgress(lib.ID, func(p *ScanProgress) {
		p.Phase = "thumbnails"
		p.ThumbTotal = len(rels)
		p.ThumbDone = 0
		p.CurrentDir = ""
	})
	if len(rels) == 0 {
		return
	}

	workers := sc.ThumbWorkers()
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				if _, err := sc.thumbs.Ensure(rel); err != nil {
					log.Printf("thumbnail %q: %v", rel, err)
				}
				sc.updateProgress(lib.ID, func(p *ScanProgress) { p.ThumbDone++ })
			}
		}()
	}
	for _, rel := range rels {
		jobs <- rel
	}
	close(jobs)
	wg.Wait()
}

// allSubtreeDirs returns every (non-hidden) directory at or under root,
// excluding root itself, used for progress totals.
func allSubtreeDirs(root string) ([]string, error) {
	var dirs []string
	var walk func(dir string) error
	walk = func(dir string) error {
		subs, err := subdirs(dir)
		if err != nil {
			return err
		}
		for _, s := range subs {
			full := filepath.Join(dir, s)
			dirs = append(dirs, full)
			if err := walk(full); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return dirs, nil
}

func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !SkipDirName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// findAlbumDirs returns every directory at or under colPath that directly
// contains at least one image file. These are the album folders. Hidden
// directories (dot-prefixed, e.g. legacy art dirs) are skipped. Results are
// sorted for stable ordering and progress reporting.
func findAlbumDirs(colPath string) ([]string, error) {
	var albums []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		hasImage := false
		var subDirs []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if !SkipDirName(name) {
					subDirs = append(subDirs, filepath.Join(dir, name))
				}
				continue
			}
			if IsImage(name) {
				hasImage = true
			}
		}
		if hasImage {
			albums = append(albums, dir)
		}
		for _, sd := range subDirs {
			if err := walk(sd); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(colPath); err != nil {
		return nil, err
	}
	sort.Strings(albums)
	return albums, nil
}

func countImages(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && IsImage(e.Name()) {
			n++
		}
	}
	return n, nil
}

// upsertNode inserts or refreshes a single node (folder) in the tree. The
// default cover is the first direct image, falling back to the first image in
// any descendant so collection-only folders still get a thumbnail.
func (sc *Scanner) upsertNode(tx *sql.Tx, libraryID, parentID, name, fsPath string, depth, count int, hasChildren bool) (string, error) {
	cover := sc.FirstPhoto(fsPath)
	if cover == "" {
		cover = sc.firstPhotoDeep(fsPath)
	}
	hc := 0
	if hasChildren {
		hc = 1
	}

	var id string
	err := tx.QueryRow(`SELECT id FROM nodes WHERE library_id = ? AND fs_path = ?`, libraryID, fsPath).Scan(&id)
	if err == sql.ErrNoRows {
		id = uuid.NewString()
		_, err = tx.Exec(`INSERT INTO nodes (id, library_id, parent_id, name, fs_path, depth, photo_count, has_children, cover_photo, scanned_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, libraryID, nullIfEmpty(parentID), name, fsPath, depth, count, hc, nullIfEmpty(cover), time.Now())
		return id, err
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE nodes SET parent_id = ?, name = ?, depth = ?, photo_count = ?, has_children = ?, scanned_at = ? WHERE id = ?`,
		nullIfEmpty(parentID), name, depth, count, hc, time.Now(), id); err != nil {
		return "", err
	}
	if cover != "" {
		_, err = tx.Exec(`UPDATE nodes SET cover_photo = ? WHERE id = ? AND cover_set_by IS NULL`, cover, id)
	}
	return id, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// firstPhotoDeep returns the first image (alphabetical) found in the first
// descendant folder that contains images, used as a default cover for folders
// that hold only sub-folders.
func (sc *Scanner) firstPhotoDeep(dir string) string {
	albumDirs, err := findAlbumDirs(dir)
	if err != nil {
		return ""
	}
	for _, a := range albumDirs {
		if p := sc.FirstPhoto(a); p != "" {
			return p
		}
	}
	return ""
}

func pruneNodes(tx *sql.Tx, libraryID string, seen map[string]bool) error {
	rows, err := tx.Query(`SELECT id FROM nodes WHERE library_id = ?`, libraryID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	for _, id := range stale {
		if _, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ListPhotos returns the image files in an album directory, sorted by name.
func (sc *Scanner) ListPhotos(albumFSPath string) ([]Photo, error) {
	entries, err := os.ReadDir(albumFSPath)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && IsImage(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]Photo, 0, len(names))
	for _, n := range names {
		full := filepath.Join(albumFSPath, n)
		out = append(out, Photo{Name: n, Path: AbsToURLPath(full)})
	}
	return out, nil
}

// FirstPhoto returns the relative path of the first image in a directory
// (alphabetical), used as a fallback cover. Empty string if none.
func (sc *Scanner) FirstPhoto(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && IsImage(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return AbsToURLPath(filepath.Join(dir, names[0]))
}

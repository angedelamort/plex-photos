package library

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Scanner walks library roots to populate collections and albums.
type Scanner struct {
	db         *sql.DB
	store      *Store
	photosRoot string

	mu       sync.Mutex
	progress map[string]*ScanProgress
}

// ScanProgress is a snapshot of an in-flight (or finished) library scan.
type ScanProgress struct {
	LibraryID  string `json:"libraryId"`
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Total      int    `json:"total"`
	Current    int    `json:"current"`
	CurrentDir string `json:"currentDir"`
	Error      string `json:"error,omitempty"`
}

// NewScanner builds a scanner. photosRoot is the container mount for /photos and
// is used to compute paths relative to the photos volume for thumb/cover URLs.
func NewScanner(db *sql.DB, store *Store, photosRoot string) *Scanner {
	return &Scanner{db: db, store: store, photosRoot: photosRoot, progress: map[string]*ScanProgress{}}
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
	sc.setProgress(ScanProgress{LibraryID: lib.ID, Running: true})

	err := sc.scan(lib)

	sc.updateProgress(lib.ID, func(p *ScanProgress) {
		p.Running = false
		p.Done = true
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
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
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
				if !strings.HasPrefix(name, ".") {
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
		rel, err := RelToRoot(sc.photosRoot, full)
		if err != nil {
			continue
		}
		out = append(out, Photo{Name: n, Path: rel})
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
	rel, err := RelToRoot(sc.photosRoot, filepath.Join(dir, names[0]))
	if err != nil {
		return ""
	}
	return rel
}

package library

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// ScanMode selects how thorough a scan's post-index (thumbnail + metadata)
// phase is.
//
// ScanQuick is the default cheap pass used by auto-scan and manual scans. It
// only does work it can detect without per-file stat syscalls: it generates
// thumbnails for photos that have none, and indexes metadata for photos with no
// row yet. Already-known photos are skipped with a single in-memory/DB lookup,
// so a warm re-scan of a large library on a NAS performs no per-photo disk I/O.
// The trade-off is that a photo edited in place (same filename, new content) is
// NOT noticed — its thumbnail and metadata stay as they were.
//
// ScanDeep is the occasional, thorough pass. It stats every source file and
// compares mtime/size against the cached thumbnail and the indexed metadata, so
// in-place edits are detected and refreshed. It also removes orphaned
// thumbnails, making a deep scan a full reconciliation of the library.
type ScanMode int

const (
	ScanQuick ScanMode = iota
	ScanDeep
)

// Scanner walks library roots to populate collections and albums.
type Scanner struct {
	db     *sql.DB
	store  *Store
	thumbs *Thumbnailer

	mu       sync.Mutex
	progress map[string]*ScanProgress
	// reportedErrs dedups per-photo failures recorded to the persistent scan
	// error log within a scan run, so one broken library can't flood the log
	// (and prune out genuine errors). Keyed by libraryID\x00phase\x00rel and
	// cleared for a library at the start of each of its scans.
	reportedErrs map[string]bool

	thumbWorkers int
}

// ScanProgress is a snapshot of an in-flight (or finished) library scan.
// A scan runs as a sequence of phases: "index" (walk + DB upsert), then
// "thumbnails" (generate missing thumbnails), then "metadata" (index EXIF /
// sidecar / geocode). A deep scan adds a final "cleanup" phase. Each phase has
// its own total/done counters so the UI can render a distinct progress bar.
type ScanProgress struct {
	LibraryID  string `json:"libraryId"`
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Phase      string `json:"phase"` // "index" | "thumbnails" | "metadata" | "cleanup" | ""
	Total      int    `json:"total"`
	Current    int    `json:"current"`
	CurrentDir string `json:"currentDir"`
	ThumbTotal int    `json:"thumbTotal"`
	ThumbDone  int    `json:"thumbDone"`
	// Metadata phase: per-photo EXIF/sidecar/geocode indexing.
	MetaTotal int `json:"metaTotal"`
	MetaDone  int `json:"metaDone"`
	// Cleanup phase: removal of orphaned cached thumbnails (no source photo).
	CleanupTotal int    `json:"cleanupTotal"`
	CleanupDone  int    `json:"cleanupDone"`
	Error        string `json:"error,omitempty"`
}

// NewScanner builds a scanner. Photo paths are derived from each library's own
// root, so there is no global photos root.
func NewScanner(db *sql.DB, store *Store) *Scanner {
	return &Scanner{db: db, store: store, progress: map[string]*ScanProgress{}, reportedErrs: map[string]bool{}, thumbWorkers: defaultThumbWorkers}
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

// Scan walks a library's root folder, syncing collections and albums into the
// database, then runs a quick (presence-only) thumbnail + metadata pass. Stale
// entries are removed. It also reports progress so callers can render a live
// banner. This is the everyday scan used by auto-scan.
func (sc *Scanner) Scan(lib *Library, source string) error {
	return sc.scanWithJob(lib, source, nil, ScanQuick)
}

// ScanJob runs a quick scan while reporting progress to a JobManager job so the
// admin Jobs page can show live progress and history.
func (sc *Scanner) ScanJob(lib *Library, source string, jp *JobProgress) error {
	return sc.scanWithJob(lib, source, jp, ScanQuick)
}

// DeepScanJob runs a thorough scan (see ScanDeep): it re-checks every photo's
// thumbnail and metadata against the source file's mtime/size and removes
// orphaned thumbnails, reporting progress to the job.
func (sc *Scanner) DeepScanJob(lib *Library, source string, jp *JobProgress) error {
	return sc.scanWithJob(lib, source, jp, ScanDeep)
}

func (sc *Scanner) scanWithJob(lib *Library, source string, jp *JobProgress, mode ScanMode) error {
	// Fresh run: forget which files we already reported so a previously-broken
	// (now fixed, or still broken) photo can be re-evaluated and re-logged.
	sc.clearLibraryErrors(lib.ID)
	sc.setProgress(ScanProgress{LibraryID: lib.ID, Running: true, Phase: "index"})
	if jp != nil {
		jp.SetPhase("index", 0)
	}

	// Collect per-task timings for this run so a timing report can be persisted
	// at the end — including when the scan fails or is canceled.
	metrics := newScanMetrics()

	indexStart := time.Now()
	err := sc.scan(lib)
	metrics.addPhase("index", time.Since(indexStart))

	// Phase 2: (re)generate thumbnails and index metadata so a finished scan
	// means the gallery is ready (rather than lazily generated on first view).
	// A deep scan additionally reconciles the cache by removing orphans.
	if err == nil && sc.thumbs != nil {
		sc.thumbnailPhase(lib, jp, mode, metrics)
	}

	// An admin stop surfaces as a job failure with a clear message rather than a
	// silent partial success. Quick re-scans resume cheaply (already-indexed
	// photos are skipped), so stopping to adjust settings is safe.
	if err == nil && jp.Canceled() {
		err = ErrJobCanceled
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
	// A user-initiated stop is expected, not a fault, so it is not logged.
	if err != nil && !errors.Is(err, ErrJobCanceled) && sc.store != nil {
		if recErr := sc.store.RecordScanError(lib.ID, lib.Name, source, err.Error()); recErr != nil {
			log.Printf("record scan error for %s: %v", lib.ID, recErr)
		}
	}

	// Persist the timing report regardless of outcome (success, failure, or a
	// user cancel), so the admin can always inspect where the time went.
	sc.persistScanReport(lib, jp, metrics, err)
	return err
}

// persistScanReport finalizes the run's metrics into a JSON report and stores it
// (pruning to the configured retention). Reporting failures are logged but never
// affect the scan's own result.
func (sc *Scanner) persistScanReport(lib *Library, jp *JobProgress, metrics *scanMetrics, scanErr error) {
	if sc.store == nil || metrics == nil {
		return
	}
	status := JobSuccess
	switch {
	case errors.Is(scanErr, ErrJobCanceled):
		status = "canceled"
	case scanErr != nil:
		status = JobFailed
	}
	report := metrics.finalize()
	payload, mErr := json.Marshal(report)
	if mErr != nil {
		log.Printf("scan report for %s: marshal: %v", lib.ID, mErr)
		return
	}
	var jobID string
	if jp != nil {
		jobID = jp.id
	}
	started := time.Now().Add(-time.Duration(report.WallMs) * time.Millisecond)
	if rErr := sc.store.RecordScanReport(jobID, lib.ID, lib.Name, status, started, time.Now(), string(payload)); rErr != nil {
		log.Printf("scan report for %s: record: %v", lib.ID, rErr)
	}
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

// thumbnailPhase runs the post-index work as distinct, separately-reported
// phases: it lists every photo once, snapshots the thumbnail cache once, then
// generates thumbnails ("thumbnails"), indexes metadata ("metadata"), and — for
// a deep scan — removes orphaned thumbnails ("cleanup"). Sharing the photo list
// and cache snapshot across all steps avoids re-walking the tree and
// re-statting the cache per step.
func (sc *Scanner) thumbnailPhase(lib *Library, jp *JobProgress, mode ScanMode, metrics *scanMetrics) {
	rels := sc.collectPhotoRels(lib)
	metrics.add("photos", int64(len(rels)))

	// Snapshot the cache directory once so each worker can check for an existing
	// thumbnail with a map lookup instead of an os.Stat per photo, and so the
	// deep-scan cleanup can diff against the same snapshot. A quick scan builds
	// the index without statting each thumbnail (presence only), so a warm
	// re-scan does zero per-file syscalls; a deep scan records modtimes so it
	// can detect sources edited in place.
	cacheIdx, err := sc.thumbs.BuildCacheIndex(mode == ScanDeep)
	if err != nil {
		log.Printf("thumbnail phase: build cache index for %s: %v", lib.ID, err)
		cacheIdx = nil
	}

	thumbStart := time.Now()
	sc.generateThumbnails(lib, rels, cacheIdx, jp, mode, metrics)
	metrics.addPhase("thumbnails", time.Since(thumbStart))

	metaStart := time.Now()
	sc.indexMetadata(lib, rels, jp, mode, metrics)
	metrics.addPhase("metadata", time.Since(metaStart))

	if mode == ScanDeep {
		cleanupStart := time.Now()
		sc.cleanupOrphanThumbs(lib, rels, cacheIdx, jp)
		metrics.addPhase("cleanup", time.Since(cleanupStart))
	}
}

// collectPhotoRels lists every photo (as a URL token) under the library root,
// walking each album directory once.
func (sc *Scanner) collectPhotoRels(lib *Library) []string {
	albumDirs, err := findAlbumDirs(lib.RootPath)
	if err != nil {
		log.Printf("thumbnail phase: list albums for %s: %v", lib.ID, err)
		return nil
	}
	var rels []string
	for _, dir := range albumDirs {
		// The scan must see every file on disk (including quarantined ones) so
		// the thumbnail phase can re-evaluate them; use the raw enumeration
		// rather than ListPhotos, which hides quarantined photos.
		names, err := listImageNames(dir)
		if err != nil {
			continue
		}
		for _, n := range names {
			rels = append(rels, AbsToURLPath(filepath.Join(dir, n)))
		}
	}
	return rels
}

// runPhotoPhase spreads per-photo work across a small worker pool, reporting
// progress both to the in-memory ScanProgress (via setTotal/bump, which run
// under the progress lock) and to the optional JobProgress. Failures on
// individual photos are the work function's concern; this helper only drives
// the pool and progress.
func (sc *Scanner) runPhotoPhase(lib *Library, phase string, rels []string, jp *JobProgress, metrics *scanMetrics, setTotal func(*ScanProgress, int), bump func(*ScanProgress), work func(rel string) error) {
	total := len(rels)
	sc.updateProgress(lib.ID, func(p *ScanProgress) {
		p.Phase = phase
		setTotal(p, total)
		p.CurrentDir = ""
	})
	if jp != nil {
		jp.SetPhase(phase, total)
	}
	if total == 0 {
		return
	}

	workers := sc.ThumbWorkers()
	if workers < 1 {
		workers = 1
	}

	var doneMu sync.Mutex
	done := 0
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				// Drain remaining items without doing work once the admin has
				// stopped the job, so the dispatcher's send unblocks and the
				// phase ends promptly.
				if jp.Canceled() {
					continue
				}
				// Park here while the admin has paused the job. WaitIfPaused
				// also returns if the job is canceled mid-pause, so re-check.
				jp.WaitIfPaused()
				if jp.Canceled() {
					continue
				}
				sc.runOnePhotoItem(lib, phase, rel, jp, metrics, work)
				sc.updateProgress(lib.ID, bump)
				if jp != nil {
					doneMu.Lock()
					done++
					n := done
					doneMu.Unlock()
					jp.SetProgress(n, total)
				}
			}
		}()
	}
	for _, rel := range rels {
		if jp.Canceled() {
			break
		}
		jobs <- rel
	}
	close(jobs)
	wg.Wait()

	// The current-file marker is per-phase; clear it so a finished phase doesn't
	// leave a stale filename in the progress banner or Jobs view.
	sc.updateProgress(lib.ID, func(p *ScanProgress) { p.CurrentDir = "" })
	if jp != nil {
		jp.SetCurrent("")
	}
}

// slowPhotoWarn is how long a single photo may take before we log a warning
// naming the file, so a stuck/slow file is identifiable in the logs.
const slowPhotoWarn = 30 * time.Second

// photoHardTimeout is the point at which we stop waiting on a single photo and
// move on, so one unreadable file (e.g. a flaky NAS path) or a pathological
// decode can't wedge the whole phase. The abandoned work goroutine is left to
// finish or die on its own; we just stop blocking the worker on it.
const photoHardTimeout = 2 * time.Minute

// runOnePhotoItem runs the per-photo work with three safety nets that the raw
// work function lacks: it records the file in the progress banner (so the UI
// shows what's being processed), recovers panics (a malformed image must not
// crash the scan), and watches the wall-clock so a single file that blocks
// forever (stalled storage, decoder loop) is warned about and ultimately
// skipped instead of freezing the phase.
func (sc *Scanner) runOnePhotoItem(lib *Library, phase, rel string, jp *JobProgress, metrics *scanMetrics, work func(rel string) error) {
	sc.updateProgress(lib.ID, func(p *ScanProgress) { p.CurrentDir = rel })
	if jp != nil {
		jp.SetCurrent(rel)
	}

	start := time.Now()
	done := make(chan struct{})
	var workErr error
	go func() {
		defer func() {
			if r := recover(); r != nil {
				workErr = fmt.Errorf("panic: %v", r)
				log.Printf("scan %s: panic processing %q: %v", phase, rel, r)
			}
			close(done)
		}()
		workErr = work(rel)
	}()

	warn := time.NewTimer(slowPhotoWarn)
	defer warn.Stop()
	hard := time.NewTimer(photoHardTimeout)
	defer hard.Stop()
	for {
		select {
		case <-done:
			// Safe to read workErr: the channel close happens-after the write.
			// Record the per-photo wall-clock for this phase so the report can
			// show the per-item distribution (min/max/avg) alongside subtasks.
			metrics.record(phase, time.Since(start))
			if workErr != nil {
				metrics.addError(phase, rel, workErr.Error())
				sc.recordPhotoError(lib, phase, rel, workErr)
			}
			return
		case <-warn.C:
			log.Printf("scan %s: still processing %q after %s (possible stuck file or slow/unreachable storage)",
				phase, rel, time.Since(start).Round(time.Second))
		case <-hard.C:
			// Abandon the leaked goroutine and move on. We don't touch workErr
			// here (the goroutine may still write it), so there's no data race.
			log.Printf("scan %s: giving up on %q after %s; skipping it so the scan can continue",
				phase, rel, time.Since(start).Round(time.Second))
			metrics.record(phase, time.Since(start))
			timeoutMsg := fmt.Sprintf("timed out after %s (stuck file or slow/unreachable storage)", photoHardTimeout)
			metrics.addError(phase, rel, timeoutMsg)
			sc.recordPhotoError(lib, phase, rel, fmt.Errorf("%s", timeoutMsg))
			return
		}
	}
}

// recordPhotoError appends a per-photo failure to the persistent scan error log
// so it surfaces in the admin error log instead of only on stdout. It dedups by
// (library, phase, file) within a scan run so a systematically broken library
// produces one row per file rather than one per retry, and it tags the row's
// source with the phase ("thumbnails" | "metadata") for at-a-glance triage.
func (sc *Scanner) recordPhotoError(lib *Library, phase, rel string, cause error) {
	if sc.store == nil || cause == nil {
		return
	}
	key := lib.ID + "\x00" + phase + "\x00" + rel
	sc.mu.Lock()
	if sc.reportedErrs[key] {
		sc.mu.Unlock()
		return
	}
	sc.reportedErrs[key] = true
	sc.mu.Unlock()

	if err := sc.store.RecordScanError(lib.ID, lib.Name, phase, fmt.Sprintf("%s: %v", rel, cause)); err != nil {
		log.Printf("record scan error for %s: %v", lib.ID, err)
	}
}

// clearLibraryErrors forgets the per-photo errors already reported for a library
// so a new scan re-evaluates and re-logs them from scratch.
func (sc *Scanner) clearLibraryErrors(libraryID string) {
	prefix := libraryID + "\x00"
	sc.mu.Lock()
	for k := range sc.reportedErrs {
		if strings.HasPrefix(k, prefix) {
			delete(sc.reportedErrs, k)
		}
	}
	sc.mu.Unlock()
}

// generateThumbnails generates any missing thumbnails for the given photos
// (phase "thumbnails"). In ScanQuick mode an existing thumbnail is trusted via a
// cache-index lookup with no source stat; only photos with no thumbnail do disk
// I/O. In ScanDeep mode every source file is stat'd so a thumbnail stale
// relative to a source edited in place is refreshed.
//
// It only ADDS or REFRESHES thumbnails; it never deletes. Removing orphaned
// thumbnails is the deep scan's separate cleanup step, so a quick scan is never
// destructive.
func (sc *Scanner) generateThumbnails(lib *Library, rels []string, cacheIdx CacheIndex, jp *JobProgress, mode ScanMode, metrics *scanMetrics) {
	deep := mode == ScanDeep
	// Load the set of already-quarantined photos once so we can skip re-decoding
	// (and re-failing on) known-broken files with an in-memory lookup. A failed
	// load is non-fatal: we simply attempt every file as before.
	quarantined, qerr := sc.store.QuarantinedPaths(lib.ID)
	if qerr != nil {
		log.Printf("scan thumbnails %s: load quarantine: %v", lib.ID, qerr)
		quarantined = map[string]bool{}
	}
	sc.runPhotoPhase(lib, "thumbnails", rels, jp, metrics,
		func(p *ScanProgress, total int) { p.ThumbTotal = total; p.ThumbDone = 0 },
		func(p *ScanProgress) { p.ThumbDone++ },
		func(rel string) error {
			if quarantined[rel] {
				metrics.incr("thumbsQuarantined")
				return nil // known-undecodable; don't retry until released
			}
			var genErr error
			var generated bool
			if deep {
				generated, genErr = sc.thumbs.EnsureIndexed(rel, cacheIdx, metrics)
			} else {
				generated, genErr = sc.thumbs.EnsurePresent(rel, cacheIdx, metrics)
			}
			if genErr != nil {
				log.Printf("thumbnail %q: %v", rel, genErr)
				// A file we could read but not decode (even after repair) is
				// quarantined so future scans skip it and users don't see a
				// broken tile. Transient I/O errors are left to retry.
				if errors.Is(genErr, ErrUndecodable) {
					sc.quarantineMedia(lib, "thumbnails", rel, genErr)
					metrics.incr("thumbsQuarantined")
				}
			} else if generated {
				metrics.incr("thumbsGenerated")
			} else {
				metrics.incr("thumbsSkipped")
			}
			return genErr
		})
}

// quarantineMedia persists a photo that could not be decoded so future scans
// skip it and it is hidden from non-admin galleries. Failures to write the
// quarantine row are logged but never abort the scan.
func (sc *Scanner) quarantineMedia(lib *Library, phase, rel string, cause error) {
	if sc.store == nil {
		return
	}
	if err := sc.store.QuarantineMedia(rel, lib.ID, lib.Name, phase, cause.Error()); err != nil {
		log.Printf("quarantine media %q: %v", rel, err)
	}
}

// indexMetadata indexes per-photo metadata (EXIF + sidecar + geocode) for the
// given photos (phase "metadata"), with the same quick/deep semantics as
// indexPhotoMeta: quick skips already-indexed photos via a DB lookup with no
// source stat; deep re-checks each source's mtime/size.
func (sc *Scanner) indexMetadata(lib *Library, rels []string, jp *JobProgress, mode ScanMode, metrics *scanMetrics) {
	deep := mode == ScanDeep
	sc.runPhotoPhase(lib, "metadata", rels, jp, metrics,
		func(p *ScanProgress, total int) { p.MetaTotal = total; p.MetaDone = 0 },
		func(p *ScanProgress) { p.MetaDone++ },
		func(rel string) error { return sc.indexPhotoMeta(lib.ID, rel, deep, metrics) })
}

// indexPhotoMeta extracts and stores per-photo metadata (EXIF + Google sidecar
// JSON + reverse geocode) for the photo at the given URL token. Failures are
// logged but never abort the scan, mirroring thumbnail generation.
//
// In quick mode (deep=false) a photo already indexed at the current version is
// skipped with a single DB lookup and NO source stat, so a warm re-scan touches
// no files; only newly seen photos are read and indexed. In deep mode the
// source is stat'd and its mtime/size compared against the stored row, so a
// photo edited in place is re-indexed.
func (sc *Scanner) indexPhotoMeta(libraryID, rel string, deep bool, metrics *scanMetrics) error {
	if !deep {
		if _, _, pv, ok, err := sc.store.PhotoMetaStat(rel); err == nil && ok && pv == photoMetaVersion {
			metrics.incr("metaSkipped")
			return nil // already indexed at the current version; trust it
		}
	}

	abs := URLPathToAbs(rel)
	fi, err := os.Stat(abs)
	if err != nil {
		log.Printf("photo meta %q: stat: %v", rel, err)
		return fmt.Errorf("stat: %w", err)
	}
	mtime, size := fi.ModTime().Unix(), fi.Size()
	if deep {
		if pm, ps, pv, ok, err := sc.store.PhotoMetaStat(rel); err == nil && ok &&
			pm == mtime && ps == size && pv == photoMetaVersion {
			metrics.incr("metaSkipped")
			return nil // unchanged since last index, and indexed at the current version
		}
	}

	m := extractPhotoMeta(abs, metrics)
	m.PhotoPath = rel
	m.LibraryID = libraryID
	m.FileMtime = mtime
	m.FileSize = size
	if err := metrics.timeIt("meta.sql", func() error { return sc.store.UpsertPhotoMeta(m) }); err != nil {
		log.Printf("photo meta %q: upsert: %v", rel, err)
		return fmt.Errorf("upsert: %w", err)
	}
	metrics.incr("metaIndexed")
	return nil
}

// cleanupOrphanThumbs deletes cached thumbnails that no longer correspond to a
// source photo. It diffs the pre-built cache index against the set of thumbnail
// paths expected from the current photo list (rels); anything in the cache but
// not expected is an orphan (its source photo was deleted/moved) and is
// removed. Failures are logged but do not abort the pass. If the cache index
// was unavailable, cleanup is skipped (we cannot safely enumerate orphans).
//
// This is the deep scan's final reconciliation step and the only path that
// deletes cached thumbnails; the quick scan and thumbnail generation never do.
func (sc *Scanner) cleanupOrphanThumbs(lib *Library, rels []string, cacheIdx CacheIndex, jp *JobProgress) {
	if cacheIdx == nil {
		return
	}

	expected := make(map[string]struct{}, len(rels))
	for _, rel := range rels {
		expected[sc.thumbs.CachePathFor(rel)] = struct{}{}
	}

	// Also protect thumbnails for any cover/background photo still referenced by
	// the DB. Auto-derived covers are already in rels, but an admin-set cover may
	// point at a photo outside the normal listed-photo set; we must not delete
	// its thumbnail while the DB still references it. "@art/" uploads live in a
	// separate cache dir and never have a thumb here, so they are skipped.
	if sc.store != nil {
		refs, err := sc.store.ReferencedArtPhotos(lib.ID)
		if err != nil {
			log.Printf("cleanup: list referenced art for %s: %v", lib.ID, err)
		}
		for _, ref := range refs {
			if IsArtPath(ref) {
				continue
			}
			expected[sc.thumbs.CachePathFor(ref)] = struct{}{}
		}
	}

	// The thumbnail cache is a single root shared by every library (it mirrors
	// absolute source paths). Restrict orphan deletion to cache entries that live
	// under THIS library's cache subtree, so we never touch another library's
	// thumbnails. The prefix is derived from the library root the same way a real
	// thumbnail path is, then truncated to the directory portion.
	libPrefix := sc.thumbs.CacheDirFor(lib.RootPath)

	var orphans []string
	for dst := range cacheIdx {
		if !strings.HasPrefix(dst, libPrefix) {
			continue
		}
		if _, ok := expected[dst]; !ok {
			orphans = append(orphans, dst)
		}
	}

	total := len(orphans)
	sc.updateProgress(lib.ID, func(p *ScanProgress) {
		p.Phase = "cleanup"
		p.CleanupTotal = total
		p.CleanupDone = 0
		p.CurrentDir = ""
	})
	if jp != nil {
		jp.SetPhase("cleanup", total)
	}
	if total == 0 {
		return
	}

	for i, dst := range orphans {
		jp.WaitIfPaused()
		if jp.Canceled() {
			break
		}
		if err := sc.thumbs.RemoveThumb(dst); err != nil {
			log.Printf("cleanup orphan thumb %q: %v", dst, err)
		}
		sc.updateProgress(lib.ID, func(p *ScanProgress) { p.CleanupDone++ })
		if jp != nil {
			jp.SetProgress(i+1, total)
		}
	}
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

// listImageNames returns the sorted base names of every image file directly in
// dir. It is the raw enumeration used by the scan (which must see every file,
// including quarantined ones, so it can re-evaluate them); user-facing callers
// go through ListPhotos/FirstPhoto, which additionally drop quarantined photos.
func listImageNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
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
	return names, nil
}

// ListPhotos returns the image files in an album directory, sorted by name,
// EXCLUDING any photo quarantined as undecodable so broken files never surface
// in galleries, viewers, search, or covers (they appear only in the admin
// quarantine list).
func (sc *Scanner) ListPhotos(albumFSPath string) ([]Photo, error) {
	names, err := listImageNames(albumFSPath)
	if err != nil {
		return nil, err
	}
	out := make([]Photo, 0, len(names))
	for _, n := range names {
		token := AbsToURLPath(filepath.Join(albumFSPath, n))
		if sc.store != nil && sc.store.IsQuarantined(token) {
			continue
		}
		out = append(out, Photo{Name: n, Path: token})
	}
	return out, nil
}

// FirstPhoto returns the URL token of the first non-quarantined image in a
// directory (alphabetical), used as a fallback cover. Empty string if none.
func (sc *Scanner) FirstPhoto(dir string) string {
	names, err := listImageNames(dir)
	if err != nil {
		return ""
	}
	for _, n := range names {
		token := AbsToURLPath(filepath.Join(dir, n))
		if sc.store != nil && sc.store.IsQuarantined(token) {
			continue
		}
		return token
	}
	return ""
}

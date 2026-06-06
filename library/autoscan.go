package library

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SettingAutoScanHours is the settings key holding the periodic rescan interval
// in hours. "0" (or unset) disables the periodic rescan; the filesystem watcher
// stays active regardless.
const SettingAutoScanHours = "auto_scan_interval_hours"

// watchDebounce is how long the watcher waits after the last filesystem event
// for a library before kicking off a scan, so a burst of file drops collapses
// into a single rescan.
const watchDebounce = 5 * time.Second

// AutoScanner gives plex-photos Plex-like auto-detection of new content. It
// combines two triggers:
//
//   - a filesystem watcher (fsnotify) over every library root, so dropping new
//     folders/photos triggers a debounced rescan of that library; and
//   - a configurable periodic rescan of all libraries (none / every N hours),
//     as a safety net for changes the watcher may miss (e.g. network shares).
type AutoScanner struct {
	store   *Store
	scanner *Scanner

	mu       sync.Mutex
	interval time.Duration
	resetCh  chan struct{}
	stopCh   chan struct{}

	watcher   *fsnotify.Watcher
	debTimers map[string]*time.Timer // libraryID -> pending scan timer
}

// NewAutoScanner builds an auto-scanner. Call Start to begin watching/ticking.
func NewAutoScanner(store *Store, scanner *Scanner) *AutoScanner {
	return &AutoScanner{
		store:     store,
		scanner:   scanner,
		resetCh:   make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		debTimers: map[string]*time.Timer{},
	}
}

// Interval returns the currently configured periodic rescan interval. Zero
// means the periodic rescan is disabled.
func (a *AutoScanner) Interval() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.interval
}

// SetIntervalHours updates and persists the periodic rescan interval (in hours)
// and reconfigures the running ticker live. Zero disables periodic rescans.
func (a *AutoScanner) SetIntervalHours(hours int) error {
	if hours < 0 {
		hours = 0
	}
	if err := a.store.SetSetting(SettingAutoScanHours, strconv.Itoa(hours)); err != nil {
		return err
	}
	a.mu.Lock()
	a.interval = time.Duration(hours) * time.Hour
	a.mu.Unlock()
	select {
	case a.resetCh <- struct{}{}:
	default:
	}
	return nil
}

// Start loads the persisted interval, begins the periodic ticker loop and the
// filesystem watcher. It returns after wiring is in place; work runs in the
// background until Stop is called.
func (a *AutoScanner) Start() error {
	hours := 0
	if v, err := a.store.GetSetting(SettingAutoScanHours, "0"); err == nil {
		hours, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	a.mu.Lock()
	a.interval = time.Duration(hours) * time.Hour
	a.mu.Unlock()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	a.watcher = w

	if err := a.refreshWatches(); err != nil {
		log.Printf("autoscan: initial watch setup: %v", err)
	}

	go a.tickLoop()
	go a.watchLoop()

	log.Printf("autoscan: started (periodic=%s, watcher=on)", intervalLabel(a.Interval()))
	return nil
}

// Stop tears down the watcher and ticker.
func (a *AutoScanner) Stop() {
	close(a.stopCh)
	if a.watcher != nil {
		_ = a.watcher.Close()
	}
}

func intervalLabel(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	return d.String()
}

// tickLoop runs the periodic rescan, re-arming whenever the interval changes.
func (a *AutoScanner) tickLoop() {
	for {
		d := a.Interval()
		var c <-chan time.Time
		var timer *time.Timer
		if d > 0 {
			timer = time.NewTimer(d)
			c = timer.C
		}
		select {
		case <-a.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-a.resetCh:
			if timer != nil {
				timer.Stop()
			}
			// Loop to pick up the new interval.
		case <-c:
			log.Printf("autoscan: periodic rescan of all libraries")
			a.scanAll()
		}
	}
}

func (a *AutoScanner) scanAll() {
	libs, err := a.store.ListLibraries()
	if err != nil {
		log.Printf("autoscan: list libraries: %v", err)
		return
	}
	for _, lib := range libs {
		if err := a.scanner.Scan(lib, "auto-scan (scheduled)"); err != nil {
			log.Printf("autoscan: scan %s: %v", lib.ID, err)
		}
	}
	// New folders may have appeared; make sure they are watched too.
	if err := a.refreshWatches(); err != nil {
		log.Printf("autoscan: refresh watches: %v", err)
	}
}

// watchLoop handles fsnotify events, debouncing per library before scanning.
func (a *AutoScanner) watchLoop() {
	for {
		select {
		case <-a.stopCh:
			return
		case event, ok := <-a.watcher.Events:
			if !ok {
				return
			}
			a.handleFSEvent(event)
		case err, ok := <-a.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("autoscan: watcher error: %v", err)
		}
	}
}

func (a *AutoScanner) handleFSEvent(event fsnotify.Event) {
	// Ignore pure attribute/chmod churn.
	if event.Op == fsnotify.Chmod {
		return
	}
	lib := a.libraryForPath(event.Name)
	if lib == nil {
		return
	}
	// If a new directory was created, start watching it so nested drops are seen.
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			a.addWatchRecursive(event.Name)
		}
	}
	a.scheduleScan(lib)
}

// scheduleScan (re)arms the per-library debounce timer.
func (a *AutoScanner) scheduleScan(lib *Library) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.debTimers[lib.ID]; ok {
		t.Stop()
	}
	libID := lib.ID
	a.debTimers[libID] = time.AfterFunc(watchDebounce, func() {
		fresh, err := a.store.GetLibrary(libID)
		if err != nil {
			return
		}
		log.Printf("autoscan: change detected, rescanning library %q", fresh.Name)
		if err := a.scanner.Scan(fresh, "auto-scan (watcher)"); err != nil {
			log.Printf("autoscan: scan %s: %v", libID, err)
		}
		if err := a.refreshWatches(); err != nil {
			log.Printf("autoscan: refresh watches: %v", err)
		}
	})
}

// libraryForPath returns the library whose root contains the given path.
func (a *AutoScanner) libraryForPath(path string) *Library {
	libs, err := a.store.ListLibraries()
	if err != nil {
		return nil
	}
	clean := filepath.Clean(path)
	for _, lib := range libs {
		root := filepath.Clean(lib.RootPath)
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return lib
		}
	}
	return nil
}

// refreshWatches ensures every directory under every library root is watched.
// fsnotify is not recursive, so each directory must be added individually.
func (a *AutoScanner) refreshWatches() error {
	if a.watcher == nil {
		return nil
	}
	libs, err := a.store.ListLibraries()
	if err != nil {
		return err
	}
	for _, lib := range libs {
		a.addWatchRecursive(lib.RootPath)
	}
	return nil
}

func (a *AutoScanner) addWatchRecursive(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking siblings
		}
		if !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && path != root {
			return filepath.SkipDir
		}
		// Adding an already-watched path is a no-op error we can ignore.
		_ = a.watcher.Add(path)
		return nil
	})
}

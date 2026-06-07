package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
)

// SettingThumbFilter is the settings key holding the resampling filter used to
// downscale thumbnails. Cheaper filters cost less CPU per image at some quality
// cost; "lanczos" (the default) is the highest quality.
const SettingThumbFilter = "thumbnail_filter"

// defaultThumbFilter is the resampling filter used when none is configured.
const defaultThumbFilter = "lanczos"

// thumbFilters maps the persisted/API filter name to an imaging.ResampleFilter.
// Names are stable identifiers used by the settings API and UI.
var thumbFilters = map[string]imaging.ResampleFilter{
	"lanczos":    imaging.Lanczos,
	"catmullrom": imaging.CatmullRom,
	"linear":     imaging.Linear,
	"box":        imaging.Box,
	"nearest":    imaging.NearestNeighbor,
}

// resampleFilterFor resolves a filter name to its imaging filter, falling back
// to the default for unknown/empty names.
func resampleFilterFor(name string) imaging.ResampleFilter {
	if f, ok := thumbFilters[strings.ToLower(strings.TrimSpace(name))]; ok {
		return f
	}
	return imaging.Lanczos
}

// Thumbnailer generates and caches thumbnails on demand.
type Thumbnailer struct {
	cacheRoot string
	width     int

	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	filterName string
	filter     imaging.ResampleFilter
}

// NewThumbnailer creates a thumbnailer. cacheRoot is where generated thumbs are
// stored (mirroring the source tree). Source paths are absolute (already
// authorized by the caller against an accessible library root).
func NewThumbnailer(cacheRoot string, width int) *Thumbnailer {
	return &Thumbnailer{
		cacheRoot:  cacheRoot,
		width:      width,
		locks:      map[string]*sync.Mutex{},
		filterName: defaultThumbFilter,
		filter:     imaging.Lanczos,
	}
}

// Filter returns the configured resampling filter name.
func (t *Thumbnailer) Filter() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filterName
}

// SetFilter sets the resampling filter by name. Unknown names fall back to the
// default (lanczos). Returns the effective (normalized) filter name.
func (t *Thumbnailer) SetFilter(name string) string {
	norm := strings.ToLower(strings.TrimSpace(name))
	if _, ok := thumbFilters[norm]; !ok {
		norm = defaultThumbFilter
	}
	t.mu.Lock()
	t.filterName = norm
	t.filter = thumbFilters[norm]
	t.mu.Unlock()
	return norm
}

// resampleFilter returns the currently configured filter (thread-safe).
func (t *Thumbnailer) resampleFilter() imaging.ResampleFilter {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filter
}

// cachePath maps an absolute source image path to its cached thumbnail path,
// mirroring the source tree under cacheRoot. The drive-letter colon (Windows)
// is replaced so the key is a valid single path segment on every OS.
func (t *Thumbnailer) cachePath(srcFull string) string {
	key := strings.ReplaceAll(AbsToURLPath(srcFull), ":", "_")
	return filepath.Join(t.cacheRoot, filepath.FromSlash(key)+".thumb.jpg")
}

// pathLock serializes generation per output path to avoid duplicate work/races.
func (t *Thumbnailer) pathLock(key string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	l, ok := t.locks[key]
	if !ok {
		l = &sync.Mutex{}
		t.locks[key] = l
	}
	return l
}

// ThumbPath returns the cached thumbnail path for a photo URL token, generating
// it lazily if missing. The caller is responsible for having authorized the
// path against an accessible library root before calling this.
func (t *Thumbnailer) ThumbPath(token string) (string, error) {
	srcFull := URLPathToAbs(token)
	if !IsImage(srcFull) {
		return "", fmt.Errorf("not an image: %s", token)
	}
	srcInfo, err := os.Stat(srcFull)
	if err != nil {
		return "", err
	}

	dstFull := t.cachePath(srcFull)

	if thumbFresh(dstFull, srcInfo) {
		return dstFull, nil
	}

	lock := t.pathLock(dstFull)
	lock.Lock()
	defer lock.Unlock()

	// Re-check after acquiring the lock (another goroutine may have generated it).
	if thumbFresh(dstFull, srcInfo) {
		return dstFull, nil
	}

	if err := t.generate(srcFull, dstFull); err != nil {
		return "", err
	}
	return dstFull, nil
}

// Ensure generates (if missing/stale) the thumbnail for the given photo URL
// token. It returns true if a new thumbnail was written, false if a fresh one
// already existed. Used to pre-warm thumbs during scan.
func (t *Thumbnailer) Ensure(token string) (bool, error) {
	srcFull := URLPathToAbs(token)
	if !IsImage(srcFull) {
		return false, fmt.Errorf("not an image: %s", token)
	}
	srcInfo, err := os.Stat(srcFull)
	if err != nil {
		return false, err
	}
	dstFull := t.cachePath(srcFull)
	if thumbFresh(dstFull, srcInfo) {
		return false, nil
	}
	lock := t.pathLock(dstFull)
	lock.Lock()
	defer lock.Unlock()
	if thumbFresh(dstFull, srcInfo) {
		return false, nil
	}
	if err := t.generate(srcFull, dstFull); err != nil {
		return false, err
	}
	return true, nil
}

// thumbFresh reports whether the cached thumbnail at dst exists, is non-empty,
// and is at least as new as its source image. A thumb older than the source is
// considered stale (the source was replaced in place) and must be regenerated.
func thumbFresh(dst string, srcInfo os.FileInfo) bool {
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() == 0 {
		return false
	}
	return !fi.ModTime().Before(srcInfo.ModTime())
}

func (t *Thumbnailer) generate(src, dst string) error {
	img, err := imaging.Open(src, imaging.AutoOrientation(true))
	if err != nil {
		return fmt.Errorf("open image: %w", err)
	}
	thumb := imaging.Resize(img, t.width, 0, t.resampleFilter())

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create thumb dir: %w", err)
	}
	tmp := dst + ".tmp.jpg"
	if err := imaging.Save(thumb, tmp, imaging.JPEGQuality(82)); err != nil {
		return fmt.Errorf("save thumb: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("finalize thumb: %w", err)
	}
	return nil
}

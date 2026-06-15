package library

import (
	"fmt"
	"hash/fnv"
	"image"
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

// thumbLockStripes is the number of striped mutexes used to serialize
// generation per output path. A fixed array keeps memory constant regardless of
// library size; distinct paths that hash to the same stripe serialize against
// each other only briefly (harmless for the 1–8 generation workers in use).
const thumbLockStripes = 256

// Thumbnailer generates and caches thumbnails on demand.
type Thumbnailer struct {
	cacheRoot string
	width     int

	mu         sync.Mutex
	filterName string
	filter     imaging.ResampleFilter

	locks [thumbLockStripes]sync.Mutex
}

// NewThumbnailer creates a thumbnailer. cacheRoot is where generated thumbs are
// stored (mirroring the source tree). Source paths are absolute (already
// authorized by the caller against an accessible library root).
func NewThumbnailer(cacheRoot string, width int) *Thumbnailer {
	return &Thumbnailer{
		cacheRoot:  cacheRoot,
		width:      width,
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

// CachePathFor returns the absolute thumbnail cache path for a photo URL token,
// without generating anything. Used by the scan cleanup phase to compute the
// set of thumbnails that legitimately correspond to a source photo.
func (t *Thumbnailer) CachePathFor(token string) string {
	return t.cachePath(URLPathToAbs(token))
}

// CacheDirFor returns the cache directory prefix (with a trailing separator)
// under which all thumbnails for the given absolute source directory live. It
// applies the same path transform as cachePath so it can be used to scope cache
// operations (e.g. orphan cleanup) to a single library's subtree without
// affecting thumbnails from other libraries that share the global cache root.
func (t *Thumbnailer) CacheDirFor(srcDir string) string {
	key := strings.ReplaceAll(AbsToURLPath(srcDir), ":", "_")
	return filepath.Join(t.cacheRoot, filepath.FromSlash(key)) + string(filepath.Separator)
}

// RemoveThumb deletes a single cached thumbnail file. Missing files are not an
// error so cleanup is idempotent.
func (t *Thumbnailer) RemoveThumb(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// pathLock returns the striped mutex serializing generation for the given
// output path. The path is hashed into a fixed set of stripes, so memory stays
// constant no matter how many distinct thumbnails are generated.
func (t *Thumbnailer) pathLock(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &t.locks[h.Sum32()%thumbLockStripes]
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

	if err := t.generate(srcFull, dstFull, nil); err != nil {
		return "", err
	}
	return dstFull, nil
}

// CacheIndex is an in-memory snapshot of the thumbnail cache directory mapping
// absolute thumbnail paths to their modification time (UnixNano). It lets the
// scan phase decide whether a thumbnail already exists with a single map lookup
// instead of an os.Stat syscall per photo, which is the dominant cost when most
// thumbnails already exist (e.g. a re-scan of a large, already-warmed library).
//
// The modtime is only populated when the index is built with modtimes (deep
// scan); a quick-scan index records presence with a 0 value, since membership
// is all it needs.
type CacheIndex map[string]int64

// BuildCacheIndex walks the cache root once and records every existing
// thumbnail file. If the cache root does not exist yet, an empty index is
// returned.
//
// When needModTimes is true (deep scan) each entry is stat'd so its modtime is
// recorded, which the deep generation path compares against the source file to
// detect in-place edits. When false (quick scan) the walk is a pure directory
// enumeration with NO per-file stat: only the thumbnail's presence is recorded
// (modtime 0). A quick scan only tests membership, so skipping d.Info() turns
// the index build from ~N stats into a handful of directory listings — the key
// to a fast warm re-scan on a NAS. Empty/partial thumbnails are not a concern
// because generate() writes to a temp file and atomically renames into place,
// so a .thumb.jpg is never zero-length.
func (t *Thumbnailer) BuildCacheIndex(needModTimes bool) (CacheIndex, error) {
	idx := CacheIndex{}
	err := filepath.WalkDir(t.cacheRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".thumb.jpg") {
			return nil
		}
		if !needModTimes {
			idx[path] = 0
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		idx[path] = info.ModTime().UnixNano()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return idx, err
	}
	return idx, nil
}

// EnsureIndexed is like Ensure but consults a pre-built CacheIndex to avoid a
// per-photo stat of the thumbnail file. It still stats the source (needed to
// detect a source newer than its thumbnail) but skips the second syscall and
// the per-path lock map growth for thumbnails that are already fresh.
func (t *Thumbnailer) EnsureIndexed(token string, idx CacheIndex, m *scanMetrics) (bool, error) {
	srcFull := URLPathToAbs(token)
	if !IsImage(srcFull) {
		return false, fmt.Errorf("not an image: %s", token)
	}
	srcInfo, err := os.Stat(srcFull)
	if err != nil {
		return false, err
	}
	dstFull := t.cachePath(srcFull)
	if mod, ok := idx[dstFull]; ok && mod >= srcInfo.ModTime().UnixNano() {
		return false, nil
	}
	lock := t.pathLock(dstFull)
	lock.Lock()
	defer lock.Unlock()
	if thumbFresh(dstFull, srcInfo) {
		return false, nil
	}
	if err := t.generate(srcFull, dstFull, m); err != nil {
		return false, err
	}
	return true, nil
}

// EnsurePresent is the quick-scan counterpart to EnsureIndexed: it generates a
// thumbnail only when one does not already exist, and performs NO stat of the
// source file. An existing cached thumbnail is trusted regardless of the
// source's mtime, so detecting a photo edited in place (same filename, new
// content) is deliberately left to a deep scan. The dominant cost on a NAS is
// the per-file stat syscall, so skipping it makes a warm re-scan effectively
// free for already-thumbnailed photos. New photos are generated, which reads
// the source anyway, making the elided stat negligible there.
func (t *Thumbnailer) EnsurePresent(token string, idx CacheIndex, m *scanMetrics) (bool, error) {
	srcFull := URLPathToAbs(token)
	if !IsImage(srcFull) {
		return false, fmt.Errorf("not an image: %s", token)
	}
	dstFull := t.cachePath(srcFull)

	// The cache snapshot is the source of truth for "do we already have a
	// thumbnail for this path?" — a single in-memory lookup, no syscall.
	if idx != nil {
		if _, ok := idx[dstFull]; ok {
			return false, nil
		}
	}

	lock := t.pathLock(dstFull)
	lock.Lock()
	defer lock.Unlock()
	// Re-check on disk under the lock. With no index this is the only existence
	// check; with an index it guards against a racing worker. Either way it
	// stats the destination thumbnail, never the (NAS-resident) source.
	if fi, err := os.Stat(dstFull); err == nil && fi.Size() > 0 {
		return false, nil
	}
	if err := t.generate(srcFull, dstFull, m); err != nil {
		return false, err
	}
	return true, nil
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
	if err := t.generate(srcFull, dstFull, nil); err != nil {
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

// generate decodes the source image, downscales it, and writes the cached
// thumbnail atomically. When m is non-nil (scan path) the decode, resize, and
// encode steps are timed separately so the scan report can attribute the cost;
// m is nil on the lazy on-demand path and adds no overhead there.
func (t *Thumbnailer) generate(src, dst string, m *scanMetrics) error {
	var img image.Image
	if err := m.timeIt("thumb.decode", func() (err error) {
		img, err = imaging.Open(src, imaging.AutoOrientation(true))
		return err
	}); err != nil {
		return fmt.Errorf("open image: %w", err)
	}

	var thumb *image.NRGBA
	_ = m.timeIt("thumb.resize", func() error {
		thumb = imaging.Resize(img, t.width, 0, t.resampleFilter())
		return nil
	})

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create thumb dir: %w", err)
	}
	tmp := dst + ".tmp.jpg"
	if err := m.timeIt("thumb.encode", func() error {
		return imaging.Save(thumb, tmp, imaging.JPEGQuality(82))
	}); err != nil {
		return fmt.Errorf("save thumb: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("finalize thumb: %w", err)
	}
	return nil
}

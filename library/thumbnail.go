package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/disintegration/imaging"
)

// Thumbnailer generates and caches thumbnails on demand.
type Thumbnailer struct {
	photosRoot string
	cacheRoot  string
	width      int

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewThumbnailer creates a thumbnailer. photosRoot is the read-only photos mount,
// cacheRoot is where generated thumbs are stored (mirrors the photos tree).
func NewThumbnailer(photosRoot, cacheRoot string, width int) *Thumbnailer {
	return &Thumbnailer{
		photosRoot: photosRoot,
		cacheRoot:  cacheRoot,
		width:      width,
		locks:      map[string]*sync.Mutex{},
	}
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

// ThumbPath returns the cached thumbnail path for a photo path (relative to the
// photos root), generating it lazily if missing. The relative path is validated
// to stay confined under the photos root.
func (t *Thumbnailer) ThumbPath(relPath string) (string, error) {
	srcFull, err := ResolveUnderRoot(t.photosRoot, relPath)
	if err != nil {
		return "", err
	}
	if !IsImage(srcFull) {
		return "", fmt.Errorf("not an image: %s", relPath)
	}
	srcInfo, err := os.Stat(srcFull)
	if err != nil {
		return "", err
	}

	rel, err := RelToRoot(t.photosRoot, srcFull)
	if err != nil {
		return "", err
	}
	dstFull := filepath.Join(t.cacheRoot, rel+".thumb.jpg")

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
	thumb := imaging.Resize(img, t.width, 0, imaging.Lanczos)

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

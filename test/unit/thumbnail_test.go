package unit

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"plex-photos/library"
)

func writeTestJPEG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

// TestThumbRegeneratesWhenSourceNewer verifies the cache is content-aware via
// mtime: an unchanged source reuses the cached thumb, while a source modified
// in place (newer mtime) forces regeneration.
func TestThumbRegeneratesWhenSourceNewer(t *testing.T) {
	photosRoot := t.TempDir()
	cacheRoot := t.TempDir()
	src := filepath.Join(photosRoot, "album", "pic.jpg")
	writeTestJPEG(t, src, color.RGBA{0x20, 0x40, 0x80, 0xff})

	th := library.NewThumbnailer(photosRoot, cacheRoot, 100)

	dst, err := th.ThumbPath("album/pic.jpg")
	if err != nil {
		t.Fatalf("first ThumbPath: %v", err)
	}
	fi1, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat thumb: %v", err)
	}

	// Unchanged source -> same cached thumb (mtime unchanged).
	if _, err := th.ThumbPath("album/pic.jpg"); err != nil {
		t.Fatalf("second ThumbPath: %v", err)
	}
	fi2, _ := os.Stat(dst)
	if !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Fatalf("thumb was regenerated for an unchanged source")
	}

	// Replace the source in place with a newer mtime -> regeneration expected.
	writeTestJPEG(t, src, color.RGBA{0xff, 0x00, 0x00, 0xff})
	newer := fi1.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(src, newer, newer); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := th.ThumbPath("album/pic.jpg"); err != nil {
		t.Fatalf("third ThumbPath: %v", err)
	}
	fi3, _ := os.Stat(dst)
	if !fi3.ModTime().After(fi1.ModTime()) {
		t.Fatalf("stale thumb was not regenerated: old=%v new=%v", fi1.ModTime(), fi3.ModTime())
	}
}

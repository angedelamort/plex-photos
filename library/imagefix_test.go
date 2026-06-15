package library

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"errors"
)

// validJPEG returns the bytes of a tiny, well-formed JPEG.
func validJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestRepairJPEG(t *testing.T) {
	good := validJPEG(t)

	t.Run("trailing garbage is trimmed", func(t *testing.T) {
		withJunk := append(append([]byte{}, good...), 0x00, 0x11, 0x22, 0x33)
		fixed, ok := repairJPEG(withJunk)
		if !ok {
			t.Fatal("expected repair to report a change")
		}
		if !bytes.Equal(fixed, good) {
			t.Fatalf("expected trailing bytes trimmed to %d bytes, got %d", len(good), len(fixed))
		}
	})

	t.Run("missing EOI is appended", func(t *testing.T) {
		// Drop the final EOI marker (FF D9).
		truncated := good[:len(good)-2]
		fixed, ok := repairJPEG(truncated)
		if !ok {
			t.Fatal("expected repair to report a change")
		}
		if n := len(fixed); n < 2 || fixed[n-2] != 0xFF || fixed[n-1] != 0xD9 {
			t.Fatal("expected an EOI marker appended")
		}
	})

	t.Run("clean jpeg is left alone", func(t *testing.T) {
		if _, ok := repairJPEG(good); ok {
			t.Fatal("expected no change for an already-clean jpeg")
		}
	})

	t.Run("non-jpeg is rejected", func(t *testing.T) {
		if _, ok := repairJPEG([]byte("not a jpeg at all")); ok {
			t.Fatal("expected non-jpeg to be left untouched")
		}
	})
}

func TestDecodeImageResilient(t *testing.T) {
	dir := t.TempDir()
	good := validJPEG(t)

	t.Run("clean jpeg decodes", func(t *testing.T) {
		p := filepath.Join(dir, "good.jpg")
		if err := os.WriteFile(p, good, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeImageResilient(p); err != nil {
			t.Fatalf("expected clean jpeg to decode, got %v", err)
		}
	})

	t.Run("jpeg with trailing garbage is repaired and decodes", func(t *testing.T) {
		p := filepath.Join(dir, "junk.jpg")
		withJunk := append(append([]byte{}, good...), 0xDE, 0xAD, 0xBE, 0xEF)
		if err := os.WriteFile(p, withJunk, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeImageResilient(p); err != nil {
			t.Fatalf("expected repaired jpeg to decode, got %v", err)
		}
	})

	t.Run("undecodable content yields ErrUndecodable", func(t *testing.T) {
		p := filepath.Join(dir, "broken.jpg")
		// SOI marker followed by garbage: read succeeds, decode cannot recover.
		if err := os.WriteFile(p, []byte{0xFF, 0xD8, 0xFF, 0x01, 0x02, 0x03}, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := decodeImageResilient(p)
		if !errors.Is(err, ErrUndecodable) {
			t.Fatalf("expected ErrUndecodable, got %v", err)
		}
	})

	t.Run("missing file is a transient error, not ErrUndecodable", func(t *testing.T) {
		_, err := decodeImageResilient(filepath.Join(dir, "does-not-exist.jpg"))
		if err == nil {
			t.Fatal("expected an error for a missing file")
		}
		if errors.Is(err, ErrUndecodable) {
			t.Fatal("a missing file must not be classified as undecodable (would wrongly quarantine)")
		}
	})
}

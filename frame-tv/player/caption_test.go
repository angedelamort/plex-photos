package player

import (
	"bytes"
	"image/color"
	"testing"

	"github.com/disintegration/imaging"

	"plex-photos/library"
)

func encodeSolidJPEG(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := imaging.New(400, 300, color.NRGBA{R: r, G: g, B: b, A: 255})
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestBuildCaptionLines_NoExifFields(t *testing.T) {
	// A freshly generated JPEG has no EXIF; album/filename still work.
	img := writeTestImage(t)
	photo := library.PlaylistPhoto{Name: "sunset.jpg", Path: "lib/Trips/Italy/sunset.jpg"}

	if lines := buildCaptionLines(nil, img, photo); lines != nil {
		t.Fatalf("expected nil for no fields, got %v", lines)
	}

	lines := buildCaptionLines([]string{capAlbum, capFilename}, img, photo)
	if len(lines) != 2 || lines[0] != "Italy" || lines[1] != "sunset.jpg" {
		t.Fatalf("unexpected lines: %v", lines)
	}

	// EXIF-only fields produce nothing when the image has no EXIF.
	if lines := buildCaptionLines([]string{capDate, capYear, capLocation, capCamera}, img, photo); len(lines) != 0 {
		t.Fatalf("expected no EXIF-derived lines, got %v", lines)
	}
}

func TestCaptionHelpers(t *testing.T) {
	if got := yearOf("2021-07-04 12:00:00"); got != "2021" {
		t.Fatalf("yearOf = %q", got)
	}
	if got := folderName("lib/Trips/Italy/sunset.jpg"); got != "Italy" {
		t.Fatalf("folderName = %q", got)
	}
	if got := folderName("sunset.jpg"); got != "" {
		t.Fatalf("folderName(no dir) = %q", got)
	}
	ex := &library.ExifInfo{Camera: "Canon R6", Aperture: "f/2.8", ISO: "100", FocalLength: "50 mm"}
	if got := cameraLine(ex); got != "Canon R6 · f/2.8 · ISO 100 · 50 mm" {
		t.Fatalf("cameraLine = %q", got)
	}
	if isoLabel("") != "" || isoLabel("200") != "ISO 200" {
		t.Fatal("isoLabel mismatch")
	}
}

func TestAutoMatte(t *testing.T) {
	// Dark photo → white mat; bright photo → black mat.
	if got := autoMatte(encodeSolidJPEG(t, 12, 12, 12)); got != matteModernWhite {
		t.Fatalf("dark photo: got %q", got)
	}
	if got := autoMatte(encodeSolidJPEG(t, 240, 240, 240)); got != matteModernBlack {
		t.Fatalf("bright photo: got %q", got)
	}
	// Mid-tone warm photo → warm mat.
	if got := autoMatte(encodeSolidJPEG(t, 170, 120, 60)); got != matteModernWarm {
		t.Fatalf("warm photo: got %q", got)
	}
	// Garbage decodes to the safe default rather than panicking.
	if got := autoMatte([]byte("not an image")); got != matteModernWhite {
		t.Fatalf("bad input: got %q", got)
	}
}

// drawCaption must not change the canvas size and must not panic for any corner.
func TestPrepareJPEG_WithCaption(t *testing.T) {
	for corner := 0; corner < 4; corner++ {
		out, err := prepareJPEG(encodeJPEG(t, 3000, 2000), DisplayOptions{
			Mode:          ModeBlurFill,
			Caption:       []string{"Italy", "2021-07-04", "Canon R6 · f/2.8"},
			CaptionCorner: corner,
		})
		if err != nil {
			t.Fatalf("corner %d: %v", corner, err)
		}
		if dx, dy := decodeBounds(t, out); dx != tvWidth || dy != tvHeight {
			t.Fatalf("corner %d: got %dx%d", corner, dx, dy)
		}
	}
}

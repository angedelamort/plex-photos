package player

import (
	"bytes"
	"image/color"
	"testing"

	"github.com/disintegration/imaging"
)

// encodeJPEG renders a solid w×h image and encodes it as JPEG bytes.
func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := imaging.New(w, h, color.NRGBA{R: 80, G: 120, B: 200, A: 255})
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG); err != nil {
		t.Fatalf("encode source: %v", err)
	}
	return buf.Bytes()
}

func decodeBounds(t *testing.T, out []byte) (int, int) {
	t.Helper()
	img, err := imaging.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// Every full-canvas mode must yield an exact panel-sized image.
func TestPrepareJPEG_FullPanelModes(t *testing.T) {
	cases := []struct {
		name string
		opt  DisplayOptions
		w, h int
	}{
		{"blur-fill portrait", DisplayOptions{Mode: ModeBlurFill}, 1200, 1600},
		{"blur-fill landscape", DisplayOptions{Mode: ModeBlurFill}, 4000, 3000},
		{"fill", DisplayOptions{Mode: ModeFill}, 1200, 1600},
		{"fit-color no border", DisplayOptions{Mode: ModeFitColor, BgColor: "#102030"}, 1200, 1600},
		{"fit-color border", DisplayOptions{Mode: ModeFitColor, BgColor: "#ffffff", BorderPct: 10}, 4000, 3000},
		{"unknown defaults to blur-fill", DisplayOptions{Mode: "bogus"}, 800, 800},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := prepareJPEG(encodeJPEG(t, c.w, c.h), c.opt)
			if err != nil {
				t.Fatalf("prepareJPEG: %v", err)
			}
			if dx, dy := decodeBounds(t, out); dx != tvWidth || dy != tvHeight {
				t.Fatalf("got %dx%d, want %dx%d", dx, dy, tvWidth, tvHeight)
			}
		})
	}
}

// tv-matte fits within the panel and does not upscale small sources.
func TestPrepareJPEG_TVMatteFits(t *testing.T) {
	out, err := prepareJPEG(encodeJPEG(t, 4000, 3000), DisplayOptions{Mode: ModeTVMatte})
	if err != nil {
		t.Fatalf("prepareJPEG: %v", err)
	}
	dx, dy := decodeBounds(t, out)
	if dx > tvWidth || dy > tvHeight {
		t.Fatalf("output %dx%d exceeds panel", dx, dy)
	}
	if dy != tvHeight { // 4:3 source is height-bound in a 16:9 panel
		t.Fatalf("expected height %d, got %d", tvHeight, dy)
	}

	out, _ = prepareJPEG(encodeJPEG(t, 200, 100), DisplayOptions{Mode: ModeTVMatte})
	if dx, dy := decodeBounds(t, out); dx != 200 || dy != 100 {
		t.Fatalf("small image was resized: got %dx%d, want 200x100", dx, dy)
	}
}

func TestParseHexColor(t *testing.T) {
	c := parseHexColor("#10a0ff")
	if c.R != 0x10 || c.G != 0xa0 || c.B != 0xff || c.A != 255 {
		t.Fatalf("bad parse: %+v", c)
	}
	// Invalid inputs fall back to black.
	for _, s := range []string{"", "xyz", "#12345", "12g456"} {
		if c := parseHexColor(s); c.R != 0 || c.G != 0 || c.B != 0 || c.A != 255 {
			t.Fatalf("expected black for %q, got %+v", s, c)
		}
	}
}

func TestPrepareJPEG_Errors(t *testing.T) {
	if _, err := prepareJPEG(nil, DisplayOptions{Mode: ModeBlurFill}); err == nil {
		t.Fatal("expected error for empty input")
	}
	if _, err := prepareJPEG([]byte("not an image"), DisplayOptions{Mode: ModeBlurFill}); err == nil {
		t.Fatal("expected error for non-image input")
	}
}

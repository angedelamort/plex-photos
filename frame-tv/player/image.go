package player

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strconv"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Frame TV native panel resolution. Composed images target this exact size so
// the result looks identical regardless of the TV's own matte handling.
const (
	tvWidth   = 3840
	tvHeight  = 2160
	jpegQual  = 95 // Samsung's recommended quality for art stills; cleaner gradients than 90
	maxDecode = 64 << 20 // guard against absurd source files
)

// Display modes for composing a photo onto the panel.
const (
	ModeBlurFill = "blur-fill" // photo centered over a blurred, zoomed copy of itself
	ModeFill     = "fill"      // crop to fill the whole panel (no bars)
	ModeFitColor = "fit-color" // photo centered on a solid color, optional margin
	ModeTVMatte  = "tv-matte"  // just fit; the TV draws its own matte/letterbox
)

// Caption corners (rotated each swap to avoid uneven panel wear).
const (
	CornerTopLeft = iota
	CornerTopRight
	CornerBottomRight
	CornerBottomLeft
)

// DisplayOptions describes how to compose a photo for a TV.
type DisplayOptions struct {
	Mode      string
	BgColor   string // #rrggbb, used by fit-color
	BorderPct int    // 0..40, margin as % of panel height (fit-color)
	// Caption is the optional metadata overlay (one entry per line). Corner
	// selects which corner it is anchored to.
	Caption       []string
	CaptionCorner int
}

// blurSigma controls how soft the blur-fill background is. Tuned for 4K.
const blurSigma = 28.0

// prepareJPEG decodes a source image and composes it for the panel according to
// opt, returning JPEG bytes ready to upload. tv-matte returns the fitted image
// untouched (the TV frames it); the other modes return a full-panel canvas.
func prepareJPEG(src []byte, opt DisplayOptions) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	if len(src) > maxDecode {
		return nil, fmt.Errorf("image too large (%d bytes)", len(src))
	}

	img, err := imaging.Decode(bytes.NewReader(src), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	var out image.Image
	switch opt.Mode {
	case ModeFill:
		out = imaging.Fill(img, tvWidth, tvHeight, imaging.Center, imaging.Lanczos)
	case ModeFitColor:
		out = composeFitColor(img, parseHexColor(opt.BgColor), opt.BorderPct)
	case ModeBlurFill:
		out = composeBlurFill(img)
	case ModeTVMatte, "":
		// Fit within the panel; the TV applies its matte / black bars. Never
		// upscales beyond the source, keeping small photos crisp.
		out = imaging.Fit(img, tvWidth, tvHeight, imaging.Lanczos)
	default:
		out = composeBlurFill(img)
	}

	if len(opt.Caption) > 0 {
		out = drawCaption(out, opt.Caption, opt.CaptionCorner)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: jpegQual}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// composeFitColor centers the photo (fit, optional margin) on a solid canvas.
func composeFitColor(img image.Image, bg color.NRGBA, borderPct int) image.Image {
	if borderPct < 0 {
		borderPct = 0
	}
	if borderPct > 40 {
		borderPct = 40
	}
	margin := tvHeight * borderPct / 100
	innerW := tvWidth - 2*margin
	innerH := tvHeight - 2*margin
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	fitted := imaging.Fit(img, innerW, innerH, imaging.Lanczos)
	canvas := imaging.New(tvWidth, tvHeight, bg)
	return imaging.PasteCenter(canvas, fitted)
}

// composeBlurFill puts the photo over a blurred, panel-filling copy of itself.
func composeBlurFill(img image.Image) image.Image {
	bg := imaging.Fill(img, tvWidth, tvHeight, imaging.Center, imaging.Lanczos)
	bg = imaging.Blur(bg, blurSigma)
	// Darken slightly so the in-focus photo stands out from its backdrop.
	bg = imaging.AdjustBrightness(bg, -20)
	fg := imaging.Fit(img, tvWidth, tvHeight, imaging.Lanczos)
	return imaging.PasteCenter(bg, fg)
}

// Matte ids understood by the TV (style_color). Auto picks among the Modern
// colors per photo.
const (
	MatteAuto        = "auto"
	matteModernWhite = "modern_polar"
	matteModernWarm  = "modern_warm"
	matteModernBlack = "modern_black"
)

// autoMatte chooses a Modern mat color that contrasts the photo's edges so the
// image pops inside the frame. It inspects only the border ring (the part the
// mat sits against). Falls back to white on any decode error.
func autoMatte(raw []byte) string {
	img, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return matteModernWhite
	}
	// Downscale for a fast, smooth average; exact pixels don't matter here.
	small := imaging.Resize(img, 200, 0, imaging.Box)
	b := small.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return matteModernWhite
	}
	mx, my := w/10, h/10 // outer 10% ring

	var sumLum, sumR, sumB, n float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= mx && x < w-mx && y >= my && y < h-my {
				continue // interior: skip
			}
			c := small.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			r, g, bl := float64(c.R), float64(c.G), float64(c.B)
			sumLum += 0.2126*r + 0.7152*g + 0.0722*bl
			sumR += r
			sumB += bl
			n++
		}
	}
	if n == 0 {
		return matteModernWhite
	}
	lum := sumLum / n
	warmth := (sumR - sumB) / n

	switch {
	case lum < 90: // dark edges → bright white mat for contrast
		return matteModernWhite
	case lum > 165: // bright edges → black mat for contrast
		return matteModernBlack
	case warmth > 12: // mid-tone & warm → cream blends/contrasts pleasantly
		return matteModernWarm
	default:
		return matteModernWhite
	}
}

var (
	capFontOnce sync.Once
	capFont     *opentype.Font
)

// captionFont returns the embedded Go regular font, parsed once.
func captionFont() *opentype.Font {
	capFontOnce.Do(func() {
		if f, err := opentype.Parse(goregular.TTF); err == nil {
			capFont = f
		}
	})
	return capFont
}

// drawCaption overlays the given lines in a corner of src as white text with a
// soft drop shadow for legibility over any photo. Right-side corners are
// right-aligned; bottom corners anchor the block to the bottom. The font size
// scales with the image height so it reads well on the panel and on the smaller
// tv-matte image alike.
func drawCaption(src image.Image, lines []string, corner int) image.Image {
	f := captionFont()
	if f == nil || len(lines) == 0 {
		return src
	}
	dst := imaging.Clone(src) // *image.NRGBA implements draw.Image
	W, H := dst.Bounds().Dx(), dst.Bounds().Dy()

	size := float64(H) * 0.022
	if size < 16 {
		size = 16
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return dst
	}
	defer face.Close()

	m := face.Metrics()
	lineH := (m.Ascent + m.Descent).Ceil() + int(size*0.3)
	margin := int(float64(H) * 0.03)
	rightAlign := corner == CornerTopRight || corner == CornerBottomRight
	bottom := corner == CornerBottomRight || corner == CornerBottomLeft

	blockH := lineH * len(lines)
	var firstBaseline int
	if bottom {
		firstBaseline = H - margin - blockH + m.Ascent.Ceil()
	} else {
		firstBaseline = margin + m.Ascent.Ceil()
	}

	white := image.NewUniform(color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	shadow := image.NewUniform(color.NRGBA{A: 170})
	off := int(size * 0.07)
	if off < 2 {
		off = 2
	}

	d := &font.Drawer{Dst: dst, Face: face}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		w := d.MeasureString(line).Ceil()
		x := margin
		if rightAlign {
			x = W - margin - w
		}
		y := firstBaseline + i*lineH

		d.Src = shadow
		d.Dot = fixed.P(x+off, y+off)
		d.DrawString(line)

		d.Src = white
		d.Dot = fixed.P(x, y)
		d.DrawString(line)
	}
	return dst
}

// parseHexColor parses "#rrggbb" (or "rrggbb"); defaults to black on error.
func parseHexColor(s string) color.NRGBA {
	black := color.NRGBA{A: 255}
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(s) != 6 {
		return black
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return black
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

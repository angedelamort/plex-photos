package tv

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"
	"time"
)

// TestProbeLiveTV talks to a real TV on your network. It is skipped unless
// FRAME_TV_IP is set, so it never runs in CI or breaks `go test ./...`.
//
// Run it against your Frame TV (PowerShell):
//
//	$env:FRAME_TV_IP = "10.0.0.83"
//	go test ./frame-tv/tv -run TestProbeLiveTV -v
//
// The first connection may show an "Allow this device?" prompt on the TV.
// Accept it; the printed token can be reused later to skip the prompt via
// FRAME_TV_TOKEN.
func TestProbeLiveTV(t *testing.T) {
	ip := os.Getenv("FRAME_TV_IP")
	if ip == "" {
		t.Skip("set FRAME_TV_IP to probe a real TV (e.g. FRAME_TV_IP=10.0.0.83)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res := Probe(ctx, ip, os.Getenv("FRAME_TV_TOKEN"))
	t.Logf("\n%s", res.String())

	if !res.Reachable {
		t.Fatalf("TV at %s was not reachable over HTTP:8001", ip)
	}
	if !res.ArtModeSupported {
		t.Fatalf("TV at %s does not advertise Frame/Art Mode support", ip)
	}
}

// TestUploadAndShowLiveTV exercises the whole goal end-to-end against a real
// TV: generate a 4K photo, upload it with a matte, and select it for display.
// After it passes, the test image should be visible on the TV in Art Mode.
//
// Run it (PowerShell):
//
//	$env:FRAME_TV_IP = "10.0.0.83"
//	go test ./frame-tv/tv -run TestUploadAndShowLiveTV -v
//
// Optional: $env:FRAME_TV_MATTE = "none" to skip matting.
func TestUploadAndShowLiveTV(t *testing.T) {
	ip := os.Getenv("FRAME_TV_IP")
	if ip == "" {
		t.Skip("set FRAME_TV_IP to upload to a real TV (e.g. FRAME_TV_IP=10.0.0.83)")
	}

	matte := os.Getenv("FRAME_TV_MATTE")
	if matte == "" {
		matte = "modern_apricot"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	art, err := DialArt(ctx, ip, os.Getenv("FRAME_TV_TOKEN"))
	if err != nil {
		t.Fatalf("connect art channel: %v", err)
	}
	defer art.Close()

	if mattes, err := art.GetMatteList(); err != nil {
		t.Logf("get_matte_list failed (non-fatal): %v", err)
	} else {
		t.Logf("matte types:  %v", mattes.Types)
		t.Logf("matte colors: %v", mattes.Colors)
	}

	photo := make4KTestJPEG(t)
	t.Logf("uploading %d bytes with matte %q", len(photo), matte)

	cid, err := art.Display(photo, "jpg", matte)
	if err != nil {
		// A bad matte id is the most likely failure; retry unmatted so we still
		// prove the upload+show pipeline works.
		t.Logf("display with matte %q failed: %v; retrying with no matte", matte, err)
		cid, err = art.Display(photo, "jpg", MatteNone)
		if err != nil {
			t.Fatalf("display image: %v", err)
		}
	}

	t.Logf("SUCCESS: uploaded and now showing content_id %q on the TV", cid)
}

// make4KTestJPEG renders a 3840x2160 diagonal gradient and encodes it as JPEG,
// matching the Frame's native landscape resolution.
func make4KTestJPEG(t *testing.T) []byte {
	t.Helper()
	const w, h = 3840, 2160
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: uint8((x + y) * 255 / (w + h)),
				A: 0xff,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

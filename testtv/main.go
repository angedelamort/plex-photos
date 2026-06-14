// Command testtv sends a single photo to a configured Frame TV, bypassing the
// playlist/swap loop, so we can isolate whether a specific option (e.g. matte)
// is what makes a real TV reject the image.
//
// Usage (PowerShell):
//
//	$env:DATA_PATH="./testdata/config"; go run ./testtv -matte none
//	go run ./testtv -tv "Living room" -matte modern_apricot
//	go run ./testtv -photo "C:\path\to\pic.jpg"
//
// With no -photo it generates a synthetic gradient JPEG so it works even when
// the photos library is empty.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strings"
	"time"

	"github.com/disintegration/imaging"

	"plex-photos/frame-tv/player"
	"plex-photos/frame-tv/tv"
	"plex-photos/library"
)

func main() {
	dataPath := flag.String("data", envOr("DATA_PATH", "./testdata/config"), "config dir holding plex-photos.db")
	tvName := flag.String("tv", "", "TV name to target (default: first configured TV)")
	matte := flag.String("matte", "none", `matte id, e.g. "none" or "modern_apricot"`)
	photoPath := flag.String("photo", "", "path to a JPEG/PNG to send (default: synthetic gradient)")
	filter := flag.String("filter", "", `Art Mode post-process effect to apply after select, e.g. "Wash", "Pastel", "Feuve", "Ink" (see -filters)`)
	nowait := flag.Bool("nowait", false, "use fire-and-forget select_image (don't block on the TV's ack) — test for matte")
	pipeline := flag.Bool("pipeline", false, "reproduce the app's real flow: load the TV's saved playlist, resolve each photo on disk, compose a 4K JPEG, then upload+select the first one")
	rotate := flag.Int("rotate", 0, "rotate the first N photos of the TV's saved playlist on an interval (like the app); 0 = single shot")
	interval := flag.Int("interval", 15, "seconds between swaps in -rotate mode")
	rounds := flag.Int("rounds", 3, "number of full passes through the photos in -rotate mode")
	listMattes := flag.Bool("mattes", false, "connect and print the matte types/colors this TV actually supports, then exit")
	listFilters := flag.Bool("filters", false, "connect and print the Art Mode photo filters (post-process effects) this TV supports, then exit")
	flag.Parse()

	if *listMattes {
		if err := showMattes(*dataPath, *tvName); err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *listFilters {
		if err := showFilters(*dataPath, *tvName); err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *rotate > 0 {
		if err := rotateShow(*dataPath, *tvName, *matte, *filter, *rotate, *interval, *rounds); err != nil {
			fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*dataPath, *tvName, *matte, *photoPath, *filter, *pipeline, *nowait); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run(dataPath, tvName, matte, photoPath, filter string, pipeline, nowait bool) error {
	db, err := library.OpenDB(dataPath)
	if err != nil {
		return fmt.Errorf("open db (%s): %w", dataPath, err)
	}
	defer db.Close()

	store := player.NewStore(db)
	tvs, err := store.List()
	if err != nil {
		return fmt.Errorf("list tvs: %w", err)
	}
	if len(tvs) == 0 {
		return fmt.Errorf("no TVs configured in %s", dataPath)
	}

	target := tvs[0]
	if tvName != "" {
		target = nil
		for _, t := range tvs {
			if strings.EqualFold(t.Name, tvName) {
				target = t
				break
			}
		}
		if target == nil {
			return fmt.Errorf("no TV named %q (have %d configured)", tvName, len(tvs))
		}
	}

	fmt.Printf("Target TV : %s (%s)  hasToken=%v\n", target.Name, target.IP, target.Token != "")
	fmt.Printf("Matte     : %s\n", matte)

	var data []byte
	var ft string
	if pipeline {
		data, ft, err = pipelinePhoto(db, store, target.ID)
	} else {
		data, ft, err = loadImage(photoPath)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Image     : %d bytes (%s)\n", len(data), ft)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("Connecting to art channel... (approve the prompt on the TV if it appears)")
	art, err := tv.DialArt(ctx, target.IP, target.Token)
	if err != nil {
		return fmt.Errorf("dial art: %w", err)
	}
	defer art.Close()

	// Persist a freshly-issued token so the next connect skips the prompt.
	if tok := art.Token(); tok != "" && tok != target.Token {
		if err := store.SetToken(target.ID, tok); err != nil {
			fmt.Printf("warn: could not persist new token: %v\n", err)
		} else {
			fmt.Println("Captured & saved a new auth token (future connects won't prompt).")
		}
	}

	if ver, err := art.APIVersion(); err != nil {
		fmt.Printf("api_version: error: %v\n", err)
	} else {
		fmt.Printf("api_version: %s\n", ver)
	}
	if st, err := art.ArtModeStatus(); err != nil {
		fmt.Printf("artmode    : error: %v\n", err)
	} else {
		fmt.Printf("artmode    : %s\n", st)
	}

	if nowait {
		fmt.Println("Uploading, then fire-and-forget select (no ack wait)...")
		cid, err := art.Upload(data, ft, matte)
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		if err := art.SelectImageNoWait(cid, true); err != nil {
			return fmt.Errorf("select (nowait): %w", err)
		}
		fmt.Printf("OK: select emitted (content_id=%s); holding connection 10s so the TV can render...\n", cid)
		time.Sleep(10 * time.Second)
		fmt.Println("Done holding. If the matted photo is on screen with no TV error, the fix works.")
		return nil
	}

	fmt.Println("Uploading + selecting one photo...")
	cid, err := art.Display(data, ft, matte)
	if err != nil {
		return fmt.Errorf("display: %w", err)
	}
	fmt.Printf("OK: photo shown (content_id=%s)\n", cid)

	if filter != "" {
		fmt.Printf("Applying post-process filter %q...\n", filter)
		if err := art.SetPhotoFilter(cid, filter); err != nil {
			return fmt.Errorf("set_photo_filter %q: %w", filter, err)
		}
		fmt.Printf("OK: filter %q applied\n", filter)
	}
	return nil
}

// rotateShow mirrors the app's swap loop: it holds ONE art connection open,
// composes each photo to a 4K JPEG, pre-uploads the next photo while the
// current one is on screen, and selects on a fixed interval — applying a matte.
// It runs `rounds` full passes through the first `count` playlist photos.
func rotateShow(dataPath, tvName, matte, filter string, count, intervalSec, rounds int) error {
	db, err := library.OpenDB(dataPath)
	if err != nil {
		return fmt.Errorf("open db (%s): %w", dataPath, err)
	}
	defer db.Close()

	store := player.NewStore(db)
	target, err := pickTV(store, tvName)
	if err != nil {
		return err
	}

	st, err := store.LoadState(target.ID)
	if err != nil {
		return fmt.Errorf("load saved state: %w", err)
	}
	if st.Owner == "" || st.PlaylistID == "" {
		return fmt.Errorf("no saved playlist for this TV; press Play in the app once first")
	}

	lib := library.NewStore(db)
	photos, err := lib.PlaylistPhotos(st.Owner, st.PlaylistID)
	if err != nil {
		return fmt.Errorf("playlist photos: %w", err)
	}
	if len(photos) == 0 {
		return fmt.Errorf("playlist is empty")
	}
	if count < len(photos) {
		photos = photos[:count]
	}
	n := len(photos)

	fmt.Printf("Target TV : %s (%s)  hasToken=%v\n", target.Name, target.IP, target.Token != "")
	fmt.Printf("Rotation  : %d photos, every %ds, %d passes, matte=%s, filter=%s\n", n, intervalSec, rounds, matte, orNone(filter))

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(intervalSec*n*rounds+60)*time.Second)
	defer cancel()

	dialCtx, dialCancel := context.WithTimeout(ctx, 30*time.Second)
	art, err := tv.DialArt(dialCtx, target.IP, target.Token)
	dialCancel()
	if err != nil {
		return fmt.Errorf("dial art: %w", err)
	}
	defer art.Close()
	if tok := art.Token(); tok != "" && tok != target.Token {
		_ = store.SetToken(target.ID, tok)
	}

	// compose a playlist photo to a 4K letterboxed JPEG (so the matte is visible
	// around it, like the app's tv-matte mode).
	compose := func(p library.PlaylistPhoto) ([]byte, error) {
		abs, err := lib.ResolvePhotoFile(p.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", p.Name, err)
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p.Name, err)
		}
		img, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", p.Name, err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, imaging.Fit(img, 3840, 2160, imaging.Lanczos), &jpeg.Options{Quality: 95}); err != nil {
			return nil, fmt.Errorf("encode %s: %w", p.Name, err)
		}
		return buf.Bytes(), nil
	}

	// uploadFiltered composes + uploads a photo and applies the post-process
	// filter WHILE IT IS OFF-SCREEN, so when it is later selected it appears
	// already filtered — no visible "develop the effect live" transition.
	uploadFiltered := func(p library.PlaylistPhoto) (string, error) {
		jpegBytes, err := compose(p)
		if err != nil {
			return "", err
		}
		cid, err := art.Upload(jpegBytes, "jpg", matte)
		if err != nil {
			return "", fmt.Errorf("upload %s: %w", p.Name, err)
		}
		if filter != "" {
			if err := art.SetPhotoFilter(cid, filter); err != nil {
				fmt.Printf("warn: pre-filter %q on %s failed: %v\n", filter, p.Name, err)
			}
		}
		return cid, nil
	}

	total := n * rounds
	var shown string  // content_id currently on the panel
	var staged string // content_id pre-uploaded AND pre-filtered for the upcoming photo

	for i := 0; i < total; i++ {
		idx := i % n
		cur := photos[idx]

		if staged == "" {
			cid, err := uploadFiltered(cur)
			if err != nil {
				return err
			}
			staged = cid
		}

		// The image is already filtered off-screen; selecting just reveals it.
		if err := art.SelectImage(staged, true); err != nil {
			return fmt.Errorf("select %s: %w", cur.Name, err)
		}
		fmt.Printf("[%02d/%d] showing %-40s content_id=%s\n", i+1, total, photoLabel(cur), staged)

		// Clean up the previously shown image (best effort), like the app.
		if shown != "" && shown != staged {
			_ = art.DeleteImages(shown)
		}
		shown = staged
		staged = ""

		// Pre-upload AND pre-filter the NEXT photo on this same connection while
		// the current one is displayed — so its filter is baked in off-screen.
		if i+1 < total {
			nphoto := photos[(i+1)%n]
			if cid, err := uploadFiltered(nphoto); err != nil {
				fmt.Printf("warn: preload %s failed: %v\n", nphoto.Name, err)
			} else {
				staged = cid
			}
		}

		// Wait out the interval, pinging so the TV doesn't drop the idle socket.
		if i+1 < total {
			if !sleepKeepAlive(ctx, art, time.Duration(intervalSec)*time.Second) {
				fmt.Println("interrupted; stopping rotation")
				break
			}
		}
	}
	fmt.Println("Rotation complete.")
	return nil
}

// orNone renders an empty filter selection as "none" for logging.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// photoLabel returns a short display name for log lines.
func photoLabel(p library.PlaylistPhoto) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	if i := strings.LastIndexAny(p.Path, "/\\"); i >= 0 {
		return p.Path[i+1:]
	}
	return p.Path
}

// showMattes connects and prints the matte shapes/colors the TV reports, so we
// can check whether the matte ids the app offers actually exist on this set.
func showMattes(dataPath, tvName string) error {
	db, err := library.OpenDB(dataPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	store := player.NewStore(db)
	target, err := pickTV(store, tvName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	art, err := tv.DialArt(ctx, target.IP, target.Token)
	if err != nil {
		return fmt.Errorf("dial art: %w", err)
	}
	defer art.Close()

	ml, err := art.GetMatteList()
	if err != nil {
		return fmt.Errorf("get_matte_list: %w", err)
	}
	fmt.Printf("Matte types : %v\n", ml.Types)
	fmt.Printf("Matte colors: %v\n", ml.Colors)
	return nil
}

// showFilters connects and prints the Art Mode photo filters the TV supports —
// these are the "post process" effects the SmartThings app applies.
func showFilters(dataPath, tvName string) error {
	db, err := library.OpenDB(dataPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	store := player.NewStore(db)
	target, err := pickTV(store, tvName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	art, err := tv.DialArt(ctx, target.IP, target.Token)
	if err != nil {
		return fmt.Errorf("dial art: %w", err)
	}
	defer art.Close()

	filters, err := art.GetPhotoFilterList()
	if err != nil {
		return fmt.Errorf("get_photo_filter_list: %w", err)
	}
	fmt.Printf("Photo filters (%d):\n", len(filters))
	for _, f := range filters {
		fmt.Printf("  %-16s %s\n", f.ID, f.Name)
	}
	return nil
}

// pickTV resolves the target TV by name (or the first configured TV).
func pickTV(store *player.Store, tvName string) (*player.TV, error) {
	tvs, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("list tvs: %w", err)
	}
	if len(tvs) == 0 {
		return nil, fmt.Errorf("no TVs configured")
	}
	if tvName == "" {
		return tvs[0], nil
	}
	for _, t := range tvs {
		if strings.EqualFold(t.Name, tvName) {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no TV named %q", tvName)
}

// sleepKeepAlive waits for d, pinging the TV every 10s so the idle art socket
// stays open. Returns false if the context is cancelled.
func sleepKeepAlive(ctx context.Context, art *tv.ArtClient, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		wait := 10 * time.Second
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
		if time.Until(deadline) > 0 {
			_ = art.KeepAlive()
		}
	}
}

// pipelinePhoto reproduces what the app does on Play/Restart for the FIRST
// photo of the TV's saved playlist: look up owner/playlist from saved state,
// resolve the photo token to a file on disk, read it, then compose a 4K JPEG
// (imaging.Fit, like tv-matte / the default mode). Each step is logged so a
// failure points straight at the broken stage.
func pipelinePhoto(db *sql.DB, store *player.Store, tvID string) ([]byte, string, error) {
	st, err := store.LoadState(tvID)
	if err != nil {
		return nil, "", fmt.Errorf("load saved state: %w (press Play in the app once so a playlist is saved)", err)
	}
	fmt.Printf("Saved play: owner=%q playlist=%q pos=%d\n", st.Owner, st.PlaylistID, st.Position)
	if st.Owner == "" || st.PlaylistID == "" {
		return nil, "", fmt.Errorf("no saved playlist for this TV; press Play in the app first")
	}

	lib := library.NewStore(db)
	photos, err := lib.PlaylistPhotos(st.Owner, st.PlaylistID)
	if err != nil {
		return nil, "", fmt.Errorf("playlist photos: %w", err)
	}
	fmt.Printf("Playlist  : %d photos\n", len(photos))
	if len(photos) == 0 {
		return nil, "", fmt.Errorf("playlist is empty")
	}

	p := photos[0]
	fmt.Printf("Photo[0]  : token=%q name=%q\n", p.Path, p.Name)

	abs, err := lib.ResolvePhotoFile(p.Path)
	if err != nil {
		return nil, "", fmt.Errorf("RESOLVE failed (step the app does first): %w", err)
	}
	fmt.Printf("Resolved  : %s\n", abs)

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", fmt.Errorf("READ failed (file missing/unreadable on disk): %w", err)
	}
	fmt.Printf("Read OK   : %d bytes from disk\n", len(raw))

	img, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", fmt.Errorf("DECODE failed: %w", err)
	}
	composed := imaging.Fit(img, 3840, 2160, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, composed, &jpeg.Options{Quality: 95}); err != nil {
		return nil, "", fmt.Errorf("ENCODE 4K failed: %w", err)
	}
	fmt.Printf("Composed  : 4K JPEG %d bytes (was %d raw)\n", buf.Len(), len(raw))
	return buf.Bytes(), "jpg", nil
}

// loadImage reads the file at path, or synthesizes a gradient JPEG when path is
// empty so the test works with an empty library.
func loadImage(path string) ([]byte, string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read photo %s: %w", path, err)
		}
		ft := "jpg"
		if strings.HasSuffix(strings.ToLower(path), ".png") {
			ft = "png"
		}
		return b, ft, nil
	}

	const w, h = 1920, 1080
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: uint8(128),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, "", fmt.Errorf("encode synthetic jpeg: %w", err)
	}
	return buf.Bytes(), "jpg", nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

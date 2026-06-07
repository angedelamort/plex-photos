package integration

import (
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"plex-photos/auth"
	"plex-photos/library"
	"plex-photos/server"
)

// testEnv bundles a fully wired in-memory test server backed by temp dirs.
type testEnv struct {
	t          *testing.T
	srv        *httptest.Server
	client     *http.Client
	photosRoot string
	dataRoot   string
	store      *library.Store
	scanner    *library.Scanner
}

// newTestEnv builds a mock-auth server (admin) against fresh temp directories,
// seeds a sample photo tree, and returns a logged-in client.
func newTestEnv(t *testing.T, admin bool, mockUser string) *testEnv {
	t.Helper()

	photosRoot := t.TempDir()
	dataRoot := t.TempDir()
	thumbCache := filepath.Join(dataRoot, "cache", "thumbs")
	if err := os.MkdirAll(thumbCache, 0o755); err != nil {
		t.Fatalf("mkdir thumbs: %v", err)
	}
	artDir := filepath.Join(dataRoot, "cache", "art")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir art: %v", err)
	}

	seedPhotos(t, photosRoot)

	db, err := library.OpenDB(dataRoot)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := library.NewStore(db)
	scanner := library.NewScanner(db, store)
	thumbs := library.NewThumbnailer(thumbCache, 200)
	gallery := library.NewHandler(store, scanner, thumbs, artDir)

	sessions := auth.NewSessionManager("integration-test-secret-integration-test", false)
	mw := auth.NewMiddleware(sessions)
	provider := auth.NewMockProvider(mockUser, admin)

	mux := server.NewMux(server.Deps{
		Provider:  provider,
		Sessions:  sessions,
		Mw:        mw,
		Gallery:   gallery,
		StaticDir: t.TempDir(), // empty static dir; API tests don't need assets
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar := newJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}

	env := &testEnv{
		t:          t,
		srv:        srv,
		client:     client,
		photosRoot: photosRoot,
		dataRoot:   dataRoot,
		store:      store,
		scanner:    scanner,
	}
	env.login()
	return env
}

// login performs the mock auth flow so the client holds a session cookie.
func (e *testEnv) login() {
	e.t.Helper()
	resp, err := e.client.Get(e.srv.URL + "/auth/login")
	if err != nil {
		e.t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	// Follow the callback explicitly (mock returns /auth/callback).
	resp2, err := e.client.Get(e.srv.URL + "/auth/callback")
	if err != nil {
		e.t.Fatalf("callback: %v", err)
	}
	resp2.Body.Close()
}

// seedPhotos creates a famille/montreal/{carnaval-2025,ete-2024} + amis tree.
func seedPhotos(t *testing.T, root string) {
	t.Helper()
	layout := map[string][]string{
		filepath.Join(root, "famille", "montreal", "carnaval-2025"): {"IMG_001.jpg", "IMG_002.jpg", "IMG_003.jpg"},
		filepath.Join(root, "famille", "montreal", "ete-2024"):      {"IMG_010.jpg", "IMG_011.jpg"},
		filepath.Join(root, "amis", "vegas-2024", "jour-1"):         {"IMG_100.jpg"},
	}
	for dir, files := range layout {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for _, f := range files {
			writeJPEG(t, filepath.Join(dir, f))
		}
	}
}

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	writeJPEGTo(t, f)
}

// writeJPEGTo encodes a small synthetic JPEG into w.
func writeJPEGTo(t *testing.T, w io.Writer) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 5), 0x80, 0xff})
		}
	}
	if err := jpeg.Encode(w, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
}

package player

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"image/color"

	"github.com/disintegration/imaging"

	"plex-photos/library"
)

// fakeSource is an in-memory photoSource backed by a real image on disk.
type fakeSource struct {
	photos  []library.PlaylistPhoto
	file    string
	err     error
	resolve error
}

func (f *fakeSource) PlaylistPhotos(owner, id string) ([]library.PlaylistPhoto, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.photos, nil
}
func (f *fakeSource) ResolvePhotoFile(path string) (string, error) {
	if f.resolve != nil {
		return "", f.resolve
	}
	return f.file, nil
}

// fakeConn records the calls the swap loop makes to a TV.
type fakeConn struct {
	mu       sync.Mutex
	uploads  int
	selects  int
	filters  int
	lastFilt string
	deleted  []string
	lastSel  string
	dialErr  error
	upErr    error
	contents []string
}

func (c *fakeConn) Upload(data []byte, fileType, matteID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upErr != nil {
		return "", c.upErr
	}
	c.uploads++
	id := fmt.Sprintf("MY_F%04d", c.uploads)
	c.contents = append(c.contents, id)
	return id, nil
}
func (c *fakeConn) SelectImageNoWait(contentID string, show bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selects++
	c.lastSel = contentID
	return nil
}
func (c *fakeConn) SetPhotoFilter(contentID, filterID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.filters++
	c.lastFilt = filterID
	return nil
}
func (c *fakeConn) DeleteImages(ids ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, ids...)
	return nil
}
func (c *fakeConn) KeepAlive() error { return nil }
func (c *fakeConn) Token() string    { return "tok-xyz" }
func (c *fakeConn) Close() error     { return nil }
func (c *fakeConn) uploadCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.uploads }
func (c *fakeConn) selectCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.selects }
func (c *fakeConn) deletedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.deleted...)
}

func writeTestImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "photo.jpg")
	img := imaging.New(640, 480, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	if err := imaging.Save(img, path); err != nil {
		t.Fatalf("save test image: %v", err)
	}
	return path
}

func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func newPlayingManager(t *testing.T) (*Manager, *Store, *fakeConn, string) {
	t.Helper()
	store := newTestStore(t)
	tv, _ := store.Create(TV{Name: "TV", IP: "10.0.0.83", Matte: "none", IntervalS: 3600, DisplayMode: "blur-fill", BgColor: "#000000"})

	src := &fakeSource{
		file: writeTestImage(t),
		photos: []library.PlaylistPhoto{
			{Name: "one", Path: "lib/one.jpg"},
			{Name: "two", Path: "lib/two.jpg"},
			{Name: "three", Path: "lib/three.jpg"},
		},
	}
	conn := &fakeConn{}
	mgr := NewManager(store, src)
	mgr.dial = func(ctx context.Context, ip, token string) (artConn, error) {
		if conn.dialErr != nil {
			return nil, conn.dialErr
		}
		return conn, nil
	}
	return mgr, store, conn, tv.ID
}

func TestManagerPlaySwapStop(t *testing.T) {
	mgr, store, conn, tvID := newPlayingManager(t)

	if err := mgr.Play(tvID, "alice", "pl1"); err != nil {
		t.Fatalf("play: %v", err)
	}
	defer mgr.Shutdown()

	waitFor(t, 3*time.Second, "first upload+select", func() bool {
		return conn.uploadCount() >= 1 && conn.selectCount() >= 1
	})

	snap, _ := mgr.Status(tvID)
	if snap.Status != "playing" {
		t.Fatalf("expected playing, got %q (err=%q)", snap.Status, snap.Error)
	}
	if snap.CurrentName != "one" || snap.NextName != "two" {
		t.Fatalf("unexpected current/next: %q / %q", snap.CurrentName, snap.NextName)
	}
	waitFor(t, 2*time.Second, "step waiting", func() bool {
		s, _ := mgr.Status(tvID)
		return s.Step == "waiting"
	})

	// Persisted state should reflect playing with a content id, and the token
	// captured from the connection should be saved.
	st, _ := store.LoadState(tvID)
	if st.Status != "playing" || st.CurrentContent == "" {
		t.Fatalf("state not persisted: %+v", st)
	}
	tv, _ := store.Get(tvID)
	if tv.Token != "tok-xyz" {
		t.Fatalf("token not persisted, got %q", tv.Token)
	}

	// Skip advances to the next photo and deletes the previous content.
	first := conn.contents[0]
	if err := mgr.Skip(tvID); err != nil {
		t.Fatalf("skip: %v", err)
	}
	waitFor(t, 3*time.Second, "second upload", func() bool { return conn.uploadCount() >= 2 })
	waitFor(t, 2*time.Second, "previous content deleted", func() bool {
		for _, id := range conn.deletedIDs() {
			if id == first {
				return true
			}
		}
		return false
	})

	if err := mgr.Stop(tvID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	st, _ = store.LoadState(tvID)
	if st.Status != "stopped" {
		t.Fatalf("expected stopped after Stop, got %q", st.Status)
	}
	snap, _ = mgr.Status(tvID)
	if snap.Status != "stopped" {
		t.Fatalf("status snapshot not stopped: %q", snap.Status)
	}
}

func TestManagerResumeContinuesFromSavedPosition(t *testing.T) {
	mgr, store, conn, tvID := newPlayingManager(t)
	defer mgr.Shutdown()

	if err := mgr.Play(tvID, "alice", "pl1"); err != nil {
		t.Fatalf("play: %v", err)
	}
	waitFor(t, 3*time.Second, "first select", func() bool { return conn.selectCount() >= 1 })

	// Advance to the second photo, then stop. The saved position should reflect
	// where we were, and Resume must pick up there—not restart at photo one.
	if err := mgr.Skip(tvID); err != nil {
		t.Fatalf("skip: %v", err)
	}
	waitFor(t, 3*time.Second, "advanced to photo two", func() bool {
		s, _ := mgr.Status(tvID)
		return s.CurrentName == "two"
	})
	if err := mgr.Stop(tvID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	st, _ := store.LoadState(tvID)
	if st.Position != 1 {
		t.Fatalf("expected saved position 1 (photo two), got %d", st.Position)
	}

	// Stopped status should advertise resumability for the saved playlist.
	snap, _ := mgr.Status(tvID)
	if !snap.Resumable || snap.ResumePlaylistID != "pl1" || snap.ResumeTotal != 3 {
		t.Fatalf("unexpected resume hints: %+v", snap)
	}

	if err := mgr.Resume(tvID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, 3*time.Second, "resumed on photo two", func() bool {
		s, _ := mgr.Status(tvID)
		return s.Status == "playing" && s.CurrentName == "two"
	})
}

func TestManagerResumeWithoutStateFails(t *testing.T) {
	mgr, _, _, tvID := newPlayingManager(t)
	defer mgr.Shutdown()
	if err := mgr.Resume(tvID); err == nil {
		t.Fatal("expected error resuming a TV that never played")
	}
	if err := mgr.Resume("missing-tv"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing tv, got %v", err)
	}
}

func TestManagerPlayErrorKeepsPlaying(t *testing.T) {
	mgr, _, conn, tvID := newPlayingManager(t)
	conn.dialErr = fmt.Errorf("boom: tv offline")

	if err := mgr.Play(tvID, "alice", "pl1"); err != nil {
		t.Fatalf("play: %v", err)
	}
	defer mgr.Shutdown()

	waitFor(t, 3*time.Second, "error step", func() bool {
		s, _ := mgr.Status(tvID)
		return s.Step == "error" && s.Error != ""
	})
	snap, _ := mgr.Status(tvID)
	if snap.Status != "playing" {
		t.Fatalf("intent should remain playing on error, got %q", snap.Status)
	}
	if err := mgr.Stop(tvID); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestManagerPlayValidatesPlaylist(t *testing.T) {
	store := newTestStore(t)
	tv, _ := store.Create(TV{Name: "TV", IP: "1.1.1.1", Matte: "none", IntervalS: 60, DisplayMode: "blur-fill", BgColor: "#000000"})
	mgr := NewManager(store, &fakeSource{}) // empty playlist
	defer mgr.Shutdown()

	if err := mgr.Play(tv.ID, "alice", "pl1"); err == nil {
		t.Fatal("expected error playing an empty playlist")
	}
	if err := mgr.Play("missing-tv", "alice", "pl1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing tv, got %v", err)
	}
}

package player

import (
	"testing"
	"time"

	"plex-photos/library"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := library.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestStoreTVCRUD(t *testing.T) {
	s := newTestStore(t)

	tv, err := s.Create(TV{Name: "Living room", IP: "10.0.0.83", Matte: "none", IntervalS: 1800, DisplayMode: "blur-fill", BgColor: "#000000"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tv.ID == "" || tv.Name != "Living room" || tv.IntervalS != 1800 || tv.HasToken || tv.DisplayMode != "blur-fill" {
		t.Fatalf("unexpected created tv: %+v", tv)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if _, err := s.Update(tv.ID, TV{Name: "Den", IP: "10.0.0.90", Matte: "modern_polar", IntervalS: 600, DisplayMode: "fit-color", BgColor: "#112233", BorderPct: 8, CaptionFields: []string{"date", "location"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.Get(tv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Den" || got.IP != "10.0.0.90" || got.Matte != "modern_polar" || got.IntervalS != 600 ||
		got.DisplayMode != "fit-color" || got.BgColor != "#112233" || got.BorderPct != 8 {
		t.Fatalf("update not applied: %+v", got)
	}
	if len(got.CaptionFields) != 2 || got.CaptionFields[0] != "date" || got.CaptionFields[1] != "location" {
		t.Fatalf("caption fields not persisted: %+v", got.CaptionFields)
	}

	if err := s.SetToken(tv.ID, "tok-123"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	got, _ = s.Get(tv.ID)
	if got.Token != "tok-123" || !got.HasToken {
		t.Fatalf("token not persisted: %+v", got)
	}

	if err := s.Delete(tv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(tv.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := s.Update("missing", TV{Name: "x", IP: "y"}); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound updating missing tv, got %v", err)
	}
}

func TestStoreState(t *testing.T) {
	s := newTestStore(t)
	tv, _ := s.Create(TV{Name: "TV", IP: "1.1.1.1", Matte: "none", IntervalS: 60, DisplayMode: "tv-matte", BgColor: "#000000"})

	// No saved state yet → stopped zero value.
	st, err := s.LoadState(tv.ID)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if st.Status != "stopped" {
		t.Fatalf("expected stopped, got %q", st.Status)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := s.SaveState(&State{
		TVID: tv.ID, Owner: "alice", PlaylistID: "pl1", Status: "playing",
		Position: 2, CurrentPath: "lib/a.jpg", CurrentContent: "MY_F0001", LastSwapAt: &now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ := s.LoadState(tv.ID)
	if got.Owner != "alice" || got.PlaylistID != "pl1" || got.Status != "playing" ||
		got.Position != 2 || got.CurrentPath != "lib/a.jpg" || got.CurrentContent != "MY_F0001" {
		t.Fatalf("state round-trip mismatch: %+v", got)
	}
	if got.LastSwapAt == nil || !got.LastSwapAt.Equal(now) {
		t.Fatalf("last swap mismatch: %v want %v", got.LastSwapAt, now)
	}

	playing, err := s.PlayingStates()
	if err != nil || len(playing) != 1 || playing[0].TVID != tv.ID {
		t.Fatalf("playing states: %v len=%d", err, len(playing))
	}

	// Flip to stopped → no longer in playing set.
	got.Status = "stopped"
	_ = s.SaveState(got)
	playing, _ = s.PlayingStates()
	if len(playing) != 0 {
		t.Fatalf("expected no playing states, got %d", len(playing))
	}

	// Deleting the TV cascades to its state row.
	_ = s.Delete(tv.ID)
	st2, _ := s.LoadState(tv.ID)
	if st2.Status != "stopped" {
		t.Fatalf("expected stopped after cascade delete, got %q", st2.Status)
	}
}

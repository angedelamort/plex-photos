package player

import (
	"math/rand"
	"sort"
	"testing"

	"plex-photos/library"
)

func deckPhotos(n int) []library.PlaylistPhoto {
	out := make([]library.PlaylistPhoto, n)
	for i := range out {
		out[i] = library.PlaylistPhoto{Path: string(rune('a'+i%26)) + "/" + itoa(i)}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func paths(deck []library.PlaylistPhoto) []string {
	out := make([]string, len(deck))
	for i, p := range deck {
		out[i] = p.Path
	}
	return out
}

func TestBuildDeckSequentialPreservesOrder(t *testing.T) {
	photos := deckPhotos(5)
	deck := buildDeck(photos, false, rand.New(rand.NewSource(1)), "")
	got := paths(deck)
	want := paths(photos)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequential deck reordered at %d: got %v want %v", i, got, want)
		}
	}
}

// A shuffled deck must be a permutation of the playlist: every photo present
// exactly once, so none repeats early and none is starved within a pass.
func TestBuildDeckRandomIsPermutation(t *testing.T) {
	photos := deckPhotos(50)
	deck := buildDeck(photos, true, rand.New(rand.NewSource(42)), "")
	if len(deck) != len(photos) {
		t.Fatalf("deck length = %d, want %d", len(deck), len(photos))
	}
	got := paths(deck)
	want := paths(photos)
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("random deck is not a permutation of the playlist:\n got %v\nwant %v", got, want)
		}
	}
}

func TestBuildDeckAvoidsBackToBackRepeat(t *testing.T) {
	photos := deckPhotos(8)
	// Try many seeds; whenever the shuffle would put avoidFirst at the front,
	// buildDeck must swap it away so it never shows twice in a row.
	for seed := int64(0); seed < 200; seed++ {
		deck := buildDeck(photos, true, rand.New(rand.NewSource(seed)), photos[0].Path)
		if deck[0].Path == photos[0].Path {
			t.Fatalf("seed %d: avoidFirst %q ended up first in deck", seed, photos[0].Path)
		}
	}
}

func TestDeckFromPathsResumesWhenSetMatches(t *testing.T) {
	photos := deckPhotos(6)
	// A shuffled order the player had persisted before a restart.
	saved := []string{
		photos[3].Path, photos[0].Path, photos[5].Path,
		photos[1].Path, photos[4].Path, photos[2].Path,
	}
	deck, ok := deckFromPaths(saved, photos)
	if !ok {
		t.Fatal("expected resume to be valid for an unchanged playlist")
	}
	got := paths(deck)
	for i := range saved {
		if got[i] != saved[i] {
			t.Fatalf("resumed deck order changed at %d: got %v want %v", i, got, saved)
		}
	}
}

func TestDeckFromPathsRejectsChangedPlaylist(t *testing.T) {
	photos := deckPhotos(4)
	saved := paths(photos)

	// Removed/added photo (length mismatch).
	if _, ok := deckFromPaths(saved[:3], photos); ok {
		t.Fatal("expected resume to be rejected when a photo was removed")
	}
	// Same length but a different photo present (set mismatch).
	swapped := append([]string{}, saved...)
	swapped[2] = "z/999"
	if _, ok := deckFromPaths(swapped, photos); ok {
		t.Fatal("expected resume to be rejected when the photo set changed")
	}
}

func TestOrderSigChangesWithModeAndContents(t *testing.T) {
	a := deckPhotos(4)
	b := deckPhotos(5)
	if orderSig(false, a) == orderSig(true, a) {
		t.Fatal("sig should differ between sequential and random for same photos")
	}
	if orderSig(false, a) == orderSig(false, b) {
		t.Fatal("sig should differ when the photo set changes")
	}
	if orderSig(true, a) != orderSig(true, a) {
		t.Fatal("sig should be stable for identical inputs")
	}
}

package library

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairFolderMoves(t *testing.T) {
	child := fingerSig("2024", []fileStamp{{rel: "a.jpg", size: 10, mtime: 20}})
	parent := fingerSig("Vacation", []fileStamp{{rel: "2024/a.jpg", size: 10, mtime: 20}, {rel: "cover.jpg", size: 3, mtime: 4}})
	oldVacation := filepath.Join("root", "Vacation")
	old2024 := filepath.Join(oldVacation, "2024")
	newVacation := filepath.Join("root", "Trips", "Vacation")
	new2024 := filepath.Join(newVacation, "2024")

	got := pairFolderMoves(
		[]folderFinger{{path: oldVacation, sig: parent}, {path: old2024, sig: child}},
		[]folderFinger{{path: newVacation, sig: parent}, {path: new2024, sig: child}},
	)
	if len(got) != 1 {
		t.Fatalf("pairs = %d, want the parent only", len(got))
	}
	if got[0].oldPath != oldVacation || got[0].newPath != newVacation {
		t.Fatalf("pair = %+v", got[0])
	}

	// Two identical folders are not a unique match.
	same := fingerSig("Trip", []fileStamp{{rel: "a.jpg", size: 10, mtime: 20}})
	got = pairFolderMoves(
		[]folderFinger{{path: filepath.Join("A", "Trip"), sig: same}, {path: filepath.Join("B", "Trip"), sig: same}},
		[]folderFinger{{path: filepath.Join("C", "Trip"), sig: same}, {path: filepath.Join("D", "Trip"), sig: same}},
	)
	if len(got) != 0 {
		t.Fatalf("ambiguous pairs = %d, want 0", len(got))
	}

	// A renamed folder (different base name) does not match.
	renamed := fingerSig("Europe", []fileStamp{{rel: "a.jpg", size: 10, mtime: 20}})
	got = pairFolderMoves(
		[]folderFinger{{path: old2024, sig: child}},
		[]folderFinger{{path: filepath.Join(oldVacation, "Europe"), sig: renamed}},
	)
	if len(got) != 0 {
		t.Fatalf("renamed folder pairs = %d, want 0", len(got))
	}

	if fingerSig("Empty", nil) != "" {
		t.Fatal("a folder with no images must not be pairable")
	}
}

func TestRewriteDirBoundary(t *testing.T) {
	old := filepath.Join("root", "Vacation")
	sib := filepath.Join("root", "Vacation2")
	if _, ok := rewriteDir(sib, old, filepath.Join("root", "Trips", "Vacation")); ok {
		t.Fatal("Vacation2 shares a string prefix and must not be rewritten")
	}
	child := filepath.Join(old, "2024")
	got, ok := rewriteDir(child, old, filepath.Join("root", "Trips", "Vacation"))
	if !ok {
		t.Fatal("expected child directory to be rewritten")
	}
	want := filepath.Join("root", "Trips", "Vacation", "2024")
	if got != want {
		t.Fatalf("rewriteDir = %q, want %q", got, want)
	}
	got, ok = rewriteDir(old, old, filepath.Join("root", "Trips", "Vacation"))
	if !ok || got != filepath.Join("root", "Trips", "Vacation") {
		t.Fatalf("rewrite of the folder itself = %q, %v", got, ok)
	}
}

func TestScanRelinksMovedFolder(t *testing.T) {
	root := t.TempDir()
	vacation := filepath.Join(root, "Vacation")
	album := filepath.Join(vacation, "2024")
	cover := filepath.Join(vacation, "cover.jpg")
	photoA := filepath.Join(album, "a.jpg")
	photoB := filepath.Join(album, "b.jpg")
	writePhoto(t, cover)
	writePhoto(t, photoA)
	writePhoto(t, photoB)

	db, store, sc, thumbs, lib := testScanner(t, root)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatalf("initial scan: %v", err)
	}

	parent, err := store.FindNodeByPath(vacation)
	if err != nil {
		t.Fatalf("parent node: %v", err)
	}
	child, err := store.FindNodeByPath(album)
	if err != nil {
		t.Fatalf("child node: %v", err)
	}
	tokenA := AbsToURLPath(photoA)
	tokenB := AbsToURLPath(photoB)
	if err := store.UpdateNodeMetadata(parent.ID, NodeMetadata{SortTitle: "The Trip", Summary: "notes"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNodeMetadata(child.ID, NodeMetadata{SortTitle: "Summer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeCover(parent.ID, tokenB, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeBackground(parent.ID, tokenA); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite("dev", child.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryCover(lib.ID, tokenA, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryBackground(lib.ID, tokenB); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPhotoRating("dev", tokenA, 5); err != nil {
		t.Fatal(err)
	}
	pl, err := store.CreatePlaylist("dev", "Favs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddPlaylistPhotos("dev", pl.ID, []PlaylistPhoto{{Name: "a", Path: tokenA}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE playlists SET cover_photo = ? WHERE id = ?`, tokenA, pl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE photo_meta SET camera = 'KEEP' WHERE library_id = ?`, lib.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO photo_people (photo_path, name) VALUES (?, 'Ada')`, tokenA); err != nil {
		t.Fatal(err)
	}
	if err := store.QuarantineMedia(tokenB, lib.ID, lib.Name, "thumbnails", "broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tvs (id, name, ip) VALUES ('tv1', 'Frame', '127.0.0.1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO tv_player_state (tv_id, owner, status, current_path, deck) VALUES ('tv1', 'dev', 'stopped', ?, ?)`,
		tokenA, tokenA+"\nnot/moved.jpg"); err != nil {
		t.Fatal(err)
	}
	// Warm the quarantine cache so the post-scan invalidate has something to drop.
	if !store.IsQuarantined(tokenB) {
		t.Fatal("expected quarantine before the move")
	}
	sentinel := thumbs.CachePathFor(tokenA)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("sentinel-thumb"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(root, "Trips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(vacation, filepath.Join(root, "Trips", "Vacation")); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatalf("scan after move: %v", err)
	}

	newVacation := filepath.Join(root, "Trips", "Vacation")
	newAlbum := filepath.Join(newVacation, "2024")
	newA := AbsToURLPath(filepath.Join(newAlbum, "a.jpg"))
	newB := AbsToURLPath(filepath.Join(newAlbum, "b.jpg"))
	newCover := AbsToURLPath(filepath.Join(newVacation, "cover.jpg"))

	parent2, err := store.FindNodeByPath(newVacation)
	if err != nil {
		t.Fatalf("relinked parent: %v", err)
	}
	if parent2.ID != parent.ID {
		t.Fatalf("parent id = %s, want %s", parent2.ID, parent.ID)
	}
	if parent2.SortTitle != "The Trip" || parent2.Summary != "notes" {
		t.Fatalf("parent metadata = %q / %q", parent2.SortTitle, parent2.Summary)
	}
	if parent2.CoverPhoto != newB {
		t.Fatalf("parent cover = %q, want %q", parent2.CoverPhoto, newB)
	}
	if parent2.BackgroundPhoto != newA {
		t.Fatalf("parent background = %q, want %q", parent2.BackgroundPhoto, newA)
	}
	if parent2.Depth != 1 {
		t.Fatalf("parent depth = %d, want 1", parent2.Depth)
	}
	var setBy string
	if err := db.QueryRow(`SELECT COALESCE(cover_set_by, '') FROM nodes WHERE id = ?`, parent.ID).Scan(&setBy); err != nil {
		t.Fatal(err)
	}
	if setBy != "admin" {
		t.Fatalf("cover_set_by = %q, want admin", setBy)
	}

	child2, err := store.FindNodeByPath(newAlbum)
	if err != nil {
		t.Fatalf("relinked child: %v", err)
	}
	if child2.ID != child.ID || child2.ParentID != parent.ID {
		t.Fatalf("child id/parent = %s/%s, want %s/%s", child2.ID, child2.ParentID, child.ID, parent.ID)
	}
	if child2.SortTitle != "Summer" {
		t.Fatalf("child sort title = %q", child2.SortTitle)
	}
	if child2.Depth != 2 || child2.PhotoCount != 2 {
		t.Fatalf("child depth/count = %d/%d", child2.Depth, child2.PhotoCount)
	}
	favs, err := store.FavoriteNodeIDs("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !favs[child.ID] {
		t.Fatal("favorite was dropped with the old folder row")
	}
	if _, err := store.FindNodeByPath(vacation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old folder still indexed: %v", err)
	}

	for _, token := range []string{newA, newB, newCover} {
		meta, err := store.GetPhotoMeta(token)
		if err != nil {
			t.Fatalf("meta %s: %v", token, err)
		}
		if meta.Camera != "KEEP" {
			t.Fatalf("meta %s camera = %q, want KEEP (exif was re-read)", token, meta.Camera)
		}
	}
	meta, err := store.GetPhotoMeta(newA)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.People) != 1 || meta.People[0] != "Ada" {
		t.Fatalf("people = %v", meta.People)
	}
	for _, old := range []string{tokenA, tokenB, AbsToURLPath(cover)} {
		if _, err := store.GetPhotoMeta(old); !errors.Is(err, ErrNotFound) {
			t.Fatalf("old meta %s still present: %v", old, err)
		}
	}

	rating, err := store.GetPhotoRating("dev", newA)
	if err != nil || rating != 5 {
		t.Fatalf("rating at new path = %d, %v", rating, err)
	}
	if rating, err = store.GetPhotoRating("dev", tokenA); err != nil || rating != 0 {
		t.Fatalf("rating at old path = %d, %v", rating, err)
	}
	photos, err := store.PlaylistPhotos("dev", pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 1 || photos[0].Path != newA {
		t.Fatalf("playlist photos = %+v", photos)
	}
	gotPL, err := store.GetPlaylist("dev", pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPL.CoverPhoto != newA {
		t.Fatalf("playlist cover = %q, want %q", gotPL.CoverPhoto, newA)
	}

	lib, err = store.GetLibrary(lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lib.CoverPhoto != newA || lib.BackgroundPhoto != newB {
		t.Fatalf("library cover/background = %q / %q", lib.CoverPhoto, lib.BackgroundPhoto)
	}
	if store.IsQuarantined(tokenB) {
		t.Fatal("quarantine stayed on the old path")
	}
	if !store.IsQuarantined(newB) {
		t.Fatal("quarantine did not follow the file")
	}

	var cur, deck string
	if err := db.QueryRow(`SELECT COALESCE(current_path, ''), COALESCE(deck, '') FROM tv_player_state WHERE tv_id = 'tv1'`).Scan(&cur, &deck); err != nil {
		t.Fatal(err)
	}
	if cur != newA {
		t.Fatalf("tv current_path = %q, want %q", cur, newA)
	}
	if deck != newA+"\nnot/moved.jpg" {
		t.Fatalf("tv deck = %q", deck)
	}

	body, err := os.ReadFile(thumbs.CachePathFor(newA))
	if err != nil {
		t.Fatalf("thumbnail at new path: %v", err)
	}
	if string(body) != "sentinel-thumb" {
		t.Fatalf("thumbnail was regenerated: %q", body)
	}
	if _, err := os.Stat(thumbs.CachePathFor(tokenA)); !os.IsNotExist(err) {
		t.Fatalf("old thumbnail still present: %v", err)
	}
}

func TestScanRelinkUnicodeFolder(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Café")
	photo := filepath.Join(album, "a.jpg")
	writePhoto(t, photo)

	_, store, sc, _, lib := testScanner(t, root)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE photo_meta SET camera = 'KEEP' WHERE library_id = ?`, lib.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Trips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(album, filepath.Join(root, "Trips", "Café")); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetPhotoMeta(AbsToURLPath(filepath.Join(root, "Trips", "Café", "a.jpg")))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Camera != "KEEP" {
		t.Fatalf("camera = %q, want KEEP", meta.Camera)
	}
}

func TestScanDoesNotRelinkRenamedOrEditedFolder(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Album")
	photo := filepath.Join(album, "a.jpg")
	writePhoto(t, photo)

	_, store, sc, _, lib := testScanner(t, root)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	node, err := store.FindNodeByPath(album)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNodeMetadata(node.ID, NodeMetadata{SortTitle: "Keep"}); err != nil {
		t.Fatal(err)
	}

	renamed := filepath.Join(root, "Europe")
	if err := os.Rename(album, renamed); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindNodeByPath(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == node.ID || got.SortTitle != "" {
		t.Fatalf("renamed folder kept id/sort %s/%q", got.ID, got.SortTitle)
	}

	// Move a folder whose file was edited. Size/mtime no longer match, so the
	// old row is not reused.
	edited := filepath.Join(root, "Edited")
	editedPhoto := filepath.Join(edited, "a.jpg")
	writePhoto(t, editedPhoto)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	editedNode, err := store.FindNodeByPath(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNodeMetadata(editedNode.ID, NodeMetadata{SortTitle: "Keep"}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(editedPhoto, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Other"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "Other", "Edited")
	if err := os.Rename(edited, moved); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	got, err = store.FindNodeByPath(moved)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == editedNode.ID || got.SortTitle != "" {
		t.Fatalf("edited folder was relinked: id %s sort %q", got.ID, got.SortTitle)
	}
}

func TestScanDoesNotRelinkIdenticalFolders(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A", "Trip", "a.jpg")
	b := filepath.Join(root, "B", "Trip", "a.jpg")
	writePhoto(t, a)
	writePhoto(t, b)
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, p := range []string{a, b} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	db, store, sc, _, lib := testScanner(t, root)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	nodeA, err := store.FindNodeByPath(filepath.Dir(a))
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := store.FindNodeByPath(filepath.Dir(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNodeMetadata(nodeA.ID, NodeMetadata{SortTitle: "from-A"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNodeMetadata(nodeB.ID, NodeMetadata{SortTitle: "from-B"}); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "C"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "D"), 0o755); err != nil {
		t.Fatal(err)
	}
	destC := filepath.Join(root, "C", "Trip")
	destD := filepath.Join(root, "D", "Trip")
	if err := os.Rename(filepath.Dir(a), destC); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(b), destD); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{nodeA.ID, nodeB.ID} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("ambiguous folder %s was kept", id)
		}
	}
	for _, p := range []string{destC, destD} {
		n, err := store.FindNodeByPath(p)
		if err != nil {
			t.Fatal(err)
		}
		if n.SortTitle != "" {
			t.Fatalf("sort title %q followed an ambiguous move to %s", n.SortTitle, p)
		}
	}
}

func TestScanDoesNotRelinkCopyAlreadyIndexed(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "A", "Trip", "a.jpg")
	writePhoto(t, src)

	db, store, sc, _, lib := testScanner(t, root)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	orig, err := store.FindNodeByPath(filepath.Dir(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE photo_meta SET camera = 'KEEP' WHERE library_id = ?`, lib.ID); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(root, "B", "Trip", "a.jpg")
	copyPhoto(t, src, dst)
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}
	copied, err := store.FindNodeByPath(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(src)); err != nil {
		t.Fatal(err)
	}
	if err := sc.Scan(lib, "test"); err != nil {
		t.Fatal(err)
	}

	got, err := store.FindNodeByPath(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != copied.ID {
		t.Fatalf("copy id = %s, want %s (original was %s)", got.ID, copied.ID, orig.ID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, orig.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("original folder row was relinked onto the copy")
	}
	meta, err := store.GetPhotoMeta(AbsToURLPath(dst))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Camera == "KEEP" {
		t.Fatal("copy picked up metadata indexed for the original")
	}
}

func testScanner(t *testing.T, root string) (*sql.DB, *Store, *Scanner, *Thumbnailer, *Library) {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	lib, err := store.CreateLibrary("Photos", root, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc := NewScanner(db, store)
	thumbs := NewThumbnailer(filepath.Join(t.TempDir(), "thumbs"), 160)
	sc.SetThumbnailer(thumbs)
	return db, store, sc, thumbs, lib
}

func writePhoto(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, validJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyPhoto(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

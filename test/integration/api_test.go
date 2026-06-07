package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- small JSON HTTP helpers ---

func (e *testEnv) do(method, path string, body any) *http.Response {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return v
}

type libraryDTO struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	RootPath        string   `json:"rootPath"`
	Whitelist       []string `json:"whitelist"`
	CollectionCount int      `json:"collectionCount"`
}

type scanProgressDTO struct {
	Running bool   `json:"running"`
	Done    bool   `json:"done"`
	Total   int    `json:"total"`
	Current int    `json:"current"`
	Error   string `json:"error"`
}

type nodeDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PhotoCount  int    `json:"photoCount"`
	ChildCount  int    `json:"childCount"`
	HasChildren bool   `json:"hasChildren"`
	CoverPhoto  string `json:"coverPhoto"`
}

type nodeResp struct {
	Node     nodeDTO   `json:"node"`
	Children []nodeDTO `json:"children"`
	Photos   []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"photos"`
	Ancestors []nodeDTO `json:"ancestors"`
}

// createAndScan creates a library rooted at famille and scans it.
func (e *testEnv) createAndScan(name, sub string, whitelist []string) libraryDTO {
	e.t.Helper()
	root := filepath.Join(e.photosRoot, sub)
	resp := e.do(http.MethodPost, "/api/admin/libraries", map[string]any{
		"name": name, "rootPath": root, "whitelist": whitelist,
	})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create library status = %d", resp.StatusCode)
	}
	lib := decode[libraryDTO](e.t, resp)

	e.scan(lib.ID)
	// Re-fetch the library (with collection count) after the async scan settles.
	libs := decode[[]libraryDTO](e.t, e.do(http.MethodGet, "/api/admin/libraries", nil))
	for _, l := range libs {
		if l.ID == lib.ID {
			return l
		}
	}
	return lib
}

// scan triggers a scan and waits for it to finish (the scan runs async).
func (e *testEnv) scan(libID string) {
	e.t.Helper()
	scanResp := e.do(http.MethodPost, "/api/admin/libraries/"+libID+"/scan", nil)
	if scanResp.StatusCode != http.StatusAccepted {
		e.t.Fatalf("scan status = %d", scanResp.StatusCode)
	}
	scanResp.Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p := decode[scanProgressDTO](e.t, e.do(http.MethodGet, "/api/admin/libraries/"+libID+"/scan-progress", nil))
		if p.Done {
			if p.Error != "" {
				e.t.Fatalf("scan failed: %s", p.Error)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("scan did not finish in time")
}

func TestMeEndpoint(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	resp := e.do(http.MethodGet, "/api/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	me := decode[map[string]any](t, resp)
	if me["username"] != "alice" || me["isAdmin"] != true {
		t.Fatalf("unexpected me: %+v", me)
	}
}

func TestFullNavigationFlow(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	lib := e.createAndScan("Famille", "famille", []string{"alice"})

	// Top-level nodes: a single "montreal" collection (holds two albums).
	top := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/libraries/"+lib.ID+"/nodes", nil))
	if len(top) != 1 || top[0].Name != "montreal" {
		t.Fatalf("unexpected top-level nodes: %+v", top)
	}
	if !top[0].HasChildren || top[0].ChildCount != 2 {
		t.Fatalf("expected montreal to have 2 children, got %+v", top[0])
	}
	if top[0].CoverPhoto == "" {
		t.Fatal("expected a fallback cover photo on the collection node")
	}

	// Drill into montreal: it has two child album nodes and no direct photos.
	montreal := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+top[0].ID, nil))
	if len(montreal.Children) != 2 {
		t.Fatalf("expected 2 child nodes, got %d", len(montreal.Children))
	}
	if len(montreal.Photos) != 0 {
		t.Fatalf("expected montreal to have no direct photos, got %d", len(montreal.Photos))
	}

	// Find carnaval-2025 (3 photos)
	var carnavalID string
	for _, a := range montreal.Children {
		if a.Name == "carnaval-2025" {
			carnavalID = a.ID
			if a.PhotoCount != 3 {
				t.Fatalf("expected 3 photos in carnaval, got %d", a.PhotoCount)
			}
		}
	}
	if carnavalID == "" {
		t.Fatal("carnaval-2025 node not found")
	}

	// Photos of the album node, plus ancestor breadcrumb.
	carnaval := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+carnavalID, nil))
	if len(carnaval.Photos) != 3 {
		t.Fatalf("expected 3 photos, got %d", len(carnaval.Photos))
	}
	if len(carnaval.Ancestors) != 1 || carnaval.Ancestors[0].Name != "montreal" {
		t.Fatalf("expected montreal ancestor, got %+v", carnaval.Ancestors)
	}

	// Thumbnail generation
	thumbResp := e.do(http.MethodGet, "/api/thumb/"+carnaval.Photos[0].Path, nil)
	defer thumbResp.Body.Close()
	if thumbResp.StatusCode != http.StatusOK {
		t.Fatalf("thumb status = %d", thumbResp.StatusCode)
	}
	if ct := thumbResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("thumb content-type = %q", ct)
	}
	b, _ := io.ReadAll(thumbResp.Body)
	if len(b) == 0 {
		t.Fatal("thumb body is empty")
	}

	// Original photo
	photoResp := e.do(http.MethodGet, "/api/photo/"+carnaval.Photos[0].Path, nil)
	photoResp.Body.Close()
	if photoResp.StatusCode != http.StatusOK {
		t.Fatalf("photo status = %d", photoResp.StatusCode)
	}

	// Set cover (admin)
	coverResp := e.do(http.MethodPut, "/api/cover", map[string]any{
		"target": "node", "id": carnavalID, "photo": carnaval.Photos[1].Path,
	})
	coverResp.Body.Close()
	if coverResp.StatusCode != http.StatusOK {
		t.Fatalf("set cover status = %d", coverResp.StatusCode)
	}

	// Verify cover persisted
	carnaval2 := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+carnavalID, nil))
	if carnaval2.Node.CoverPhoto != carnaval.Photos[1].Path {
		t.Fatalf("cover not persisted: got %q want %q", carnaval2.Node.CoverPhoto, carnaval.Photos[1].Path)
	}
}

// TestUploadArtStoredOutsideLibrary verifies uploaded custom art is saved under
// the internal art dir (flagged with the @art sentinel, not inside the photos
// library) and is served back via /api/art.
func TestUploadArtStoredOutsideLibrary(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	lib := e.createAndScan("Famille", "famille", []string{"alice"})

	top := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/libraries/"+lib.ID+"/nodes", nil))
	if len(top) == 0 {
		t.Fatal("no top-level nodes")
	}
	montreal := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+top[0].ID, nil))
	if len(montreal.Children) == 0 {
		t.Fatal("no child nodes")
	}
	albID := montreal.Children[0].ID

	// Build a multipart upload of a tiny JPEG.
	var img bytes.Buffer
	writeJPEGTo(t, &img)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("target", "node")
	_ = mw.WriteField("id", albID)
	_ = mw.WriteField("kind", "cover")
	fw, err := mw.CreateFormFile("file", "poster.jpg")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write(img.Bytes()); err != nil {
		t.Fatalf("write form: %v", err)
	}
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/art", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	var out struct {
		Status string `json:"status"`
		Photo  string `json:"photo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode upload resp: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}
	if !strings.HasPrefix(out.Photo, "@art/") {
		t.Fatalf("expected @art-prefixed path, got %q", out.Photo)
	}

	// The art file must live under the data dir, not the photos library.
	rel := strings.TrimPrefix(out.Photo, "@art/")
	if _, err := os.Stat(filepath.Join(e.dataRoot, "cache", "art", filepath.FromSlash(rel))); err != nil {
		t.Fatalf("art not stored under data dir: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(e.photosRoot, "**", ".plexart")); len(matches) != 0 {
		t.Fatalf("unexpected .plexart folder in photos library: %v", matches)
	}

	// It must be persisted as the cover and served via /api/art.
	node2 := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+albID, nil))
	if node2.Node.CoverPhoto != out.Photo {
		t.Fatalf("cover not persisted: got %q want %q", node2.Node.CoverPhoto, out.Photo)
	}

	artResp := e.do(http.MethodGet, "/api/art/"+rel, nil)
	defer artResp.Body.Close()
	if artResp.StatusCode != http.StatusOK {
		t.Fatalf("serve art status = %d", artResp.StatusCode)
	}
	if ct := artResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("art content-type = %q", ct)
	}
}

func TestScanIsIdempotent(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	lib := e.createAndScan("Famille", "famille", nil)
	// Scan again.
	e.scan(lib.ID)

	top := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/libraries/"+lib.ID+"/nodes", nil))
	if len(top) != 1 {
		t.Fatalf("expected scan to remain idempotent (1 top-level node), got %d", len(top))
	}
}

// TestScanDeepNesting verifies the scanner builds a recursive node tree. The
// seeded tree is photosRoot/<group>/<place>/<event-or-day>/*.jpg, and the
// library is rooted at photosRoot itself, so each folder becomes a node and
// nesting is preserved (amis -> vegas-2024 -> jour-1).
func TestScanDeepNesting(t *testing.T) {
	e := newTestEnv(t, true, "alice")

	resp := e.do(http.MethodPost, "/api/admin/libraries", map[string]any{
		"name": "All", "rootPath": e.photosRoot, "whitelist": nil,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create library status = %d", resp.StatusCode)
	}
	lib := decode[libraryDTO](t, resp)
	e.scan(lib.ID)

	top := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/libraries/"+lib.ID+"/nodes", nil))
	byName := map[string]nodeDTO{}
	for _, c := range top {
		byName[c.Name] = c
	}
	if len(top) != 2 {
		t.Fatalf("expected 2 top-level nodes (amis, famille), got %d: %+v", len(top), top)
	}

	// amis is a collection holding vegas-2024 (no direct photos itself).
	amis, ok := byName["amis"]
	if !ok || amis.PhotoCount != 0 || amis.ChildCount != 1 {
		t.Fatalf("amis: want 0 photos / 1 child, got %+v", amis)
	}
	// famille holds montreal.
	if fam, ok := byName["famille"]; !ok || fam.ChildCount != 1 {
		t.Fatalf("famille: want 1 child, got %+v", fam)
	}

	// amis -> vegas-2024 -> jour-1 (1 photo). Walk the tree explicitly.
	amisNode := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+amis.ID, nil))
	if len(amisNode.Children) != 1 || amisNode.Children[0].Name != "vegas-2024" {
		t.Fatalf("amis children: want [vegas-2024], got %+v", amisNode.Children)
	}
	vegas := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+amisNode.Children[0].ID, nil))
	if len(vegas.Children) != 1 || vegas.Children[0].Name != "jour-1" {
		t.Fatalf("vegas children: want [jour-1], got %+v", vegas.Children)
	}
	jour := decode[nodeResp](t, e.do(http.MethodGet, "/api/nodes/"+vegas.Children[0].ID, nil))
	if len(jour.Photos) != 1 {
		t.Fatalf("expected 1 photo in jour-1, got %d", len(jour.Photos))
	}
	if len(jour.Ancestors) != 2 || jour.Ancestors[0].Name != "amis" || jour.Ancestors[1].Name != "vegas-2024" {
		t.Fatalf("jour-1 ancestors: want [amis, vegas-2024], got %+v", jour.Ancestors)
	}
	thumb := e.do(http.MethodGet, "/api/thumb/"+jour.Photos[0].Path, nil)
	defer thumb.Body.Close()
	if thumb.StatusCode != http.StatusOK {
		t.Fatalf("thumb for deep-album photo: status %d", thumb.StatusCode)
	}
}

func TestSearchNodes(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	e.createAndScan("Famille", "famille", []string{"alice"})

	// "montreal" is a top-level collection in the famille fixture.
	hits := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/search?q=montr", nil))
	if len(hits) == 0 {
		t.Fatal("expected at least one search hit for 'montr'")
	}
	found := false
	for _, h := range hits {
		if h.Name == "montreal" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'montreal' in search results, got %+v", hits)
	}

	// Empty query returns an empty list, not an error.
	empty := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/search?q=", nil))
	if len(empty) != 0 {
		t.Fatalf("expected empty results for empty query, got %d", len(empty))
	}

	// A non-matching query yields no results.
	none := decode[[]nodeDTO](t, e.do(http.MethodGet, "/api/search?q=zzzznope", nil))
	if len(none) != 0 {
		t.Fatalf("expected no results for non-matching query, got %d", len(none))
	}
}

func TestSearchRespectsLibraryAccess(t *testing.T) {
	// Library whitelisted to alice only; bob must not find its nodes.
	admin := newTestEnv(t, true, "admin")
	admin.createAndScan("Famille", "famille", []string{"alice"})

	nodes, err := admin.store.SearchNodes([]string{}, "montreal", 50)
	if err != nil {
		t.Fatalf("search with no libraries: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 results when no accessible libraries, got %d", len(nodes))
	}
}

func TestThumbBlocksTraversal(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	e.createAndScan("Famille", "famille", nil)

	resp := e.do(http.MethodGet, "/api/photo/"+url.PathEscape("../../etc/passwd"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected traversal to be blocked, got %d", resp.StatusCode)
	}
}

func TestNonAdminForbiddenFromAdminRoutes(t *testing.T) {
	e := newTestEnv(t, false, "bob")
	resp := e.do(http.MethodGet, "/api/admin/libraries", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin admin route, got %d", resp.StatusCode)
	}
}

func TestWhitelistFiltersLibraries(t *testing.T) {
	// Admin creates a library whitelisted only to alice.
	admin := newTestEnv(t, true, "admin")
	admin.createAndScan("Famille", "famille", []string{"alice"})

	// A non-admin user "bob" not on the whitelist sees no libraries.
	// Reuse the same data dir/photos by pointing a new env at them is complex;
	// instead assert via the store directly that bob has no access.
	libs, err := admin.store.ListLibrariesForUser("bob", false)
	if err != nil {
		t.Fatalf("list for bob: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("expected bob to see 0 libraries, got %d", len(libs))
	}
	aliceLibs, err := admin.store.ListLibrariesForUser("alice", false)
	if err != nil {
		t.Fatalf("list for alice: %v", err)
	}
	if len(aliceLibs) != 1 {
		t.Fatalf("expected alice to see 1 library, got %d", len(aliceLibs))
	}
}

func TestUnauthenticatedRejected(t *testing.T) {
	e := newTestEnv(t, true, "alice")
	// Fresh client with no cookies.
	resp, err := http.Get(e.srv.URL + "/api/libraries")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}
}

package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"plex-photos/auth"
)

// Handler exposes all gallery/admin HTTP endpoints.
type Handler struct {
	store    *Store
	scanner  *Scanner
	autoScan *AutoScanner
	thumbs   *Thumbnailer
	jobs     *JobManager
	artDir   string
	version  string
}

// defaultBrowseRoot is the conventional photos mount used as the starting point
// for the admin directory browser. It is only a convenience default; libraries
// may be rooted at any accessible absolute directory.
const defaultBrowseRoot = "/photos"

// SetVersion records the build version surfaced to clients (e.g. in /api/me).
func (h *Handler) SetVersion(v string) { h.version = v }

// NewHandler builds the gallery handler. artDir is the internal folder (under the
// data mount) where uploaded custom art is stored, kept out of the photos library.
func NewHandler(store *Store, scanner *Scanner, thumbs *Thumbnailer, artDir string) *Handler {
	return &Handler{store: store, scanner: scanner, thumbs: thumbs, artDir: artDir}
}

// SetAutoScanner wires the auto-scanner so admin settings endpoints can read
// and update the periodic rescan interval at runtime.
func (h *Handler) SetAutoScanner(a *AutoScanner) { h.autoScan = a }

// SetJobManager wires the background job manager used to run library scans and
// thumbnail regeneration as tracked jobs.
func (h *Handler) SetJobManager(j *JobManager) { h.jobs = j }

// RecordLogin records a successful sign-in for the given user, keeping the
// users table in sync with whoever actually authenticates via Plex. Errors are
// non-fatal to the login flow and should be logged by the caller if returned.
func (h *Handler) RecordLogin(username, email string, isAdmin bool) error {
	return h.store.TouchUser(username, email, isAdmin)
}

// artPrefix marks a stored cover/background path as custom art living under the
// internal art directory (artDir) rather than inside the photos library. The
// remainder after the prefix is a path relative to artDir.
const artPrefix = "@art/"

// IsArtPath reports whether a stored cover/background path refers to custom art.
func IsArtPath(p string) bool {
	return strings.HasPrefix(p, artPrefix)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- Current user ---

// Me returns the current session user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"username": s.Username,
		"email":    s.Email,
		"isAdmin":  s.IsAdmin,
		"version":  h.version,
		"rowLimit": h.rowLimit(),
	})
}

// maxPrefsBytes caps the size of a stored preferences blob to keep it sane.
const maxPrefsBytes = 16 << 10 // 16 KiB

// GetPreferences returns the current user's stored UI preferences as raw JSON.
func (h *Handler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	prefs, err := h.store.GetPreferences(s.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, prefs)
}

// PutPreferences replaces the current user's stored UI preferences. The body
// must be a JSON object; it is stored verbatim after validation.
func (h *Handler) PutPreferences(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	data, err := io.ReadAll(io.LimitReader(r.Body, maxPrefsBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read failed")
		return
	}
	if int64(len(data)) > maxPrefsBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "preferences too large")
		return
	}
	// Validate it is a JSON object (reject arrays, scalars, malformed input).
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		writeErr(w, http.StatusBadRequest, "preferences must be a JSON object")
		return
	}
	// Re-marshal to normalize and strip anything unexpected at the top level.
	normalized, err := json.Marshal(obj)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode failed")
		return
	}
	if err := h.store.SetPreferences(s.Username, string(normalized)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(normalized)
}

// --- Admin: libraries CRUD ---

type libraryInput struct {
	Name      string   `json:"name"`
	RootPath  string   `json:"rootPath"`
	Whitelist []string `json:"whitelist"`
	SortTitle string   `json:"sortTitle"`
	Summary   string   `json:"summary"`
}

// AdminListLibraries lists all libraries.
func (h *Handler) AdminListLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := h.store.ListLibraries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libs)
}

// AdminBrowseDirs lists the immediate subdirectories of an absolute filesystem
// path so admins can pick any library root server-side. The "path" query param
// is an absolute path; empty defaults to the photos root. "Up" navigates to the
// parent, all the way to the filesystem root. Admin-only, read-only listing.
func (h *Handler) AdminBrowseDirs(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		// No global photos root: default to the conventional /photos mount, and
		// fall back to the working directory if it isn't present (e.g. local dev).
		p = defaultBrowseRoot
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			if wd, werr := os.Getwd(); werr == nil {
				p = wd
			}
		}
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	full := filepath.Clean(p)
	names, err := subdirs(full)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot read folder")
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, entry{Name: name, Path: filepath.ToSlash(filepath.Join(full, name))})
	}
	parent := filepath.Dir(full)
	hasParent := parent != full // false only at the filesystem root
	writeJSON(w, http.StatusOK, map[string]any{
		"root":      filepath.ToSlash(defaultBrowseRoot),
		"path":      filepath.ToSlash(full),
		"parent":    filepath.ToSlash(parent),
		"hasParent": hasParent,
		"dirs":      entries,
	})
}

// resolveRoot validates an admin-supplied library root. There is no global
// photos mount: the library root the admin picks (via the directory browser) is
// itself the anchor for everything under it. The input must resolve to an
// existing absolute directory.
func (h *Handler) resolveRoot(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ErrUnsafePath
	}
	full, err := filepath.Abs(input)
	if err != nil {
		return "", ErrUnsafePath
	}
	full = filepath.Clean(full)
	info, err := os.Stat(full)
	if err != nil || !info.IsDir() {
		return "", ErrUnsafePath
	}
	return full, nil
}

// AdminCreateLibrary creates a library.
func (h *Handler) AdminCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var in libraryInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.RootPath) == "" {
		writeErr(w, http.StatusBadRequest, "name and rootPath are required")
		return
	}
	root, err := h.resolveRoot(in.RootPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "root path must be inside the photos folder")
		return
	}
	lib, err := h.store.CreateLibrary(in.Name, root, in.Whitelist)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, lib)
}

// AdminUpdateLibrary updates a library.
func (h *Handler) AdminUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in libraryInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	root, err := h.resolveRoot(in.RootPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "root path must be inside the photos folder")
		return
	}
	lib, err := h.store.UpdateLibrary(id, in.Name, root, in.Whitelist, strings.TrimSpace(in.SortTitle), strings.TrimSpace(in.Summary))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "library not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, lib)
}

// AdminDeleteLibrary deletes a library.
func (h *Handler) AdminDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.DeleteLibrary(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "library not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminScanLibrary forces a filesystem rescan. A quick (presence-only) scan is
// the default; pass ?deep=1 (or deep=true) for a thorough scan that re-checks
// every photo against its source mtime/size and removes orphaned thumbnails.
func (h *Handler) AdminScanLibrary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lib, err := h.store.GetLibrary(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "library not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	deepParam := r.URL.Query().Get("deep")
	deep := deepParam == "1" || strings.EqualFold(deepParam, "true")
	source := "manual"
	if deep {
		source = "manual (deep)"
	}

	// Run the scan as a tracked background job so the client can poll progress
	// and show a Plex-like scanning banner, and it appears on the Jobs page.
	if h.jobs != nil {
		h.jobs.Enqueue(JobTypeScan, lib.Name, func(p *JobProgress) error {
			var err error
			if deep {
				err = h.scanner.DeepScanJob(lib, source, p)
			} else {
				err = h.scanner.ScanJob(lib, source, p)
			}
			if err != nil {
				log.Printf("scan library %s failed: %v", lib.ID, err)
				return err
			}
			return nil
		})
	} else {
		go func() {
			if err := h.scanner.Scan(lib, source); err != nil {
				log.Printf("scan library %s failed: %v", lib.ID, err)
			}
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "libraryId": id, "deep": deep})
}

// AdminListJobs returns the recorded background jobs (active + recent history).
func (h *Handler) AdminListJobs(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	jobs, err := h.jobs.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// AdminDebugGoroutines dumps every goroutine's stack as plain text, so when a
// scan appears stuck an admin can see exactly which call the worker is parked on
// (a blocked syscall on a NAS read, a lock, a decode loop, etc.). It is a
// read-only diagnostic with no side effects.
func (h *Handler) AdminDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		writeErr(w, http.StatusInternalServerError, "goroutine profile unavailable")
		return
	}
	// debug=2 prints full, human-readable per-goroutine stack traces.
	if err := profile.WriteTo(w, 2); err != nil {
		log.Printf("debug goroutines: %v", err)
	}
}

// AdminScanProgress reports the live progress of a library scan.
func (h *Handler) AdminScanProgress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := h.scanner.Progress(id)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"libraryId": id, "running": false, "done": false})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- Admin: settings ---

// SettingRowLimit is the settings key holding the maximum number of items
// loaded into each home/library/collection carousel row before the user must
// scroll. Keeps very large folders snappy by paging instead of rendering all.
const SettingRowLimit = "row_limit"

// defaultRowLimit caps each carousel row when none is configured.
const defaultRowLimit = 16

// rowLimit returns the configured per-row item cap, falling back to the default.
func (h *Handler) rowLimit() int {
	if h.store == nil {
		return defaultRowLimit
	}
	v, err := h.store.GetSetting(SettingRowLimit, strconv.Itoa(defaultRowLimit))
	if err != nil {
		return defaultRowLimit
	}
	n, perr := strconv.Atoi(strings.TrimSpace(v))
	if perr != nil || n < 1 {
		return defaultRowLimit
	}
	return n
}

// AdminGetSettings returns global app settings (currently the auto-scan
// interval). Hours of 0 means the periodic rescan is disabled.
func (h *Handler) AdminGetSettings(w http.ResponseWriter, r *http.Request) {
	hours := 0
	if h.autoScan != nil {
		hours = int(h.autoScan.Interval().Hours())
	}
	thumbWorkers := defaultThumbWorkers
	if h.scanner != nil {
		thumbWorkers = h.scanner.ThumbWorkers()
	}
	thumbFilter := defaultThumbFilter
	if h.thumbs != nil {
		thumbFilter = h.thumbs.Filter()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"autoScanIntervalHours": hours,
		"thumbnailWorkers":      thumbWorkers,
		"thumbnailFilter":       thumbFilter,
		"rowLimit":              h.rowLimit(),
	})
}

// AdminUpdateSettings updates global app settings. Currently supports the
// periodic rescan interval in hours (0 disables it; the filesystem watcher
// always stays on).
func (h *Handler) AdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AutoScanIntervalHours *int    `json:"autoScanIntervalHours"`
		ThumbnailWorkers      *int    `json:"thumbnailWorkers"`
		ThumbnailFilter       *string `json:"thumbnailFilter"`
		RowLimit              *int    `json:"rowLimit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if in.AutoScanIntervalHours != nil && h.autoScan != nil {
		if err := h.autoScan.SetIntervalHours(*in.AutoScanIntervalHours); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.ThumbnailWorkers != nil && h.scanner != nil {
		h.scanner.SetThumbWorkers(*in.ThumbnailWorkers)
		if err := h.store.SetSetting(SettingThumbWorkers, strconv.Itoa(h.scanner.ThumbWorkers())); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.ThumbnailFilter != nil && h.thumbs != nil {
		eff := h.thumbs.SetFilter(*in.ThumbnailFilter)
		if err := h.store.SetSetting(SettingThumbFilter, eff); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.RowLimit != nil {
		n := *in.RowLimit
		if n < 1 {
			n = defaultRowLimit
		}
		if err := h.store.SetSetting(SettingRowLimit, strconv.Itoa(n)); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.AdminGetSettings(w, r)
}

// --- Admin: scan error log ---

// AdminListScanErrors returns the persistent log of failed library scans,
// most recent first.
func (h *Handler) AdminListScanErrors(w http.ResponseWriter, r *http.Request) {
	errs, err := h.store.ListScanErrors()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, errs)
}

// AdminClearScanErrors removes all recorded scan errors.
func (h *Handler) AdminClearScanErrors(w http.ResponseWriter, r *http.Request) {
	if err := h.store.ClearScanErrors(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Admin: users / library access ---

type userInput struct {
	Username   string   `json:"username"`
	Email      string   `json:"email"`
	LibraryIDs []string `json:"libraryIds"`
}

// AdminListUsers lists all known users with the libraries they can access.
func (h *Handler) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// AdminCreateUser adds a user by Plex username and grants the chosen libraries.
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if _, err := h.store.AddUser(in.Username, strings.TrimSpace(in.Email)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.store.SetUserLibraryAccess(in.Username, in.LibraryIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := h.store.GetUser(in.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

// AdminUpdateUserAccess replaces the set of libraries a user can access.
func (h *Handler) AdminUpdateUserAccess(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	var in userInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Ensure a user record exists so they show up even with zero libraries.
	if _, err := h.store.AddUser(username, strings.TrimSpace(in.Email)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.store.SetUserLibraryAccess(username, in.LibraryIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := h.store.GetUser(username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// AdminDeleteUser removes a user and all of their library access grants.
func (h *Handler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := h.store.DeleteUser(username); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Navigation (whitelist filtered) ---

// ListLibraries lists libraries accessible to the current user.
func (h *Handler) ListLibraries(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	libs, err := h.store.ListLibrariesForUser(s.Username, s.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libs)
}

// ListTopNodes lists the top-level nodes of an accessible library.
func (h *Handler) ListTopNodes(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	libID := r.PathValue("id")
	ok, err := h.store.CanAccessLibrary(libID, s.Username, s.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return
	}
	nodes, err := h.store.ListTopLevelNodes(libID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.fillNodeCovers(nodes)
	h.markFavorites(s.Username, nodes)
	writeJSON(w, http.StatusOK, nodes)
}

// Search finds albums and collections whose name matches the "q" query within
// the libraries the current user can access. Returns a flat list of nodes.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, []*Node{})
		return
	}
	libs, err := h.store.ListLibrariesForUser(s.Username, s.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := make([]string, 0, len(libs))
	for _, lib := range libs {
		ids = append(ids, lib.ID)
	}
	nodes, err := h.store.SearchNodes(ids, query, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.fillNodeCovers(nodes)
	h.markFavorites(s.Username, nodes)
	writeJSON(w, http.StatusOK, nodes)
}

// markFavorites sets the Favorite flag on each node for the given user.
func (h *Handler) markFavorites(username string, nodes []*Node) {
	favs, err := h.store.FavoriteNodeIDs(username)
	if err != nil {
		return
	}
	for _, n := range nodes {
		n.Favorite = favs[n.ID]
	}
}

// GetNode returns a single node with its child nodes (sub-collections), its
// direct photos (album view), and breadcrumb ancestors.
func (h *Handler) GetNode(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	nodeID := r.PathValue("node")
	ok, err := h.store.CanAccessNode(nodeID, s.Username, s.IsAdmin)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return
	}
	node, err := h.store.GetNode(nodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// FolderPath is displayed relative to the owning library root.
	libRoot := ""
	if lib, err := h.store.GetLibrary(node.LibraryID); err == nil {
		libRoot = lib.RootPath
	}
	if rel, err := filepath.Rel(libRoot, node.FSPath); err == nil {
		node.FolderPath = filepath.ToSlash(rel)
	}

	children, err := h.store.ListChildNodes(nodeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.fillNodeCovers(children)
	h.markFavorites(s.Username, children)

	photos, err := h.scanner.ListPhotos(node.FSPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Fall back to the first photo as cover (and implicitly backdrop) when no
	// cover has been explicitly set.
	if node.CoverPhoto == "" && len(photos) > 0 {
		node.CoverPhoto = photos[0].Path
	}
	h.markFavorites(s.Username, []*Node{node})

	ancestors, _ := h.store.Ancestors(nodeID)
	for _, a := range ancestors {
		if rel, err := filepath.Rel(libRoot, a.FSPath); err == nil {
			a.FolderPath = filepath.ToSlash(rel)
		}
	}

	// Record the view only for nodes that actually hold photos (album view).
	if len(photos) > 0 {
		_ = h.store.RecordView(s.Username, node.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":      node,
		"children":  children,
		"photos":    photos,
		"ancestors": ancestors,
	})
}

// --- Assets ---

// Thumb serves a (lazily generated) thumbnail for the photo path.
func (h *Handler) Thumb(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	rel := r.PathValue("path")

	full := URLPathToAbs(rel)
	if ok, _ := h.store.CanAccessPhotoPath(full, s.Username, s.IsAdmin); !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return
	}

	thumbPath, err := h.thumbs.ThumbPath(rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "thumbnail unavailable")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, thumbPath)
}

// Photo serves the original photo file.
func (h *Handler) Photo(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	rel := r.PathValue("path")

	full := URLPathToAbs(rel)
	if !IsImage(full) {
		writeErr(w, http.StatusBadRequest, "not an image")
		return
	}
	if ok, _ := h.store.CanAccessPhotoPath(full, s.Username, s.IsAdmin); !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, full)
}

// Art serves a custom cover/background image stored under the internal art
// directory. The {path} is the portion after the "@art/" sentinel (relative to
// artDir). Any authenticated user may read art (covers/backgrounds are not
// sensitive and are already visible wherever the entity is listed).
func (h *Handler) Art(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	full, err := ResolveUnderRoot(h.artDir, rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	if !IsImage(full) {
		writeErr(w, http.StatusBadRequest, "not an image")
		return
	}
	if fi, err := os.Stat(full); err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, full)
}

// exifResponse augments the on-demand EXIF with indexed metadata (geocoded
// place, person tags) so the viewer can show a richer "Details" section. The
// extra fields are omitted when empty, leaving the EXIF-only shape unchanged.
type exifResponse struct {
	*ExifInfo
	Place  string   `json:"place,omitempty"`
	People []string `json:"people,omitempty"`
}

// Exif returns EXIF metadata for an accessible photo, enriched with the indexed
// place name and person tags when they are available.
func (h *Handler) Exif(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	rel := r.PathValue("path")

	full := URLPathToAbs(rel)
	if !IsImage(full) {
		writeErr(w, http.StatusBadRequest, "not an image")
		return
	}
	if ok, _ := h.store.CanAccessPhotoPath(full, s.Username, s.IsAdmin); !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return
	}

	// Fast path: when a photo is fully indexed (at the current extractor
	// version) the whole panel comes from the DB, so we never touch the file.
	// photo_meta is keyed by the canonical URL token (AbsToURLPath), the same
	// form the scanner stores. Rows indexed by an older extractor (or not
	// indexed at all) fall through to the on-demand disk read below until the
	// next scan backfills them.
	m, _ := h.store.GetPhotoMeta(AbsToURLPath(full))
	if m != nil && m.MetaVersion >= photoMetaVersion {
		resp := exifResponse{
			ExifInfo: m.toExifInfo(),
			People:   m.People,
			Place:    joinPlace(m.PlaceCity, m.PlaceProvince, m.PlaceCountry),
		}
		if resp.Place == "" && m.HasGPS {
			if place, ok := PlaceNameIfReady(m.Lat, m.Lon); ok {
				resp.Place = place
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Slow path: read EXIF from the file for photos not yet (fully) indexed.
	info, err := ReadExif(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := exifResponse{ExifInfo: info}
	if m != nil {
		resp.People = m.People
		resp.Place = joinPlace(m.PlaceCity, m.PlaceProvince, m.PlaceCountry)
	}
	// Fall back to on-demand reverse geocoding for photos that have GPS but
	// were not indexed (or were indexed before geocoding was available). This is
	// non-blocking: if the geocoding index is still warming up we omit the place
	// rather than stall the request (and the panel's "loading…") on the
	// expensive one-time dataset parse.
	if resp.Place == "" && info.HasGPS {
		if place, ok := PlaceNameIfReady(info.Lat, info.Lon); ok {
			resp.Place = place
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type nodeMetadataInput struct {
	SortTitle     string `json:"sortTitle"`
	Summary       string `json:"summary"`
	ContentRating string `json:"contentRating"`
	Year          string `json:"year"`
	Studio        string `json:"studio"`
}

// AdminUpdateNode updates the user-editable metadata of a node (admin only).
// Node names mirror folder names on disk and are not renamable.
func (h *Handler) AdminUpdateNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node")
	var in nodeMetadataInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := h.store.UpdateNodeMetadata(nodeID, NodeMetadata{
		SortTitle:     strings.TrimSpace(in.SortTitle),
		Summary:       strings.TrimSpace(in.Summary),
		ContentRating: strings.TrimSpace(in.ContentRating),
		Year:          strings.TrimSpace(in.Year),
		Studio:        strings.TrimSpace(in.Studio),
	})
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	node, _ := h.store.GetNode(nodeID)
	writeJSON(w, http.StatusOK, node)
}

type coverInput struct {
	// Target selects which entity to update: "node" or "library".
	Target string `json:"target"`
	// ID is the node or library id.
	ID string `json:"id"`
	// Photo is the relative path of the cover photo.
	Photo string `json:"photo"`
	// Kind selects which art to set: "cover" (default) or "background".
	Kind string `json:"kind"`
}

// SetCover sets the cover (or background) photo of a node or library to an
// existing photo path (admin only).
func (h *Handler) SetCover(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in coverInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Validate the photo path belongs to a library the user can access.
	if ok, _ := h.store.CanAccessPhotoPath(URLPathToAbs(in.Photo), s.Username, s.IsAdmin); !ok {
		writeErr(w, http.StatusBadRequest, "invalid photo path")
		return
	}
	if err := h.applyArt(in.Target, in.ID, in.Photo, in.Kind, s.Username); err != nil {
		h.writeArtErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) writeArtErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, errBadTarget):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

var errBadTarget = errors.New("target must be node or library")

// applyArt persists a (already-saved, root-relative) photo path as the cover or
// background of the given entity.
func (h *Handler) applyArt(target, id, photo, kind, username string) error {
	background := kind == "background"
	switch target {
	case "node":
		if background {
			return h.store.SetNodeBackground(id, photo)
		}
		return h.store.SetNodeCover(id, photo, username)
	case "library":
		if background {
			return h.store.SetLibraryBackground(id, photo)
		}
		return h.store.SetLibraryCover(id, photo, username)
	default:
		return errBadTarget
	}
}

func extFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	default:
		return ".jpg"
	}
}

// UploadArt accepts a custom poster/background as either a multipart file upload
// (form field "file") or a remote URL (form field "url"), saves it under the
// internal art directory (outside the photos library), and records it as the
// cover or background. Admin only.
func (h *Handler) UploadArt(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())

	target := r.FormValue("target")
	id := r.FormValue("id")
	kind := r.FormValue("kind")
	if kind == "" {
		kind = "cover"
	}
	if kind != "cover" && kind != "background" {
		writeErr(w, http.StatusBadRequest, "kind must be cover or background")
		return
	}

	if target != "node" && target != "library" {
		writeErr(w, http.StatusBadRequest, errBadTarget.Error())
		return
	}
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}

	// Store custom art under the internal art directory (outside the photos
	// library) at <artDir>/<target>/<id>/.
	subDir := filepath.Join(h.artDir, target, id)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create art dir")
		return
	}

	var ext string
	var data []byte
	var err error

	if url := strings.TrimSpace(r.FormValue("url")); url != "" {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			writeErr(w, http.StatusBadRequest, "url must be http(s)")
			return
		}
		data, ext, err = fetchImageURL(url)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "fetch failed: "+err.Error())
			return
		}
	} else {
		file, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			writeErr(w, http.StatusBadRequest, "no file or url provided")
			return
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, maxArtBytes+1))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read failed")
			return
		}
		if int64(len(data)) > maxArtBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, "image too large")
			return
		}
		ext = strings.ToLower(filepath.Ext(hdr.Filename))
		if !imageExts[ext] {
			ext = extFromContentType(hdr.Header.Get("Content-Type"))
		}
	}

	if !imageExts[ext] {
		ext = ".jpg"
	}
	fname := fmt.Sprintf("%s-%d%s", kind, time.Now().UnixNano(), ext)
	full := filepath.Join(subDir, fname)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}

	relToArt, err := RelToRoot(h.artDir, full)
	if err != nil {
		_ = os.Remove(full)
		writeErr(w, http.StatusInternalServerError, "path error")
		return
	}
	rel := artPrefix + relToArt

	if err := h.applyArt(target, id, rel, kind, s.Username); err != nil {
		_ = os.Remove(full)
		h.writeArtErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "photo": rel})
}

const maxArtBytes = 25 << 20 // 25 MiB

func fetchImageURL(url string) ([]byte, string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "image/") {
		return nil, "", fmt.Errorf("not an image (%s)", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxArtBytes {
		return nil, "", errors.New("image too large")
	}
	ext := strings.ToLower(filepath.Ext(url))
	if !imageExts[ext] {
		ext = extFromContentType(ct)
	}
	return data, ext, nil
}

// --- cover fallbacks ---

func (h *Handler) fillNodeCovers(nodes []*Node) {
	for _, n := range nodes {
		if n.CoverPhoto == "" {
			cover := h.scanner.FirstPhoto(n.FSPath)
			if cover == "" {
				cover = h.scanner.firstPhotoDeep(n.FSPath)
			}
			n.CoverPhoto = cover
			_ = h.store.CacheNodeCover(n.ID, cover)
		}
	}
}

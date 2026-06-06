package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"plex-photos/auth"
	"plex-photos/library"
)

// Deps holds the wired dependencies a server needs.
type Deps struct {
	Provider  auth.Provider
	Sessions  *auth.SessionManager
	Mw        *auth.Middleware
	Gallery   *library.Handler
	StaticDir string
}

// NewMux builds the application's HTTP routes. It is shared by the main binary
// and the integration tests so both exercise identical routing.
func NewMux(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	// --- Auth ---
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		url, err := d.Provider.StartLogin(w, r)
		if err != nil {
			http.Error(w, "login failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	})
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		user, err := d.Provider.HandleCallback(w, r)
		if err != nil {
			http.Error(w, "authentication failed: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if err := d.Sessions.Set(w, user); err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}
		if err := d.Gallery.RecordLogin(user.Username, user.Email, user.IsAdmin); err != nil {
			log.Printf("record login for %s: %v", user.Username, err)
		}
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("GET /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		d.Sessions.Clear(w)
		http.Redirect(w, r, "/", http.StatusFound)
	})

	// --- Current user ---
	mux.Handle("GET /api/me", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.Me)))
	mux.Handle("GET /api/preferences", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.GetPreferences)))
	mux.Handle("PUT /api/preferences", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.PutPreferences)))

	// --- Admin libraries ---
	mux.Handle("GET /api/admin/browse", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminBrowseDirs)))
	mux.Handle("GET /api/admin/libraries", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminListLibraries)))
	mux.Handle("POST /api/admin/libraries", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminCreateLibrary)))
	mux.Handle("PUT /api/admin/libraries/{id}", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminUpdateLibrary)))
	mux.Handle("DELETE /api/admin/libraries/{id}", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminDeleteLibrary)))
	mux.Handle("POST /api/admin/libraries/{id}/scan", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminScanLibrary)))
	mux.Handle("GET /api/admin/libraries/{id}/scan-progress", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminScanProgress)))
	mux.Handle("PUT /api/admin/nodes/{node}", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminUpdateNode)))

	// --- Admin settings ---
	mux.Handle("GET /api/admin/settings", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminGetSettings)))
	mux.Handle("PUT /api/admin/settings", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminUpdateSettings)))

	// --- Admin scan error log ---
	mux.Handle("GET /api/admin/errors", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminListScanErrors)))
	mux.Handle("DELETE /api/admin/errors", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminClearScanErrors)))

	// --- Admin users / library access ---
	mux.Handle("GET /api/admin/users", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminListUsers)))
	mux.Handle("POST /api/admin/users", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminCreateUser)))
	mux.Handle("PUT /api/admin/users/{username}", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminUpdateUserAccess)))
	mux.Handle("DELETE /api/admin/users/{username}", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.AdminDeleteUser)))

	// --- Navigation ---
	mux.Handle("GET /api/libraries", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.ListLibraries)))
	mux.Handle("GET /api/libraries/{id}/nodes", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.ListTopNodes)))
	mux.Handle("GET /api/nodes/{node}", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.GetNode)))

	// --- Home swimlanes ---
	mux.Handle("GET /api/favorites", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.ListFavorites)))
	mux.Handle("GET /api/recent", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.ListRecent)))
	mux.Handle("GET /api/libraries/{id}/random-albums", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.ListRandomAlbums)))
	mux.Handle("PUT /api/nodes/{node}/favorite", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.SetFavorite)))
	mux.Handle("DELETE /api/nodes/{node}/favorite", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.SetFavorite)))

	// --- Assets ---
	mux.Handle("GET /api/thumb/{path...}", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.Thumb)))
	mux.Handle("GET /api/photo/{path...}", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.Photo)))
	mux.Handle("GET /api/exif/{path...}", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.Exif)))
	mux.Handle("GET /api/art/{path...}", d.Mw.RequireAuth(http.HandlerFunc(d.Gallery.Art)))
	mux.Handle("PUT /api/cover", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.SetCover)))
	mux.Handle("POST /api/art", d.Mw.RequireAdmin(http.HandlerFunc(d.Gallery.UploadArt)))

	// --- Static frontend ---
	staticDir := d.StaticDir
	if staticDir == "" {
		staticDir = "static"
	}
	fileServer := http.FileServer(http.Dir(staticDir))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(filepath.Join(staticDir, filepath.Clean(r.URL.Path))); err != nil {
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	return mux
}

// LogRequests is a simple request-logging middleware.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

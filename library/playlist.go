package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"plex-photos/auth"
)

// Playlist is a user-owned, ordered set of photos curated by hand, independent
// of the folder tree. It is private to its owner.
type Playlist struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CoverPhoto string    `json:"coverPhoto,omitempty"`
	PhotoCount int       `json:"photoCount"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// PlaylistPhoto is a single photo entry of a playlist. Path is the URL token
// (same as Photo.Path) and Name is the display name.
type PlaylistPhoto struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// --- Store: playlists ---

// CreatePlaylist creates an empty playlist owned by the given user.
func (s *Store) CreatePlaylist(owner, name string) (*Playlist, error) {
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO playlists (id, owner, name) VALUES (?, ?, ?)`,
		id, owner, name); err != nil {
		return nil, err
	}
	return s.GetPlaylist(owner, id)
}

// ListPlaylists returns the user's playlists, most recently used first, each
// with a derived cover (explicit cover or first item) and photo count.
func (s *Store) ListPlaylists(owner string) ([]*Playlist, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, COALESCE(p.cover_photo, ''),
		       (SELECT i.photo_path FROM playlist_items i WHERE i.playlist_id = p.id ORDER BY i.position, i.id LIMIT 1),
		       (SELECT COUNT(*) FROM playlist_items i WHERE i.playlist_id = p.id),
		       p.created_at, p.updated_at
		FROM playlists p
		WHERE p.owner = ?
		ORDER BY p.last_used_at DESC, p.name`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Playlist{}
	for rows.Next() {
		p, err := scanPlaylistRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPlaylistRow(sc interface{ Scan(...any) error }) (*Playlist, error) {
	var p Playlist
	var cover string
	var firstPhoto sql.NullString
	if err := sc.Scan(&p.ID, &p.Name, &cover, &firstPhoto, &p.PhotoCount, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if cover != "" {
		p.CoverPhoto = cover
	} else if firstPhoto.Valid {
		p.CoverPhoto = firstPhoto.String
	}
	return &p, nil
}

// GetPlaylist returns a single playlist owned by the user.
func (s *Store) GetPlaylist(owner, id string) (*Playlist, error) {
	p, err := scanPlaylistRow(s.db.QueryRow(`
		SELECT p.id, p.name, COALESCE(p.cover_photo, ''),
		       (SELECT i.photo_path FROM playlist_items i WHERE i.playlist_id = p.id ORDER BY i.position, i.id LIMIT 1),
		       (SELECT COUNT(*) FROM playlist_items i WHERE i.playlist_id = p.id),
		       p.created_at, p.updated_at
		FROM playlists p
		WHERE p.owner = ? AND p.id = ?`, owner, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RenamePlaylist updates a playlist's name (owner-scoped).
func (s *Store) RenamePlaylist(owner, id, name string) error {
	res, err := s.db.Exec(
		`UPDATE playlists SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND owner = ?`,
		name, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlaylist removes a playlist (and its items via cascade), owner-scoped.
func (s *Store) DeletePlaylist(owner, id string) error {
	res, err := s.db.Exec(`DELETE FROM playlists WHERE id = ? AND owner = ?`, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddPlaylistPhotos appends photos to a playlist, skipping duplicates, and bumps
// the playlist's last_used_at. Returns how many new entries were added. Caller
// must verify ownership and per-photo access first.
func (s *Store) AddPlaylistPhotos(owner, id string, photos []PlaylistPhoto) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Confirm ownership inside the transaction.
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM playlists WHERE id = ? AND owner = ?`, id, owner).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}

	var pos int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) FROM playlist_items WHERE playlist_id = ?`, id).Scan(&pos); err != nil {
		return 0, err
	}

	added := 0
	for _, ph := range photos {
		if strings.TrimSpace(ph.Path) == "" {
			continue
		}
		pos++
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO playlist_items (playlist_id, photo_path, photo_name, position) VALUES (?, ?, ?, ?)`,
			id, ph.Path, ph.Name, pos)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		} else {
			pos-- // re-add skipped; don't burn a position number
		}
	}
	if _, err := tx.Exec(
		`UPDATE playlists SET updated_at = CURRENT_TIMESTAMP, last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// RemovePlaylistPhoto removes a single photo (by path) from a playlist.
func (s *Store) RemovePlaylistPhoto(owner, id, photoPath string) error {
	res, err := s.db.Exec(`
		DELETE FROM playlist_items
		WHERE photo_path = ? AND playlist_id IN (SELECT id FROM playlists WHERE id = ? AND owner = ?)`,
		photoPath, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.Exec(`UPDATE playlists SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return nil
}

// PlaylistPhotos returns the ordered photos of a playlist (owner-scoped).
func (s *Store) PlaylistPhotos(owner, id string) ([]PlaylistPhoto, error) {
	rows, err := s.db.Query(`
		SELECT i.photo_path, COALESCE(i.photo_name, '')
		FROM playlist_items i
		JOIN playlists p ON p.id = i.playlist_id
		WHERE i.playlist_id = ? AND p.owner = ?
		ORDER BY i.position, i.id`, id, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaylistPhoto{}
	for rows.Next() {
		var ph PlaylistPhoto
		if err := rows.Scan(&ph.Path, &ph.Name); err != nil {
			return nil, err
		}
		out = append(out, ph)
	}
	return out, rows.Err()
}

// --- Handlers ---

// maxPlaylistNameLen caps a playlist name to a sane length.
const maxPlaylistNameLen = 120

// ListPlaylists returns the current user's playlists.
func (h *Handler) ListPlaylists(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	pls, err := h.store.ListPlaylists(s.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pls)
}

type playlistNameInput struct {
	Name string `json:"name"`
}

// CreatePlaylist creates a new (empty) playlist for the current user.
func (h *Handler) CreatePlaylist(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in playlistNameInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(name) > maxPlaylistNameLen {
		name = name[:maxPlaylistNameLen]
	}
	pl, err := h.store.CreatePlaylist(s.Username, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pl)
}

// GetPlaylist returns a playlist with its ordered photos.
func (h *Handler) GetPlaylist(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	pl, err := h.store.GetPlaylist(s.Username, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "playlist not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	photos, err := h.store.PlaylistPhotos(s.Username, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Drop any photos the user can no longer access (e.g. revoked library).
	visible := make([]PlaylistPhoto, 0, len(photos))
	for _, ph := range photos {
		if ok, _ := h.store.CanAccessPhotoPath(URLPathToAbs(ph.Path), s.Username, s.IsAdmin); ok {
			visible = append(visible, ph)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"playlist": pl,
		"photos":   visible,
	})
}

// RenamePlaylist updates a playlist's name.
func (h *Handler) RenamePlaylist(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var in playlistNameInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(name) > maxPlaylistNameLen {
		name = name[:maxPlaylistNameLen]
	}
	err := h.store.RenamePlaylist(s.Username, id, name)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "playlist not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pl, _ := h.store.GetPlaylist(s.Username, id)
	writeJSON(w, http.StatusOK, pl)
}

// DeletePlaylist removes a playlist.
func (h *Handler) DeletePlaylist(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	err := h.store.DeletePlaylist(s.Username, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "playlist not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxPlaylistAddPhotos caps how many photos one add request may carry.
const maxPlaylistAddPhotos = 5000

type playlistItemsInput struct {
	Photos []PlaylistPhoto `json:"photos"`
}

// AddPlaylistItems appends photos to a playlist. Photos the user cannot access
// are silently skipped.
func (h *Handler) AddPlaylistItems(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var in playlistItemsInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(in.Photos) == 0 {
		writeErr(w, http.StatusBadRequest, "no photos provided")
		return
	}
	if len(in.Photos) > maxPlaylistAddPhotos {
		in.Photos = in.Photos[:maxPlaylistAddPhotos]
	}
	// Keep only photos the user may access; sanitize/derive a display name.
	allowed := make([]PlaylistPhoto, 0, len(in.Photos))
	for _, ph := range in.Photos {
		path := strings.TrimSpace(ph.Path)
		if path == "" {
			continue
		}
		if ok, _ := h.store.CanAccessPhotoPath(URLPathToAbs(path), s.Username, s.IsAdmin); !ok {
			continue
		}
		name := strings.TrimSpace(ph.Name)
		if name == "" {
			name = baseName(path)
		}
		allowed = append(allowed, PlaylistPhoto{Name: name, Path: path})
	}
	if len(allowed) == 0 {
		writeErr(w, http.StatusBadRequest, "no accessible photos provided")
		return
	}
	added, err := h.store.AddPlaylistPhotos(s.Username, id, allowed)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "playlist not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pl, _ := h.store.GetPlaylist(s.Username, id)
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "playlist": pl})
}

type playlistRemoveInput struct {
	Path string `json:"path"`
}

// RemovePlaylistItem removes a single photo from a playlist.
func (h *Handler) RemovePlaylistItem(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var in playlistRemoveInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	err := h.store.RemovePlaylistPhoto(s.Username, id, in.Path)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// baseName returns the trailing path segment of a slash-separated URL token.
func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

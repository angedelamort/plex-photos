package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"plex-photos/auth"
)

// ErrInvalidRating is returned when a rating is outside 1..5.
var ErrInvalidRating = errors.New("rating must be between 1 and 5")

// --- Store: photo ratings ---

// GetPhotoRating returns the user's rating for a photo (1–5), or 0 if unset.
// photoPath must be the canonical URL token (AbsToURLPath).
func (s *Store) GetPhotoRating(username, photoPath string) (int, error) {
	var rating int
	err := s.db.QueryRow(
		`SELECT rating FROM photo_ratings WHERE plex_username = ? AND photo_path = ?`,
		username, photoPath,
	).Scan(&rating)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return rating, nil
}

// SetPhotoRating upserts a 1–5 star rating for a photo.
func (s *Store) SetPhotoRating(username, photoPath string, rating int) error {
	if rating < 1 || rating > 5 {
		return ErrInvalidRating
	}
	_, err := s.db.Exec(`
		INSERT INTO photo_ratings (plex_username, photo_path, rating, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(plex_username, photo_path) DO UPDATE SET
		  rating = excluded.rating,
		  updated_at = CURRENT_TIMESTAMP`,
		username, photoPath, rating,
	)
	return err
}

// ClearPhotoRating removes a user's rating for a photo.
func (s *Store) ClearPhotoRating(username, photoPath string) error {
	_, err := s.db.Exec(
		`DELETE FROM photo_ratings WHERE plex_username = ? AND photo_path = ?`,
		username, photoPath,
	)
	return err
}

// --- Handler ---

// GetPhotoRating returns the current user's rating for a photo.
func (h *Handler) GetPhotoRating(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	token, ok := h.authorizePhotoToken(w, r, s.Username, s.IsAdmin)
	if !ok {
		return
	}
	rating, err := h.store.GetPhotoRating(s.Username, token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rating": rating})
}

type ratingInput struct {
	Rating int `json:"rating"`
}

// SetPhotoRating sets (PUT) or clears (DELETE) the current user's rating.
// PUT body: {"rating": 1..5}.
func (h *Handler) SetPhotoRating(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	token, ok := h.authorizePhotoToken(w, r, s.Username, s.IsAdmin)
	if !ok {
		return
	}

	if r.Method == http.MethodDelete {
		if err := h.store.ClearPhotoRating(s.Username, token); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rating": 0})
		return
	}

	var in ratingInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Rating < 1 || in.Rating > 5 {
		writeErr(w, http.StatusBadRequest, "rating must be between 1 and 5")
		return
	}
	if err := h.store.SetPhotoRating(s.Username, token, in.Rating); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rating": in.Rating})
}

// authorizePhotoToken checks access for the {path...} URL token and returns the
// canonical AbsToURLPath form used as the photo_ratings key.
func (h *Handler) authorizePhotoToken(w http.ResponseWriter, r *http.Request, username string, isAdmin bool) (string, bool) {
	rel := r.PathValue("path")
	full := URLPathToAbs(rel)
	if !IsImage(full) {
		writeErr(w, http.StatusBadRequest, "not an image")
		return "", false
	}
	ok, err := h.store.CanAccessPhotoPath(full, username, isAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "no access")
		return "", false
	}
	return AbsToURLPath(full), true
}

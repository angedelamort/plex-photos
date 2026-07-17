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

// SmartCollectionRules is the persisted rule definition for a smart collection.
// Empty libraryIds/nodeIds mean no restriction (all accessible libraries/folders).
type SmartCollectionRules struct {
	MinRating  int      `json:"minRating"`
	LibraryIDs []string `json:"libraryIds,omitempty"`
	NodeIDs    []string `json:"nodeIds,omitempty"`
}

// SmartCollection is a user-owned, rule-based photo set evaluated at query time.
type SmartCollection struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Rules      SmartCollectionRules `json:"rules"`
	CoverPhoto string               `json:"coverPhoto,omitempty"`
	PhotoCount int                  `json:"photoCount"`
	CreatedAt  time.Time            `json:"createdAt"`
	UpdatedAt  time.Time            `json:"updatedAt"`
}

func validateSmartRules(rules SmartCollectionRules) error {
	if rules.MinRating < 1 || rules.MinRating > 5 {
		return ErrInvalidRating
	}
	return nil
}

// validateSmartScope checks that optional library/node filters refer to items
// the user can access.
func (s *Store) validateSmartScope(owner string, isAdmin bool, rules SmartCollectionRules) error {
	for _, id := range rules.LibraryIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ok, err := s.CanAccessLibrary(id, owner, isAdmin)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	for _, id := range rules.NodeIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ok, err := s.CanAccessNode(id, owner, isAdmin)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func normalizeSmartRules(rules SmartCollectionRules) SmartCollectionRules {
	rules.LibraryIDs = dedupeStrings(rules.LibraryIDs)
	rules.NodeIDs = dedupeStrings(rules.NodeIDs)
	return rules
}

func (s *Store) resolveSmartScope(owner string, isAdmin bool, rules SmartCollectionRules) (libraryRoots, nodeRoots []string, err error) {
	for _, id := range rules.LibraryIDs {
		lib, err := s.GetLibrary(id)
		if err != nil {
			return nil, nil, err
		}
		libraryRoots = append(libraryRoots, lib.RootPath)
	}
	for _, id := range rules.NodeIDs {
		node, err := s.GetNode(id)
		if err != nil {
			return nil, nil, err
		}
		nodeRoots = append(nodeRoots, node.FSPath)
	}
	return libraryRoots, nodeRoots, nil
}

func photoMatchesSmartScope(fullPath string, libraryRoots, nodeRoots []string) bool {
	if len(libraryRoots) > 0 {
		ok := false
		for _, root := range libraryRoots {
			if underRoot(root, fullPath) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(nodeRoots) > 0 {
		ok := false
		for _, root := range nodeRoots {
			if underRoot(root, fullPath) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func encodeSmartRules(rules SmartCollectionRules) (string, error) {
	b, err := json.Marshal(rules)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeSmartRules(raw string) (SmartCollectionRules, error) {
	var rules SmartCollectionRules
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return SmartCollectionRules{}, err
	}
	return rules, nil
}

func scanSmartCollectionRow(sc interface{ Scan(...any) error }) (*SmartCollection, string, error) {
	var c SmartCollection
	var rulesJSON string
	if err := sc.Scan(&c.ID, &c.Name, &rulesJSON, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, "", err
	}
	rules, err := decodeSmartRules(rulesJSON)
	if err != nil {
		return nil, "", err
	}
	c.Rules = rules
	return &c, rulesJSON, nil
}

// CreateSmartCollection creates a smart collection owned by the given user.
func (s *Store) CreateSmartCollection(owner, name string, rules SmartCollectionRules, isAdmin bool) (*SmartCollection, error) {
	rules = normalizeSmartRules(rules)
	if err := validateSmartRules(rules); err != nil {
		return nil, err
	}
	if err := s.validateSmartScope(owner, isAdmin, rules); err != nil {
		return nil, err
	}
	rulesJSON, err := encodeSmartRules(rules)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO smart_collections (id, owner, name, rules) VALUES (?, ?, ?, ?)`,
		id, owner, name, rulesJSON); err != nil {
		return nil, err
	}
	return s.GetSmartCollection(owner, id)
}

// ListSmartCollections returns the user's smart collections, most recently updated first.
func (s *Store) ListSmartCollections(owner string, isAdmin bool) ([]*SmartCollection, error) {
	rows, err := s.db.Query(`
		SELECT id, name, rules, created_at, updated_at
		FROM smart_collections
		WHERE owner = ?
		ORDER BY updated_at DESC, name`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SmartCollection{}
	for rows.Next() {
		c, _, err := scanSmartCollectionRow(rows)
		if err != nil {
			return nil, err
		}
		photos, err := s.evaluateSmartRules(owner, c.Rules, isAdmin)
		if err != nil {
			return nil, err
		}
		c.PhotoCount = len(photos)
		if len(photos) > 0 {
			c.CoverPhoto = photos[0].Path
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetSmartCollection returns a single smart collection owned by the user.
func (s *Store) GetSmartCollection(owner, id string) (*SmartCollection, error) {
	c, _, err := scanSmartCollectionRow(s.db.QueryRow(`
		SELECT id, name, rules, created_at, updated_at
		FROM smart_collections
		WHERE owner = ? AND id = ?`, owner, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateSmartCollection updates name and rules (owner-scoped).
func (s *Store) UpdateSmartCollection(owner, id, name string, rules SmartCollectionRules, isAdmin bool) error {
	rules = normalizeSmartRules(rules)
	if err := validateSmartRules(rules); err != nil {
		return err
	}
	if err := s.validateSmartScope(owner, isAdmin, rules); err != nil {
		return err
	}
	rulesJSON, err := encodeSmartRules(rules)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE smart_collections
		SET name = ?, rules = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND owner = ?`,
		name, rulesJSON, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSmartCollection removes a smart collection (owner-scoped).
func (s *Store) DeleteSmartCollection(owner, id string) error {
	res, err := s.db.Exec(`DELETE FROM smart_collections WHERE id = ? AND owner = ?`, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EvaluateSmartCollection returns accessible photos matching the collection's rules.
func (s *Store) EvaluateSmartCollection(owner, id string, isAdmin bool) ([]PlaylistPhoto, error) {
	c, err := s.GetSmartCollection(owner, id)
	if err != nil {
		return nil, err
	}
	return s.evaluateSmartRules(owner, c.Rules, isAdmin)
}

func (s *Store) evaluateSmartRules(owner string, rules SmartCollectionRules, isAdmin bool) ([]PlaylistPhoto, error) {
	rules = normalizeSmartRules(rules)
	if err := validateSmartRules(rules); err != nil {
		return nil, err
	}
	libraryRoots, nodeRoots, err := s.resolveSmartScope(owner, isAdmin, rules)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT photo_path
		FROM photo_ratings
		WHERE plex_username = ? AND rating >= ?
		ORDER BY rating DESC, updated_at DESC`, owner, rules.MinRating)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PlaylistPhoto{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		full := URLPathToAbs(path)
		if ok, _ := s.CanAccessPhotoPath(full, owner, isAdmin); !ok {
			continue
		}
		if !photoMatchesSmartScope(full, libraryRoots, nodeRoots) {
			continue
		}
		out = append(out, PlaylistPhoto{Name: baseName(path), Path: path})
	}
	return out, rows.Err()
}

// --- Handlers ---

type smartCollectionInput struct {
	Name       string   `json:"name"`
	MinRating  int      `json:"minRating"`
	LibraryIDs []string `json:"libraryIds"`
	NodeIDs    []string `json:"nodeIds"`
}

func smartRulesFromInput(in smartCollectionInput) SmartCollectionRules {
	return normalizeSmartRules(SmartCollectionRules{
		MinRating:  in.MinRating,
		LibraryIDs: in.LibraryIDs,
		NodeIDs:    in.NodeIDs,
	})
}

// ListSmartCollections returns the current user's smart collections.
func (h *Handler) ListSmartCollections(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	cols, err := h.store.ListSmartCollections(s.Username, s.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

// CreateSmartCollection creates a new smart collection for the current user.
func (h *Handler) CreateSmartCollection(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	var in smartCollectionInput
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
	rules := smartRulesFromInput(in)
	if err := validateSmartRules(rules); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	col, err := h.store.CreateSmartCollection(s.Username, name, rules, s.IsAdmin)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusBadRequest, "invalid library or folder scope")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	photos, _ := h.store.evaluateSmartRules(s.Username, rules, s.IsAdmin)
	col.PhotoCount = len(photos)
	if len(photos) > 0 {
		col.CoverPhoto = photos[0].Path
	}
	writeJSON(w, http.StatusCreated, col)
}

// GetSmartCollection returns a smart collection with its evaluated photos.
func (h *Handler) GetSmartCollection(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	col, err := h.store.GetSmartCollection(s.Username, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "smart collection not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	photos, err := h.store.EvaluateSmartCollection(s.Username, id, s.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	col.PhotoCount = len(photos)
	if len(photos) > 0 {
		col.CoverPhoto = photos[0].Path
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collection": col,
		"photos":     photos,
	})
}

// UpdateSmartCollection updates a smart collection's name and rules.
func (h *Handler) UpdateSmartCollection(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var in smartCollectionInput
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
	rules := smartRulesFromInput(in)
	if err := validateSmartRules(rules); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := h.store.UpdateSmartCollection(s.Username, id, name, rules, s.IsAdmin)
	if errors.Is(err, ErrNotFound) {
		// Distinguish missing collection from invalid scope by checking existence.
		if _, getErr := h.store.GetSmartCollection(s.Username, id); getErr != nil {
			writeErr(w, http.StatusNotFound, "smart collection not found")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid library or folder scope")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	col, _ := h.store.GetSmartCollection(s.Username, id)
	photos, _ := h.store.evaluateSmartRules(s.Username, rules, s.IsAdmin)
	col.PhotoCount = len(photos)
	if len(photos) > 0 {
		col.CoverPhoto = photos[0].Path
	}
	writeJSON(w, http.StatusOK, col)
}

// DeleteSmartCollection removes a smart collection.
func (h *Handler) DeleteSmartCollection(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	err := h.store.DeleteSmartCollection(s.Username, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "smart collection not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

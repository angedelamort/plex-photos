package library

import (
	"errors"
	"net/http"

	"plex-photos/auth"
)

// homeAlbumLimit caps the number of albums returned per home swimlane.
const (
	recentLimit = 20
	randomLimit = 12
)

// fillHomeNodeCovers backfills missing covers from the first photo on disk and
// marks favorites for the given user.
func (h *Handler) fillHomeNodeCovers(username string, nodes []*HomeNode) {
	favs, _ := h.store.FavoriteNodeIDs(username)
	for _, a := range nodes {
		if a.CoverPhoto == "" {
			cover := h.scanner.FirstPhoto(a.FSPath)
			if cover == "" {
				cover = h.scanner.firstPhotoDeep(a.FSPath)
			}
			a.CoverPhoto = cover
		}
		a.Favorite = favs[a.ID]
	}
}

// accessibleHomeNodes filters nodes down to libraries the user can access.
func (h *Handler) accessibleHomeNodes(s *auth.Session, nodes []*HomeNode) []*HomeNode {
	if s.IsAdmin {
		return nodes
	}
	allowed := map[string]bool{}
	libs, err := h.store.ListLibrariesForUser(s.Username, s.IsAdmin)
	if err != nil {
		return []*HomeNode{}
	}
	for _, l := range libs {
		allowed[l.ID] = true
	}
	out := make([]*HomeNode, 0, len(nodes))
	for _, a := range nodes {
		if allowed[a.LibraryID] {
			out = append(out, a)
		}
	}
	return out
}

// ListFavorites returns the current user's favorited nodes (cross-library).
func (h *Handler) ListFavorites(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	nodes, err := h.store.FavoriteNodes(s.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodes = h.accessibleHomeNodes(s, nodes)
	h.fillHomeNodeCovers(s.Username, nodes)
	writeJSON(w, http.StatusOK, nodes)
}

// SetFavorite toggles favorite status for a node. Body: {"favorite": bool}.
func (h *Handler) SetFavorite(w http.ResponseWriter, r *http.Request) {
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
	fav := r.Method == http.MethodPut
	if err := h.store.SetFavorite(s.Username, nodeID, fav); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorite": fav})
}

// ListRecent returns the current user's most recently viewed nodes.
func (h *Handler) ListRecent(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	nodes, err := h.store.RecentNodes(s.Username, recentLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodes = h.accessibleHomeNodes(s, nodes)
	h.fillHomeNodeCovers(s.Username, nodes)
	writeJSON(w, http.StatusOK, nodes)
}

// ListRandomAlbums returns a random sample of photo-bearing nodes from one library.
func (h *Handler) ListRandomAlbums(w http.ResponseWriter, r *http.Request) {
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
	nodes, err := h.store.RandomLibraryNodes(libID, randomLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.fillHomeNodeCovers(s.Username, nodes)
	writeJSON(w, http.StatusOK, nodes)
}

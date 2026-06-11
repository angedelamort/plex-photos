package player

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"plex-photos/auth"
	"plex-photos/frame-tv/tv"
)

const (
	maxTVNameLen = 120
	minIntervalS = 30    // shortest swap interval offered in the UI
	maxIntervalS = 86400 // 24h
	probeTimeout = 20 * time.Second
)

// Handler exposes the TV management + playback HTTP API.
type Handler struct {
	store *Store
	mgr   *Manager
}

// NewHandler builds the HTTP handler.
func NewHandler(store *Store, mgr *Manager) *Handler {
	return &Handler{store: store, mgr: mgr}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusView merges a TV's config with its live snapshot for the UI.
func (h *Handler) statusView(t *TV) map[string]any {
	snap, _ := h.mgr.Status(t.ID)
	return map[string]any{
		"id":              t.ID,
		"name":            t.Name,
		"matte":           t.Matte,
		"intervalSeconds": t.IntervalS,
		"status":          snap,
	}
}

// --- Admin: TV CRUD + test ---

// ListTVsAdmin returns every configured TV with full details.
func (h *Handler) ListTVsAdmin(w http.ResponseWriter, r *http.Request) {
	tvs, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tvs)
}

type tvInput struct {
	Name            string   `json:"name"`
	IP              string   `json:"ip"`
	Matte           string   `json:"matte"`
	IntervalSeconds int      `json:"intervalSeconds"`
	DisplayMode     string   `json:"displayMode"`
	BgColor         string   `json:"bgColor"`
	BorderPct       int      `json:"borderPct"`
	CaptionFields   []string `json:"captionFields"`
	PlayOrder       string   `json:"playOrder"`
}

var validDisplayModes = map[string]bool{
	ModeBlurFill: true, ModeFill: true, ModeFitColor: true, ModeTVMatte: true,
}

var validCaptionFields = map[string]bool{
	capDate: true, capYear: true, capCamera: true, capLocation: true, capFilename: true, capAlbum: true,
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// normalize validates and sanitizes the input into a TV value object.
func (in *tvInput) normalize() (TV, error) {
	tv := TV{
		Name:        strings.TrimSpace(in.Name),
		IP:          strings.TrimSpace(in.IP),
		Matte:       strings.TrimSpace(in.Matte),
		DisplayMode: strings.TrimSpace(in.DisplayMode),
		BgColor:     strings.TrimSpace(in.BgColor),
		BorderPct:   in.BorderPct,
		PlayOrder:   strings.TrimSpace(in.PlayOrder),
	}
	if tv.Name == "" {
		return TV{}, errors.New("name is required")
	}
	if len(tv.Name) > maxTVNameLen {
		tv.Name = tv.Name[:maxTVNameLen]
	}
	if tv.IP == "" {
		return TV{}, errors.New("ip address is required")
	}
	if tv.Matte == "" {
		tv.Matte = "none"
	}
	if !validDisplayModes[tv.DisplayMode] {
		tv.DisplayMode = ModeBlurFill
	}
	if tv.PlayOrder != OrderRandom {
		tv.PlayOrder = OrderSequential
	}
	if !hexColorRe.MatchString(tv.BgColor) {
		tv.BgColor = "#000000"
	}
	if tv.BorderPct < 0 {
		tv.BorderPct = 0
	}
	if tv.BorderPct > 40 {
		tv.BorderPct = 40
	}
	// Keep only recognized caption fields, de-duplicated, in a stable order.
	seen := map[string]bool{}
	for _, f := range in.CaptionFields {
		f = strings.TrimSpace(f)
		if validCaptionFields[f] && !seen[f] {
			seen[f] = true
			tv.CaptionFields = append(tv.CaptionFields, f)
		}
	}
	tv.IntervalS = in.IntervalSeconds
	if tv.IntervalS < minIntervalS {
		tv.IntervalS = 300 // sensible default (5 min) for missing/invalid input
	}
	if tv.IntervalS > maxIntervalS {
		tv.IntervalS = maxIntervalS
	}
	return tv, nil
}

// CreateTV registers a new TV.
func (h *Handler) CreateTV(w http.ResponseWriter, r *http.Request) {
	var in tvInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	tv, err := in.normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := h.store.Create(tv)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// UpdateTV changes a TV's settings.
func (h *Handler) UpdateTV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in tvInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	tv, err := in.normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := h.store.Update(id, tv)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DeleteTV stops playback and removes a TV.
func (h *Handler) DeleteTV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = h.mgr.Stop(id)
	err := h.store.Delete(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tvTestInput struct {
	IP    string `json:"ip"`
	Token string `json:"token"`
}

// TestTV probes a TV by IP (no save) and reports what it found, mirroring the
// setup wizard's "test connection" flow.
func (h *Handler) TestTV(w http.ResponseWriter, r *http.Request) {
	var in tvTestInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	ip := strings.TrimSpace(in.IP)
	if ip == "" {
		writeErr(w, http.StatusBadRequest, "ip address is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	res := tv.Probe(ctx, ip, strings.TrimSpace(in.Token))

	out := map[string]any{
		"reachable":      res.Reachable,
		"frameSupported": res.ArtModeSupported,
		"apiVersion":     res.ArtAPIVersion,
		"artModeStatus":  res.ArtModeStatus,
		"token":          res.Token,
		"errors":         res.Errors,
	}
	if res.Device != nil {
		out["model"] = res.Device.Device.ModelName
		out["tvName"] = res.Device.Device.Name
		out["resolution"] = res.Device.Device.Resolution
		out["powerState"] = res.Device.Device.PowerState
	}
	if !res.Reachable {
		writeJSON(w, http.StatusOK, out) // surface details even when unreachable
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- User: list, status, playback control ---

// ListTVs returns each TV with its live status (for the TV dashboard cards).
func (h *Handler) ListTVs(w http.ResponseWriter, r *http.Request) {
	tvs, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(tvs))
	for _, t := range tvs {
		out = append(out, h.statusView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// TVStatus returns the full live status for a single TV.
func (h *Handler) TVStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := h.store.Get(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.statusView(t))
}

type playInput struct {
	PlaylistID string `json:"playlistId"`
}

// PlayTV starts rotating a playlist on a TV.
func (h *Handler) PlayTV(w http.ResponseWriter, r *http.Request) {
	s := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var in playInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(in.PlaylistID) == "" {
		writeErr(w, http.StatusBadRequest, "playlistId is required")
		return
	}
	err := h.mgr.Play(id, s.Username, in.PlaylistID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, _ := h.store.Get(id)
	writeJSON(w, http.StatusOK, h.statusView(t))
}

// ResumeTV continues playback on a TV from its saved position/deck.
func (h *Handler) ResumeTV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.mgr.Resume(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, _ := h.store.Get(id)
	writeJSON(w, http.StatusOK, h.statusView(t))
}

// StopTV halts rotation on a TV.
func (h *Handler) StopTV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.Stop(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	t, err := h.store.Get(id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "tv not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.statusView(t))
}

// SkipTV jumps to the next photo immediately.
func (h *Handler) SkipTV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.Skip(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

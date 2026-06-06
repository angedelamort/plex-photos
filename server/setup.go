package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"plex-photos/auth"
)

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// parseHTTPURL validates a URL is http(s) with a host and returns it trimmed of
// any trailing slash.
func parseHTTPURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("invalid URL")
	}
	return trimmed, nil
}

// Settings keys used to persist Plex configuration in the data dir (DB).
const (
	SettingPlexServerURL = "plex_server_url"
	SettingPlexMachineID = "plex_machine_id"
	SettingPublicBaseURL = "public_base_url"
)

// SettingsStore is the minimal persistence surface the setup wizard needs.
// It is satisfied by *library.Store (GetSetting/SetSetting).
type SettingsStore interface {
	GetSetting(key, fallback string) (string, error)
	SetSetting(key, value string) error
}

// SetupHandler serves the first-run setup wizard: a Plex server URL is entered,
// the machine id is auto-detected from <serverURL>/identity, and the settings
// are persisted and hot-applied without a container restart.
type SetupHandler struct {
	state     *auth.SetupState
	store     SettingsStore
	staticDir string
	// envLocked marks fields provided via environment variables, which are
	// authoritative and must not be overwritten by the wizard.
	envLocked map[string]bool
}

// NewSetupHandler builds a setup handler. envLocked keys (any of the Setting*
// constants) are treated as read-only because they came from env vars.
func NewSetupHandler(state *auth.SetupState, store SettingsStore, staticDir string, envLocked map[string]bool) *SetupHandler {
	if envLocked == nil {
		envLocked = map[string]bool{}
	}
	return &SetupHandler{state: state, store: store, staticDir: staticDir, envLocked: envLocked}
}

// Page serves the setup wizard HTML while unconfigured; once configured it
// redirects to the app root so the page becomes inert.
func (h *SetupHandler) Page(w http.ResponseWriter, r *http.Request) {
	if h.state.Configured() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.ServeFile(w, r, h.staticDir+"/setup.html")
}

// guardActive rejects API calls once setup is complete, so the unauthenticated
// setup endpoints stop functioning after first run.
func (h *SetupHandler) guardActive(w http.ResponseWriter) bool {
	if h.state.Configured() {
		writeJSONErr(w, http.StatusForbidden, "setup already completed")
		return false
	}
	return true
}

// DetectMachineID handles POST /api/setup/identity: given a Plex server URL,
// it contacts <serverURL>/identity and returns the machineIdentifier.
func (h *SetupHandler) DetectMachineID(w http.ResponseWriter, r *http.Request) {
	if !h.guardActive(w) {
		return
	}
	var in struct {
		ServerURL string `json:"serverURL"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	id, err := auth.FetchMachineID(in.ServerURL)
	if err != nil {
		writeJSONErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSONOK(w, map[string]string{"machineId": id})
}

// Save handles POST /api/setup/save: persists Plex settings, hot-applies them
// to the live SetupState, and reports success so the client can redirect.
func (h *SetupHandler) Save(w http.ResponseWriter, r *http.Request) {
	if !h.guardActive(w) {
		return
	}
	var in struct {
		ServerURL     string `json:"serverURL"`
		MachineID     string `json:"machineId"`
		PublicBaseURL string `json:"publicBaseURL"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	in.ServerURL = strings.TrimSpace(in.ServerURL)
	in.MachineID = strings.TrimSpace(in.MachineID)
	in.PublicBaseURL = strings.TrimSpace(in.PublicBaseURL)

	if in.ServerURL == "" || in.MachineID == "" {
		writeJSONErr(w, http.StatusBadRequest, "server URL and machine ID are required")
		return
	}
	if u, err := parseHTTPURL(in.ServerURL); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid server URL")
		return
	} else {
		in.ServerURL = u
	}
	if in.PublicBaseURL != "" {
		if u, err := parseHTTPURL(in.PublicBaseURL); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid public base URL")
			return
		} else {
			in.PublicBaseURL = u
		}
	}

	// Start from current (possibly env-seeded) config so env-locked fields win.
	cur := h.state.Config()
	next := auth.PlexConfig{
		ServerURL:     cur.ServerURL,
		MachineID:     cur.MachineID,
		PublicBaseURL: cur.PublicBaseURL,
	}
	h.applyField(SettingPlexServerURL, in.ServerURL, &next.ServerURL, w)
	h.applyField(SettingPlexMachineID, in.MachineID, &next.MachineID, w)
	if in.PublicBaseURL != "" {
		h.applyField(SettingPublicBaseURL, in.PublicBaseURL, &next.PublicBaseURL, w)
	}

	h.state.Set(next)
	log.Printf("setup: Plex configured (server=%s machine=%s)", next.ServerURL, next.MachineID)
	writeJSONOK(w, map[string]bool{"ok": true})
}

// applyField persists a value unless the field is env-locked, in which case the
// env value (already in dst) is kept authoritative.
func (h *SetupHandler) applyField(key, value string, dst *string, w http.ResponseWriter) {
	if h.envLocked[key] {
		return
	}
	if err := h.store.SetSetting(key, value); err != nil {
		log.Printf("setup: persist %s: %v", key, err)
	}
	*dst = value
}

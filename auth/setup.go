package auth

import (
	"strings"
	"sync"
)

// PlexConfig is the runtime-mutable Plex configuration the provider needs.
type PlexConfig struct {
	ServerURL     string
	MachineID     string
	PublicBaseURL string
}

// Configured reports whether the minimum required Plex settings are present.
func (c PlexConfig) Configured() bool {
	return strings.TrimSpace(c.ServerURL) != "" && strings.TrimSpace(c.MachineID) != ""
}

// SetupState holds the live Plex configuration and whether the app has been
// configured. It is safe for concurrent use: the HTTP handlers read it on every
// request while the setup wizard may update it at runtime (hot apply, no
// restart). When unconfigured, the app serves the first-run setup wizard.
type SetupState struct {
	mu  sync.RWMutex
	cfg PlexConfig
}

// NewSetupState creates state seeded with the given Plex config.
func NewSetupState(cfg PlexConfig) *SetupState {
	return &SetupState{cfg: cfg}
}

// Config returns a snapshot of the current Plex configuration.
func (s *SetupState) Config() PlexConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Configured reports whether Plex is currently configured.
func (s *SetupState) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Configured()
}

// Set replaces the live Plex configuration (used by the setup wizard).
func (s *SetupState) Set(cfg PlexConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

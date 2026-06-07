package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime configuration, loaded from environment variables.
type Config struct {
	PlexServerURL string
	PlexMachineID string
	PublicBaseURL string
	DataPath      string
	SessionSecret string
	Port          string
	ThumbWidth    int
	TimeZone      string
	LogLevel      string

	// AuthProvider selects the authentication backend: "plex" or "mock".
	AuthProvider string

	// CookieSecure sets the Secure flag on session/auth cookies. It must be
	// false when the app is served over plain HTTP (the common self-hosted
	// case: LAN IP, Synology, etc.), because browsers silently drop Secure
	// cookies on non-HTTPS origins — which would break login. Enable it only
	// when the app is reached over HTTPS (e.g. behind a TLS reverse proxy).
	CookieSecure bool

	// Mock provider settings (only used when AuthProvider == "mock").
	MockUser  string
	MockAdmin bool
}

// Load reads configuration from the environment and validates required fields.
func Load() (*Config, error) {
	c := &Config{
		PlexServerURL: os.Getenv("PLEX_SERVER_URL"),
		PlexMachineID: os.Getenv("PLEX_MACHINE_ID"),
		PublicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
		DataPath:      getEnv("DATA_PATH", "/config"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		Port:          getEnv("PORT", "8099"),
		TimeZone:      getEnv("TZ", "UTC"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		AuthProvider:  strings.ToLower(getEnv("AUTH_PROVIDER", "plex")),
		CookieSecure:  getEnvBool("COOKIE_SECURE", false),
		MockUser:      getEnv("MOCK_USER", "dev"),
		MockAdmin:     getEnvBool("MOCK_ADMIN", true),
	}

	width, err := strconv.Atoi(getEnv("THUMB_WIDTH", "400"))
	if err != nil || width <= 0 {
		return nil, fmt.Errorf("invalid THUMB_WIDTH: %q", os.Getenv("THUMB_WIDTH"))
	}
	c.ThumbWidth = width

	if c.AuthProvider != "plex" && c.AuthProvider != "mock" {
		return nil, fmt.Errorf("invalid AUTH_PROVIDER %q (expected \"plex\" or \"mock\")", c.AuthProvider)
	}

	if c.DataPath == "" {
		return nil, fmt.Errorf("DATA_PATH is required")
	}

	// Normalize the data path to absolute. Library roots are picked (and stored)
	// as absolute paths via the admin directory browser, so there is no global
	// photos root to normalize here.
	if abs, err := filepath.Abs(c.DataPath); err == nil {
		c.DataPath = abs
	}

	// SESSION_SECRET is optional: when unset, the app auto-generates and
	// persists one under DataPath on first run (see auth.ResolveSessionSecret),
	// the same way Overseerr/the *arr apps manage their signing keys.

	// Plex settings (PLEX_SERVER_URL / PLEX_MACHINE_ID / PUBLIC_BASE_URL) are no
	// longer required at boot. When absent, the app starts in setup mode and the
	// first-run wizard collects them, persisting to the data dir. Env vars, when
	// present, act as authoritative bootstrap overrides (see main.go wiring).

	return c, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

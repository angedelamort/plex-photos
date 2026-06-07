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
	PhotosPath    string
	DataPath      string
	SessionSecret string
	Port          string
	ThumbWidth    int
	TimeZone      string
	LogLevel      string

	// AuthProvider selects the authentication backend: "plex" or "mock".
	AuthProvider string

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
		PhotosPath:    getEnv("PHOTOS_PATH", "/photos"),
		DataPath:      getEnv("DATA_PATH", "/config"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		Port:          getEnv("PORT", "8099"),
		TimeZone:      getEnv("TZ", "UTC"),
		LogLevel:      getEnv("LOG_LEVEL", "info"),
		AuthProvider:  strings.ToLower(getEnv("AUTH_PROVIDER", "plex")),
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

	if c.PhotosPath == "" {
		return nil, fmt.Errorf("PHOTOS_PATH is required")
	}
	if c.DataPath == "" {
		return nil, fmt.Errorf("DATA_PATH is required")
	}

	// Normalize to absolute paths. Library roots are stored absolute (resolved
	// via filepath.Abs), so the photos root must be absolute too; otherwise
	// computing photo paths relative to it (filepath.Rel) fails and covers /
	// photo listings come back empty. Doing this once here keeps every path
	// comparison absolute-vs-absolute.
	if abs, err := filepath.Abs(c.PhotosPath); err == nil {
		c.PhotosPath = abs
	}
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

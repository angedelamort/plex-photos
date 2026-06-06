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
		PhotosPath:    getEnv("PHOTOS_PATH", "/photos"),
		DataPath:      getEnv("DATA_PATH", "/data"),
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

	if c.AuthProvider == "plex" {
		if c.PlexServerURL == "" {
			return nil, fmt.Errorf("PLEX_SERVER_URL is required when AUTH_PROVIDER=plex")
		}
		if c.PlexMachineID == "" {
			return nil, fmt.Errorf("PLEX_MACHINE_ID is required when AUTH_PROVIDER=plex")
		}
	}

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

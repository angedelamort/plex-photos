package unit

import (
	"path/filepath"
	"testing"

	"plex-photos/config"
)

// clearEnv unsets all config-related env vars for a clean test slate.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PLEX_SERVER_URL", "PLEX_MACHINE_ID", "PUBLIC_BASE_URL", "PHOTOS_PATH", "DATA_PATH",
		"SESSION_SECRET", "PORT", "THUMB_WIDTH", "TZ", "LOG_LEVEL",
		"AUTH_PROVIDER", "MOCK_USER", "MOCK_ADMIN",
	} {
		t.Setenv(k, "")
	}
}

func TestConfigMockDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "mock")
	t.Setenv("PHOTOS_PATH", "/photos")
	t.Setenv("DATA_PATH", "/data")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected mock config to load, got: %v", err)
	}
	if cfg.AuthProvider != "mock" {
		t.Errorf("AuthProvider = %q, want mock", cfg.AuthProvider)
	}
	if cfg.Port != "8099" {
		t.Errorf("Port = %q, want 8099 default", cfg.Port)
	}
	if cfg.ThumbWidth != 400 {
		t.Errorf("ThumbWidth = %d, want 400 default", cfg.ThumbWidth)
	}
}

// TestConfigNormalizesRelativePaths ensures PHOTOS_PATH/DATA_PATH are made
// absolute, since library roots are stored absolute and path math relies on
// both sides being absolute (otherwise covers/photo listings come back empty).
func TestConfigNormalizesRelativePaths(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "mock")
	t.Setenv("PHOTOS_PATH", "./testdata/photos")
	t.Setenv("DATA_PATH", "./testdata/data")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !filepath.IsAbs(cfg.PhotosPath) {
		t.Errorf("PhotosPath = %q, want absolute", cfg.PhotosPath)
	}
	if !filepath.IsAbs(cfg.DataPath) {
		t.Errorf("DataPath = %q, want absolute", cfg.DataPath)
	}
}

// Plex mode no longer requires PLEX_SERVER_URL / PLEX_MACHINE_ID at boot: when
// absent, the app starts in setup mode and the first-run wizard collects them.
func TestConfigPlexLoadsWithoutPlexVars(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "plex")
	t.Setenv("PHOTOS_PATH", "/photos")
	t.Setenv("DATA_PATH", "/data")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected plex config to load without Plex vars (setup mode), got: %v", err)
	}
	if cfg.PlexServerURL != "" || cfg.PlexMachineID != "" {
		t.Errorf("expected empty Plex vars, got server=%q machine=%q", cfg.PlexServerURL, cfg.PlexMachineID)
	}
}

// When Plex env vars are provided they are still read into the config and act
// as authoritative bootstrap overrides.
func TestConfigPlexReadsEnvVars(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "plex")
	t.Setenv("PHOTOS_PATH", "/photos")
	t.Setenv("DATA_PATH", "/data")
	t.Setenv("PLEX_SERVER_URL", "http://plex:32400")
	t.Setenv("PLEX_MACHINE_ID", "abc123")
	t.Setenv("PUBLIC_BASE_URL", "https://photos.example.com")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected plex config to load, got: %v", err)
	}
	if cfg.PlexMachineID != "abc123" {
		t.Errorf("PlexMachineID = %q, want abc123", cfg.PlexMachineID)
	}
	if cfg.PublicBaseURL != "https://photos.example.com" {
		t.Errorf("PublicBaseURL = %q, want the env value", cfg.PublicBaseURL)
	}
}

func TestConfigRejectsBadProvider(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "carrier-pigeon")
	t.Setenv("PHOTOS_PATH", "/photos")
	t.Setenv("DATA_PATH", "/data")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected invalid AUTH_PROVIDER to fail")
	}
}

// SESSION_SECRET is no longer required: it is auto-generated and persisted on
// first run, so plex mode must load fine without it.
func TestConfigPlexLoadsWithoutSessionSecret(t *testing.T) {
	clearEnv(t)
	t.Setenv("AUTH_PROVIDER", "plex")
	t.Setenv("PHOTOS_PATH", "/photos")
	t.Setenv("DATA_PATH", "/data")
	t.Setenv("PLEX_SERVER_URL", "http://plex:32400")
	t.Setenv("PLEX_MACHINE_ID", "abc123")
	if _, err := config.Load(); err != nil {
		t.Fatalf("expected plex mode to load without SESSION_SECRET, got: %v", err)
	}
}

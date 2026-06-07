package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"plex-photos/auth"
	"plex-photos/config"
	"plex-photos/library"
	"plex-photos/server"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := os.MkdirAll(cfg.DataPath, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	// Everything cacheable lives under <DataPath>/cache so the whole data dir
	// (DB + cache) can be mounted as a single volume (arr-style /config).
	thumbCache := filepath.Join(cfg.DataPath, "cache", "thumbs")
	if err := os.MkdirAll(thumbCache, 0o755); err != nil {
		log.Fatalf("create thumb cache dir: %v", err)
	}
	artDir := filepath.Join(cfg.DataPath, "cache", "art")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		log.Fatalf("create art dir: %v", err)
	}

	db, err := library.OpenDB(cfg.DataPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	store := library.NewStore(db)
	scanner := library.NewScanner(db, store, cfg.PhotosPath)
	thumbs := library.NewThumbnailer(cfg.PhotosPath, thumbCache, cfg.ThumbWidth)
	galleryHandler := library.NewHandler(store, scanner, thumbs, cfg.PhotosPath, artDir)
	galleryHandler.SetVersion(version)

	// Plex-like auto-detection of new content: a filesystem watcher over the
	// library roots plus an admin-configurable periodic rescan.
	autoScan := library.NewAutoScanner(store, scanner)
	galleryHandler.SetAutoScanner(autoScan)
	if err := autoScan.Start(); err != nil {
		log.Printf("autoscan: disabled (%v)", err)
	}
	defer autoScan.Stop()

	// Secure cookies require an HTTPS origin; browsers drop Secure cookies on
	// plain HTTP, which silently breaks login. Most self-hosted deployments
	// (LAN IP, Synology) are HTTP, so this defaults off and is opt-in via
	// COOKIE_SECURE=true for HTTPS/reverse-proxy setups. Mock/dev is always off.
	secureCookies := cfg.AuthProvider != "mock" && cfg.CookieSecure
	sessionSecret, err := auth.ResolveSessionSecret(cfg.SessionSecret, cfg.DataPath)
	if err != nil {
		log.Fatalf("session secret: %v", err)
	}
	sessions := auth.NewSessionManager(sessionSecret, secureCookies)
	mw := auth.NewMiddleware(sessions)

	var provider auth.Provider
	var setupHandler *server.SetupHandler
	switch cfg.AuthProvider {
	case "mock":
		provider = auth.NewMockProvider(cfg.MockUser, cfg.MockAdmin)
		log.Printf("auth: mock provider (user=%q admin=%v)", cfg.MockUser, cfg.MockAdmin)
	default:
		// Resolve Plex config with precedence env > DB. Env-provided fields are
		// authoritative and locked against wizard overwrites.
		setupState, envLocked := resolvePlexSetup(cfg, store)
		provider = auth.NewPlexProvider("plex-photos", setupState, secureCookies)
		setupHandler = server.NewSetupHandler(setupState, store, "static", envLocked)
		if setupState.Configured() {
			log.Printf("auth: plex provider (configured)")
		} else {
			log.Printf("auth: plex provider (NOT configured \u2014 first-run setup at /setup)")
		}
	}

	mux := server.NewMux(server.Deps{
		Provider:  provider,
		Sessions:  sessions,
		Mw:        mw,
		Gallery:   galleryHandler,
		StaticDir: "static",
		Setup:     setupHandler,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.LogRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("plex-photos %s listening on :%s", version, cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Printf("shutdown complete")
}

// resolvePlexSetup builds the live Plex SetupState using precedence env > DB.
// It returns the state plus the set of setting keys that were supplied via env
// vars (and are therefore authoritative / not editable by the setup wizard).
func resolvePlexSetup(cfg *config.Config, store *library.Store) (*auth.SetupState, map[string]bool) {
	envLocked := map[string]bool{}

	serverURL := cfg.PlexServerURL
	if serverURL != "" {
		envLocked[server.SettingPlexServerURL] = true
	} else {
		serverURL, _ = store.GetSetting(server.SettingPlexServerURL, "")
	}

	machineID := cfg.PlexMachineID
	if machineID != "" {
		envLocked[server.SettingPlexMachineID] = true
	} else {
		machineID, _ = store.GetSetting(server.SettingPlexMachineID, "")
	}

	publicBase := cfg.PublicBaseURL
	if publicBase != "" {
		envLocked[server.SettingPublicBaseURL] = true
	} else {
		publicBase, _ = store.GetSetting(server.SettingPublicBaseURL, "")
	}
	if publicBase == "" {
		publicBase = "http://localhost:" + cfg.Port
	}

	return auth.NewSetupState(auth.PlexConfig{
		ServerURL:     serverURL,
		MachineID:     machineID,
		PublicBaseURL: publicBase,
	}), envLocked
}


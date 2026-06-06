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

	// Plex-like auto-detection of new content: a filesystem watcher over the
	// library roots plus an admin-configurable periodic rescan.
	autoScan := library.NewAutoScanner(store, scanner)
	galleryHandler.SetAutoScanner(autoScan)
	if err := autoScan.Start(); err != nil {
		log.Printf("autoscan: disabled (%v)", err)
	}
	defer autoScan.Stop()

	// Secure cookies are disabled in mock/dev mode (local HTTP).
	secureCookies := cfg.AuthProvider != "mock"
	sessionSecret, err := auth.ResolveSessionSecret(cfg.SessionSecret, cfg.DataPath)
	if err != nil {
		log.Fatalf("session secret: %v", err)
	}
	sessions := auth.NewSessionManager(sessionSecret, secureCookies)
	mw := auth.NewMiddleware(sessions)

	var provider auth.Provider
	switch cfg.AuthProvider {
	case "mock":
		provider = auth.NewMockProvider(cfg.MockUser, cfg.MockAdmin)
		log.Printf("auth: mock provider (user=%q admin=%v)", cfg.MockUser, cfg.MockAdmin)
	default:
		provider = auth.NewPlexProvider(
			"plex-photos-"+cfg.PlexMachineID,
			"plex-photos",
			cfg.PlexMachineID,
			publicBaseURL(cfg),
			secureCookies,
		)
		log.Printf("auth: plex provider")
	}

	mux := server.NewMux(server.Deps{
		Provider:  provider,
		Sessions:  sessions,
		Mw:        mw,
		Gallery:   galleryHandler,
		StaticDir: "static",
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

func publicBaseURL(cfg *config.Config) string {
	if v := os.Getenv("PUBLIC_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:" + cfg.Port
}


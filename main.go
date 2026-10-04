package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		slog.Error("data dir", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}

	st, err := openStore(cfg.DBPath())
	if err != nil {
		slog.Error("sqlite", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	sources := newSourceSet(&localPhotoSource{root: cfg.PhotosDir})
	storages, err := st.listStorages()
	if err != nil {
		slog.Error("storages", "err", err)
		os.Exit(1)
	}
	sources.useStorages(storages)
	thumbs := newThumbCache(cfg.ThumbDir(), cfg.WallDir(), sources, cfg.ThumbWorkers)
	scanner := newScanner(cfg, st, thumbs, sources)

	key, err := loadSessionKey(cfg.DataDir)
	if err != nil {
		slog.Error("session key", "err", err)
		os.Exit(1)
	}
	gate := newAuthGate(cfg.AuthUser, cfg.AuthPass, key)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	servers := []*http.Server{{
		Addr:              cfg.Listen,
		Handler:           newRouter(st, scanner, thumbs, sources, cfg.TZ, gate),
		ReadHeaderTimeout: 10 * time.Second,
	}}
	if cfg.EventsListen != "" {
		hub := newEventHub(ctx, cfg.EventsToken, scanner)
		servers = append(servers, &http.Server{Addr: cfg.EventsListen, Handler: hub.handler(), ReadHeaderTimeout: 10 * time.Second})
	}

	go scanner.loop(ctx)

	errCh := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			errCh <- srv.ListenAndServe()
		}()
	}
	slog.Info("listen", "addr", cfg.Listen, "events", cfg.EventsListen, "workers", cfg.ThumbWorkers,
		"scanEvery", cfg.ScanEvery.String(), "storages", len(storages), "data", cfg.DataDir)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http", "err", err)
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown", "err", err)
		}
	}
}

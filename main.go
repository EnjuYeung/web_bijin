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

	thumbs := newThumbCache(cfg.ThumbDir(), cfg.PhotosDir)
	scanner := newScanner(cfg, st, thumbs)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go scanner.loop(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newRouter(st, scanner, thumbs, cfg.PhotosDir),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listen", "addr", cfg.Listen, "photos", cfg.PhotosDir, "data", cfg.DataDir)
		errCh <- srv.ListenAndServe()
	}()

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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}

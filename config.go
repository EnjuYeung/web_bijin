package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Listen       string
	PhotosDir    string
	DataDir      string
	ScanEvery    time.Duration
	MaxPixels    int64
	ThumbMaxEdge int
}

func loadConfig() (config, error) {
	cfg := config{
		Listen:       envOr("LISTEN", ":5001"),
		PhotosDir:    envOr("PHOTOS_DIR", "/photos"),
		DataDir:      envOr("DATA_DIR", "/data"),
		ScanEvery:    2 * time.Minute,
		MaxPixels:    64_000_000,
		ThumbMaxEdge: 720,
	}
	if !strings.Contains(cfg.Listen, ":") {
		cfg.Listen = ":" + cfg.Listen
	}
	photos, err := filepath.Abs(cfg.PhotosDir)
	if err != nil {
		return cfg, fmt.Errorf("photos dir: %w", err)
	}
	data, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return cfg, fmt.Errorf("data dir: %w", err)
	}
	cfg.PhotosDir = photos
	cfg.DataDir = data
	if v := strings.TrimSpace(os.Getenv("SCAN_EVERY")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("SCAN_EVERY: %w", err)
		}
		if d < 10*time.Second {
			d = 10 * time.Second
		}
		cfg.ScanEvery = d
	}
	return cfg, nil
}

func (c config) DBPath() string {
	return filepath.Join(c.DataDir, "bijin.db")
}

func (c config) ThumbDir() string {
	return filepath.Join(c.DataDir, "thumbs")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Listen    string
	PhotosDir string
	DataDir   string
	TZ        string
	AuthUser  string
	AuthPass  string
	ScanEvery time.Duration
	MaxPixels int64
	// PhotosHostDir is only shown on the settings page; Compose does the mount.
	PhotosHostDir string
	// ThumbWorkers is how many images are read and decoded at once.
	ThumbWorkers int
	// EventsListen is the storage notification listener (empty: off); it must
	// only be reachable by the storage, and EventsToken authenticates it.
	EventsListen string
	EventsToken  string
}

func loadConfig() (config, error) {
	cfg := config{
		Listen:    envOr("LISTEN", ":5001"),
		PhotosDir: envOr("PHOTOS_DIR", "/photos"),
		DataDir:   envOr("DATA_DIR", "/data"),
		TZ:        envOr("TZ", "Asia/Shanghai"),
		AuthUser:  strings.TrimSpace(os.Getenv("AUTH_USER")),
		AuthPass:  strings.TrimSpace(os.Getenv("AUTH_PASS")),
		ScanEvery: 2 * time.Minute,
		MaxPixels: 64_000_000,

		PhotosHostDir: strings.TrimSpace(os.Getenv("PHOTOS_HOST_DIR")),
		// Decoding one image keeps a core busy; leave the rest for requests.
		ThumbWorkers: max(1, runtime.GOMAXPROCS(0)*2/3),
		EventsListen: strings.TrimSpace(os.Getenv("EVENTS_LISTEN")),
		EventsToken:  strings.TrimSpace(os.Getenv("EVENTS_TOKEN")),
	}
	if _, err := time.LoadLocation(cfg.TZ); err != nil {
		cfg.TZ = "Asia/Shanghai"
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
	if cfg.AuthUser == "" || cfg.AuthPass == "" {
		return cfg, fmt.Errorf("AUTH_USER and AUTH_PASS must be set")
	}
	if v := strings.TrimSpace(os.Getenv("THUMB_WORKERS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 64 {
			return cfg, fmt.Errorf("THUMB_WORKERS must be a number from 1 to 64")
		}
		cfg.ThumbWorkers = n
	}
	if cfg.EventsListen != "" {
		if !strings.Contains(cfg.EventsListen, ":") {
			cfg.EventsListen = ":" + cfg.EventsListen
		}
		if len(cfg.EventsToken) < 16 {
			return cfg, fmt.Errorf("EVENTS_TOKEN must have at least 16 characters when EVENTS_LISTEN is set")
		}
		// New photos arrive by notification; the scan only reconciles.
		cfg.ScanEvery = 30 * time.Minute
	}
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

func (c config) WallDir() string {
	return filepath.Join(c.DataDir, "wallpapers")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

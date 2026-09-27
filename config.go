package main

import (
	"fmt"
	"os"
	"path/filepath"
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

	StorageBackend string
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3Prefix       string
	S3AccessKey    string
	S3SecretKey    string
	S3UseSSL       bool
}

func loadConfig() (config, error) {
	s3UseSSL, err := envBool("S3_USE_SSL", true)
	if err != nil {
		return config{}, err
	}
	cfg := config{
		Listen:    envOr("LISTEN", ":5001"),
		PhotosDir: envOr("PHOTOS_DIR", "/photos"),
		DataDir:   envOr("DATA_DIR", "/data"),
		TZ:        envOr("TZ", "Asia/Shanghai"),
		AuthUser:  strings.TrimSpace(os.Getenv("AUTH_USER")),
		AuthPass:  strings.TrimSpace(os.Getenv("AUTH_PASS")),
		ScanEvery: 2 * time.Minute,
		MaxPixels: 64_000_000,

		StorageBackend: strings.ToLower(envOr("STORAGE_BACKEND", "local")),
		S3Endpoint:     strings.TrimSpace(os.Getenv("S3_ENDPOINT")),
		S3Region:       envOr("S3_REGION", "us-east-1"),
		S3Bucket:       strings.TrimSpace(os.Getenv("S3_BUCKET")),
		S3Prefix:       strings.TrimSpace(os.Getenv("S3_PREFIX")),
		S3AccessKey:    strings.TrimSpace(os.Getenv("S3_ACCESS_KEY")),
		S3SecretKey:    strings.TrimSpace(os.Getenv("S3_SECRET_KEY")),
		S3UseSSL:       s3UseSSL,
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
	switch cfg.StorageBackend {
	case "local":
	case "s3":
		if cfg.S3Endpoint == "" || cfg.S3Bucket == "" || cfg.S3AccessKey == "" || cfg.S3SecretKey == "" {
			return cfg, fmt.Errorf("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY and S3_SECRET_KEY must be set when STORAGE_BACKEND=s3")
		}
	default:
		return cfg, fmt.Errorf("STORAGE_BACKEND must be local or s3")
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

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

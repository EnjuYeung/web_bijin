package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanObjectPrefixAndObjectKey(t *testing.T) {
	prefix, err := cleanObjectPrefix("/albums/2026/")
	if err != nil || prefix != "albums/2026/" {
		t.Fatalf("prefix %q: %v", prefix, err)
	}
	source := &s3PhotoSource{prefix: prefix}
	key, err := source.objectKey("旅行/a.jpg")
	if err != nil || key != "albums/2026/旅行/a.jpg" {
		t.Fatalf("key %q: %v", key, err)
	}
	if _, err := source.objectKey("../secret.jpg"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := cleanObjectPrefix("ok/../bad"); err == nil {
		t.Fatal("expected bad prefix rejection")
	}
}

func TestLoadConfigForS3(t *testing.T) {
	t.Setenv("AUTH_USER", "juen")
	t.Setenv("AUTH_PASS", "secret")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("STORAGE_BACKEND", "s3")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("S3_BUCKET", "photos")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret-key")
	t.Setenv("S3_USE_SSL", "false")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageBackend != "s3" || cfg.S3UseSSL || cfg.S3Region != "us-east-1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if _, err := newPhotoSource(cfg); err != nil {
		t.Fatal(err)
	}
}

type unavailableSource struct{}

func (unavailableSource) Name() string { return "unavailable" }

func (unavailableSource) Walk(context.Context, func(sourceObject) error) error {
	return errors.New("source offline")
}

func (unavailableSource) Open(context.Context, string) (readSeekCloser, error) {
	return nil, errors.New("source offline")
}

func TestScanFailureKeepsExistingIndex(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.upsert(photo{
		RelPath:       "existing.jpg",
		Size:          10,
		MtimeUnix:     time.Now().Unix(),
		Width:         100,
		Height:        100,
		SourceVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	source := unavailableSource{}
	thumbs := newThumbCache(filepath.Join(data, "thumbs"), source)
	scanner := newScanner(config{MaxPixels: 64_000_000}, st, thumbs, source)
	if err := scanner.walk(context.Background()); err == nil {
		t.Fatal("expected scan failure")
	}
	count, err := st.countOK()
	if err != nil || count != 1 {
		t.Fatalf("existing index lost: count=%d err=%v", count, err)
	}
}

type migrationLocalSource struct{ unavailableSource }

func (migrationLocalSource) Name() string { return "local" }

func TestLocalMigrationReusesExistingMetadataAndThumb(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mtime := time.Now().Unix()
	id, err := st.upsert(photo{
		RelPath:   "existing.jpg",
		Size:      10,
		MtimeUnix: mtime,
		Width:     100,
		Height:    80,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := migrationLocalSource{}
	thumbs := newThumbCache(filepath.Join(data, "thumbs"), source)
	if err := os.MkdirAll(thumbs.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbs.path(id), []byte("existing-thumb"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := newScanner(config{MaxPixels: 64_000_000}, st, thumbs, source)
	object := sourceObject{
		Key:     "existing.jpg",
		RelPath: "existing.jpg",
		Size:    10,
		Mtime:   time.Unix(mtime, 0),
		Version: "local:10:123",
		Backend: "local",
	}
	if err := scanner.ingest(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.getByPath("existing.jpg")
	if err != nil || !ok || got.SourceVersion != object.Version {
		t.Fatalf("version not backfilled: %+v ok=%v err=%v", got, ok, err)
	}
	thumb, err := os.ReadFile(thumbs.path(id))
	if err != nil || string(thumb) != "existing-thumb" {
		t.Fatalf("existing thumb changed: %q err=%v", thumb, err)
	}
}

type staticSource struct {
	name    string
	objects []sourceObject
}

func (s staticSource) Name() string { return s.name }

func (s staticSource) Walk(_ context.Context, visit func(sourceObject) error) error {
	for _, object := range s.objects {
		if err := visit(object); err != nil {
			return err
		}
	}
	return nil
}

func (s staticSource) Open(context.Context, string) (readSeekCloser, error) {
	return nil, errors.New("not used")
}

func TestCombinedSourceKeepsLocalAndS3ObjectsWithSameDisplayPath(t *testing.T) {
	localObject := sourceObject{Key: "旅行/a.jpg", RelPath: "旅行/a.jpg", Backend: "local"}
	s3Object := sourceObject{Key: "旅行/a.jpg", RelPath: "旅行/a.jpg", Backend: "s3"}
	source := &combinedPhotoSource{
		local: staticSource{name: "local", objects: []sourceObject{localObject}},
		s3:    staticSource{name: "s3", objects: []sourceObject{s3Object}},
	}
	var got []sourceObject
	if err := source.Walk(context.Background(), func(object sourceObject) error {
		got = append(got, object)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("objects = %d", len(got))
	}
	if got[0].Key != "旅行/a.jpg" || got[1].Key != s3SourceKeyPrefix+"旅行/a.jpg" {
		t.Fatalf("unexpected keys: %q, %q", got[0].Key, got[1].Key)
	}
	if got[0].RelPath != got[1].RelPath {
		t.Fatalf("display paths differ: %q, %q", got[0].RelPath, got[1].RelPath)
	}
}

func TestStoreKeepsLocalAndS3RowsWithSameDisplayPath(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []photo{
		{SourceKey: "旅行/a.jpg", RelPath: "旅行/a.jpg", Width: 100, Height: 80, SourceVersion: "local:v1"},
		{SourceKey: s3SourceKeyPrefix + "旅行/a.jpg", RelPath: "旅行/a.jpg", Width: 100, Height: 80, SourceVersion: "s3:v1"},
	} {
		if _, err := st.upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	photos, err := st.listOK()
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 2 || photos[0].RelPath != "旅行/a.jpg" || photos[1].RelPath != "旅行/a.jpg" {
		t.Fatalf("unexpected photos: %+v", photos)
	}
}

package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateExistingPhotoTableAddsSourceVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bijin.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE photos (
  id INTEGER PRIMARY KEY,
  rel_path TEXT NOT NULL UNIQUE,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  broken INTEGER NOT NULL DEFAULT 0
);
INSERT INTO photos (rel_path, size, mtime_unix, width, height, broken)
VALUES ('old.jpg', 10, 1234, 100, 80, 0);
`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old, ok, err := store.getBySourceKey("old.jpg")
	if err != nil || !ok {
		t.Fatalf("existing row missing: ok=%v err=%v", ok, err)
	}
	if old.SourceVersion != "" {
		t.Fatalf("migrated row version = %q", old.SourceVersion)
	}
	if old.SourceKey != "old.jpg" || old.RelPath != "old.jpg" {
		t.Fatalf("migrated paths = key:%q display:%q", old.SourceKey, old.RelPath)
	}
	old.SourceVersion = "etag-v1"
	if _, err := store.upsert(old); err != nil {
		t.Fatal(err)
	}
	updated, ok, err := store.getBySourceKey("old.jpg")
	if err != nil || !ok || updated.SourceVersion != "etag-v1" {
		t.Fatalf("source version not persisted: %+v ok=%v err=%v", updated, ok, err)
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRelPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := safeRelPath(root, "ok.jpg"); err != nil {
		t.Fatalf("ok.jpg: %v", err)
	}
	if _, err := safeRelPath(root, filepath.Join("sub", "a.jpg")); err != nil {
		t.Fatalf("sub: %v", err)
	}
	if _, err := safeRelPath(root, filepath.Join("..", "etc", "passwd")); err == nil {
		t.Fatal("expected reject of ..")
	}
	if _, err := safeRelPath(root, "/etc/passwd"); err == nil {
		t.Fatal("expected reject of abs")
	}
	if _, err := safeRelPath(root, ""); err == nil {
		t.Fatal("expected reject of empty")
	}
}

func TestIsImageName(t *testing.T) {
	if !isImageName("A.JPG") || !isImageName("x.webp") || !isImageName("x.gif") {
		t.Fatal("expected common image names")
	}
	if isImageName("notes.txt") || isImageName("clip.mp4") || isImageName("noext") {
		t.Fatal("expected non-images rejected")
	}
}

func TestHiddenRel(t *testing.T) {
	if !hiddenRel(".hidden/a.jpg") || !hiddenRel("vis/.skip.jpg") {
		t.Fatal("expected hidden paths")
	}
	if hiddenRel("2024/trip/a.jpg") {
		t.Fatal("visible path marked hidden")
	}
}

func TestFitEdge(t *testing.T) {
	w, h := fitEdge(4000, 3000, 720)
	if w != 720 || h != 540 {
		t.Fatalf("got %dx%d", w, h)
	}
	w, h = fitEdge(100, 80, 720)
	if w != 100 || h != 80 {
		t.Fatalf("no upscale: %dx%d", w, h)
	}
}

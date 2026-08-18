package main

import (
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJPEG(t *testing.T, path string, w, h int, c color.Color) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
}

func TestScanRecursiveAndFilters(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	writeJPEG(t, filepath.Join(root, "wide.jpg"), 400, 200, color.RGBA{200, 80, 40, 255})
	writeJPEG(t, filepath.Join(root, "子目录", "海边 日落.jpg"), 200, 400, color.RGBA{40, 80, 160, 255})
	writeJPEG(t, filepath.Join(root, ".hidden", "no.jpg"), 80, 80, color.RGBA{10, 10, 10, 255})
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.jpg"), []byte("not a jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "misc"), 0o755); err != nil {
		t.Fatal(err)
	}
	pf, err := os.Create(filepath.Join(root, "misc", "sq.png"))
	if err != nil {
		t.Fatal(err)
	}
	_ = png.Encode(pf, image.NewRGBA(image.Rect(0, 0, 120, 120)))
	pf.Close()
	gf, err := os.Create(filepath.Join(root, "misc", "dot.gif"))
	if err != nil {
		t.Fatal(err)
	}
	_ = gif.Encode(gf, image.NewRGBA(image.Rect(0, 0, 10, 20)), nil)
	gf.Close()

	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	th := newThumbCache(filepath.Join(data, "thumbs"), root)
	sc := newScanner(config{
		PhotosDir:    root,
		DataDir:      data,
		ScanEvery:    time.Hour,
		MaxPixels:    64_000_000,
		ThumbMaxEdge: 720,
	}, st, th)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := st.countOK()
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("want 4 visible photos, got %d", n)
	}
	photos, err := st.listOK()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range photos {
		names = append(names, p.RelPath)
		if !th.exists(p.ID) {
			t.Fatalf("missing thumb for %s", p.RelPath)
		}
	}
	joined := strings.Join(names, " | ")
	for _, want := range []string{"wide.jpg", "海边 日落.jpg", "misc/sq.png", "misc/dot.gif"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, names)
		}
	}

	if err := os.Remove(filepath.Join(root, "wide.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, _ = st.countOK()
	if n != 3 {
		t.Fatalf("after delete want 3, got %d", n)
	}
}

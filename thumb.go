package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"

	"github.com/disintegration/imaging"
)

type thumbCache struct {
	dir    string
	photos string
	edge   int
	mu     sync.Mutex
}

func newThumbCache(dir, photos string) *thumbCache {
	return &thumbCache{dir: dir, photos: photos, edge: 720}
}

func (t *thumbCache) path(id int64) string {
	return filepath.Join(t.dir, fmt.Sprintf("%d.jpg", id))
}

func (t *thumbCache) exists(id int64) bool {
	_, err := os.Stat(t.path(id))
	return err == nil
}

func (t *thumbCache) remove(id int64) {
	_ = os.Remove(t.path(id))
}

func (t *thumbCache) ensure(p photo) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return err
	}
	src, err := safeRelPath(t.photos, filepath.FromSlash(p.RelPath))
	if err != nil {
		return err
	}
	img, err := imaging.Open(src, imaging.AutoOrientation(true))
	if err != nil {
		return err
	}
	edge := t.edge
	if edge <= 0 {
		edge = 720
	}
	tw, th := fitEdge(img.Bounds().Dx(), img.Bounds().Dy(), edge)
	if tw < img.Bounds().Dx() || th < img.Bounds().Dy() {
		img = imaging.Resize(img, tw, th, imaging.Lanczos)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return err
	}
	tmp := t.path(p.ID) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path(p.ID))
}

func (t *thumbCache) serveBytes(p photo) ([]byte, error) {
	if !t.exists(p.ID) {
		if err := t.ensure(p); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(t.path(p.ID))
}

func fitEdge(w, h, edge int) (int, int) {
	if w <= 0 || h <= 0 {
		return edge, edge
	}
	if w >= h {
		if w <= edge {
			return w, h
		}
		return edge, max(1, h*edge/w)
	}
	if h <= edge {
		return w, h
	}
	return max(1, w*edge/h), edge
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/disintegration/imaging"
)

type thumbCache struct {
	dir    string
	source imageOpener
	edge   int
	gate   chan struct{}
}

func newThumbCache(dir string, source imageOpener) *thumbCache {
	return &thumbCache{dir: dir, source: source, edge: 720, gate: make(chan struct{}, 1)}
}

const imageReadTimeout = 45 * time.Second

// A bad image is only confirmed after its entire source was read successfully.
type invalidImageError struct{ err error }

func (e *invalidImageError) Error() string { return e.err.Error() }
func (e *invalidImageError) Unwrap() error { return e.err }

func (t *thumbCache) lock(ctx context.Context) error {
	select {
	case t.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *thumbCache) unlock() { <-t.gate }

func (t *thumbCache) path(p photo) string {
	return filepath.Join(t.dir, fmt.Sprintf("%d-%s.jpg", p.ID, photoVersion(p)))
}
func (t *thumbCache) exists(p photo) bool {
	info, err := os.Stat(t.path(p))
	return err == nil && info.Mode().IsRegular()
}
func (t *thumbCache) remove(id int64) {
	paths, _ := filepath.Glob(filepath.Join(t.dir, fmt.Sprintf("%d-*.jpg", id)))
	paths = append(paths, filepath.Join(t.dir, fmt.Sprintf("%d.jpg", id)))
	for _, path := range paths {
		_ = os.Remove(path)
	}
}
func (t *thumbCache) removeOld(p, previous photo) {
	if previous.ID == 0 {
		return
	}
	if t.path(previous) != t.path(p) {
		_ = os.Remove(t.path(previous))
	}
	_ = os.Remove(filepath.Join(t.dir, fmt.Sprintf("%d.jpg", p.ID)))
}

// Caller holds the serial generation gate. Spooling to disk keeps remote I/O
// errors separate from invalid image bytes without retaining whole originals.
func (t *thumbCache) prepare(ctx context.Context, key string, maxPixels int64) ([]byte, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return nil, 0, 0, err
	}
	f, err := os.CreateTemp(t.dir, ".source-*")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	src, err := t.source.Open(ctx, key)
	if err != nil {
		return nil, 0, 0, err
	}
	_, err = io.Copy(f, src)
	closeErr := src.Close()
	if err != nil {
		return nil, 0, 0, err
	}
	if closeErr != nil {
		return nil, 0, 0, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, 0, 0, classifyDecodeError(err)
	}
	if maxPixels <= 0 {
		maxPixels = 64_000_000
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > maxPixels/int64(cfg.Height) {
		return nil, 0, 0, &invalidImageError{errTooManyPixels}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	img, err := imaging.Decode(f, imaging.AutoOrientation(true))
	if err != nil {
		return nil, 0, 0, classifyDecodeError(err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	tw, th := fitEdge(w, h, t.edge)
	if tw < w || th < h {
		img = imaging.Resize(img, tw, th, imaging.Lanczos)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, 0, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), w, h, nil
}
func classifyDecodeError(err error) error {
	var ioErr *os.PathError
	if errors.As(err, &ioErr) {
		return err
	}
	return &invalidImageError{err}
}
func (t *thumbCache) save(p photo, b []byte) error {
	tmp := t.path(p) + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path(p))
}
func (t *thumbCache) ensure(ctx context.Context, p photo) error {
	if err := t.lock(ctx); err != nil {
		return err
	}
	defer t.unlock()
	if t.exists(p) {
		return nil
	}
	b, _, _, err := t.prepare(ctx, p.sourceKey(), 64_000_000)
	if err != nil {
		return err
	}
	return t.save(p, b)
}
func (t *thumbCache) serveBytes(ctx context.Context, p photo) ([]byte, error) {
	if !t.exists(p) {
		if err := t.ensure(ctx, p); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(t.path(p))
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

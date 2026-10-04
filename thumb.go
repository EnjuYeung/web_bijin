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
	"sync"
	"time"

	"github.com/disintegration/imaging"
)

type thumbCache struct {
	dir string
	// wallDir holds the wallpapers, made from the same read as the thumbnail.
	wallDir string
	source  imageOpener
	edge    int
	// gate bounds how many images are read and decoded at once; scanning,
	// storage events and missing-thumbnail requests share it.
	gate chan struct{}

	keysMu sync.Mutex
	keys   map[string]*keyLock
}

type keyLock struct {
	sync.Mutex
	users int
}

func newThumbCache(dir, wallDir string, source imageOpener, workers int) *thumbCache {
	return &thumbCache{dir: dir, wallDir: wallDir, source: source, edge: 720, gate: make(chan struct{}, max(1, workers)), keys: map[string]*keyLock{}}
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

// lockKey serializes work on one index key, so a scan, an event and a page
// request for the same photo read it once; different photos run in parallel.
func (t *thumbCache) lockKey(key string) (unlock func()) {
	t.keysMu.Lock()
	l := t.keys[key]
	if l == nil {
		l = &keyLock{}
		t.keys[key] = l
	}
	l.users++
	t.keysMu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		t.keysMu.Lock()
		if l.users--; l.users == 0 {
			delete(t.keys, key)
		}
		t.keysMu.Unlock()
	}
}

func (t *thumbCache) path(p photo) string {
	return filepath.Join(t.dir, fmt.Sprintf("%d-%s.jpg", p.ID, photoVersion(p)))
}
func (t *thumbCache) exists(p photo) bool {
	info, err := os.Stat(t.path(p))
	return err == nil && info.Mode().IsRegular()
}

// remove deletes every cached file of a photo: thumbnails and wallpapers.
func (t *thumbCache) remove(id int64) {
	paths, _ := filepath.Glob(filepath.Join(t.dir, fmt.Sprintf("%d-*.jpg", id)))
	paths = append(paths, filepath.Join(t.dir, fmt.Sprintf("%d.jpg", id)))
	walls, _ := filepath.Glob(filepath.Join(t.wallDir, fmt.Sprintf("%d-*.webp", id)))
	for _, path := range append(paths, walls...) {
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

// decoded is one fully read, valid and upright source image with its
// thumbnail. The image stays available for the wallpapers.
type decoded struct {
	img   image.Image
	thumb []byte
	w, h  int
}

// Caller holds a generation slot. Spooling to disk keeps remote I/O errors
// separate from invalid image bytes without retaining whole originals.
func (t *thumbCache) prepare(ctx context.Context, key string, maxPixels int64) (decoded, error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return decoded{}, err
	}
	f, err := os.CreateTemp(t.dir, ".source-*")
	if err != nil {
		return decoded{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	src, err := t.source.Open(ctx, key)
	if err != nil {
		return decoded{}, err
	}
	_, err = io.Copy(f, src)
	closeErr := src.Close()
	if err != nil {
		return decoded{}, err
	}
	if closeErr != nil {
		return decoded{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return decoded{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return decoded{}, err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return decoded{}, classifyDecodeError(err)
	}
	if maxPixels <= 0 {
		maxPixels = 64_000_000
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > maxPixels/int64(cfg.Height) {
		return decoded{}, &invalidImageError{errTooManyPixels}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return decoded{}, err
	}
	img, err := imaging.Decode(f, imaging.AutoOrientation(true))
	if err != nil {
		return decoded{}, classifyDecodeError(err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	thumb := img
	tw, th := fitEdge(w, h, t.edge)
	if tw < w || th < h {
		thumb = imaging.Resize(img, tw, th, imaging.Lanczos)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return decoded{}, err
	}
	if err := ctx.Err(); err != nil {
		return decoded{}, err
	}
	return decoded{img: img, thumb: buf.Bytes(), w: w, h: h}, nil
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
	defer t.lockKey(p.sourceKey())()
	if t.exists(p) {
		return nil
	}
	if err := t.lock(ctx); err != nil {
		return err
	}
	defer t.unlock()
	d, err := t.prepare(ctx, p.sourceKey(), 64_000_000)
	if err != nil {
		return err
	}
	return t.save(p, d.thumb)
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

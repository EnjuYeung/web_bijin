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
	"golang.org/x/sync/semaphore"
)

type thumbCache struct {
	dir string
	// wallDir holds the wallpapers, made from the same read as the thumbnail.
	wallDir string
	source  imageOpener
	edge    int
	// Separate limits bound spooled files and in-memory generation. Scanning,
	// storage events and missing-thumbnail requests share both limits.
	spool      *semaphore.Weighted
	generation *semaphore.Weighted
	workers    int
}

func newThumbCache(dir, wallDir string, source imageOpener, workers int) *thumbCache {
	workers = max(1, workers)
	return &thumbCache{dir: dir, wallDir: wallDir, source: source, edge: 720,
		spool: semaphore.NewWeighted(int64(workers)), generation: semaphore.NewWeighted(int64(workers)), workers: workers}
}

const imageReadTimeout = 45 * time.Second

// A bad image is only confirmed after its entire source was read successfully.
type invalidImageError struct{ err error }

func (e *invalidImageError) Error() string { return e.err.Error() }
func (e *invalidImageError) Unwrap() error { return e.err }

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
}

// decoded is one fully read, valid and upright source image with its
// thumbnail. The image stays available for the wallpapers.
type decoded struct {
	img   image.Image
	thumb []byte
	w, h  int
}

// Caller holds a spool slot. Spooling to disk keeps remote I/O errors
// separate from invalid image bytes without retaining whole originals.
func (t *thumbCache) readVersion(ctx context.Context, object sourceObject) (file *os.File, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(t.dir, ".source-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	var src readSeekCloser
	var errOpen error
	if source, ok := t.source.(interface {
		OpenVersion(context.Context, sourceObject) (readSeekCloser, error)
	}); ok && object.Version != "" {
		src, errOpen = source.OpenVersion(ctx, object)
	} else {
		src, errOpen = t.source.Open(ctx, object.Key)
	}
	err = errOpen
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(f, src)
	closeErr := src.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return f, nil
}

func (t *thumbCache) decodeVersion(ctx context.Context, f *os.File) (decoded, error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return decoded{}, err
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

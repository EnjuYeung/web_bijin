package main

import (
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

var imageExt = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".webp": {},
	".gif":  {},
}

type scanState struct {
	Scanning bool      `json:"scanning"`
	LastAt   time.Time `json:"lastAt"`
	LastErr  string    `json:"lastErr,omitempty"`
	Seen     int       `json:"seen"`
	Ready    int       `json:"ready"`
}

type scanner struct {
	cfg    config
	store  *store
	thumbs *thumbCache

	mu    sync.Mutex
	state scanState
}

func newScanner(cfg config, store *store, thumbs *thumbCache) *scanner {
	return &scanner{cfg: cfg, store: store, thumbs: thumbs}
}

func (s *scanner) snapshot() scanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	if n, err := s.store.countOK(); err == nil {
		st.Ready = n
	}
	return st
}

func (s *scanner) loop(ctx context.Context) {
	s.run(ctx)
	t := time.NewTicker(s.cfg.ScanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.run(ctx)
		}
	}
}

func (s *scanner) run(ctx context.Context) {
	s.mu.Lock()
	if s.state.Scanning {
		s.mu.Unlock()
		return
	}
	s.state.Scanning = true
	s.state.LastErr = ""
	s.mu.Unlock()

	err := s.walk(ctx)

	s.mu.Lock()
	s.state.Scanning = false
	s.state.LastAt = time.Now()
	if err != nil {
		s.state.LastErr = err.Error()
		slog.Error("scan", "err", err)
	}
	s.mu.Unlock()
}

func (s *scanner) walk(ctx context.Context) error {
	root := s.cfg.PhotosDir
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		slog.Warn("photos dir missing or not a directory", "dir", root, "err", err)
		gone, delErr := s.store.deleteMissing(map[string]struct{}{})
		if delErr != nil {
			return delErr
		}
		for _, p := range gone {
			s.thumbs.remove(p.ID)
		}
		s.mu.Lock()
		s.state.Seen = 0
		s.mu.Unlock()
		return nil
	}

	keep := make(map[string]struct{})
	seen := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			slog.Warn("walk", "path", path, "err", walkErr)
			return nil
		}
		name := d.Name()
		if name != "." && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isImageName(name) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if hiddenRel(rel) {
			return nil
		}
		keep[rel] = struct{}{}
		seen++
		if err := s.ingest(path, rel); err != nil {
			slog.Warn("ingest", "path", rel, "err", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	gone, err := s.store.deleteMissing(keep)
	if err != nil {
		return err
	}
	for _, p := range gone {
		s.thumbs.remove(p.ID)
	}

	s.mu.Lock()
	s.state.Seen = seen
	s.mu.Unlock()
	slog.Info("scan done", "files", seen, "removed", len(gone))
	return nil
}

func (s *scanner) ingest(absPath, rel string) error {
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	mtime := info.ModTime().Unix()
	size := info.Size()

	existing, ok, err := s.store.getByPath(rel)
	if err != nil {
		return err
	}
	unchanged := ok && existing.Size == size && existing.MtimeUnix == mtime && !existing.Broken && existing.Width > 0
	if unchanged {
		if !s.thumbs.exists(existing.ID) {
			if err := s.thumbs.ensure(existing); err != nil {
				slog.Warn("thumb", "id", existing.ID, "err", err)
			}
		}
		return nil
	}

	w, h, decErr := imageSize(absPath, s.cfg.MaxPixels)
	p := photo{
		RelPath:   rel,
		Size:      size,
		MtimeUnix: mtime,
		Width:     w,
		Height:    h,
		Broken:    decErr != nil || w == 0 || h == 0,
	}
	id, err := s.store.upsert(p)
	if err != nil {
		return err
	}
	p.ID = id
	if p.Broken {
		slog.Warn("skip broken image", "path", rel, "err", decErr)
		s.thumbs.remove(id)
		return nil
	}
	if err := s.thumbs.ensure(p); err != nil {
		slog.Warn("thumb", "id", id, "path", rel, "err", err)
	}
	return nil
}

func isImageName(name string) bool {
	_, ok := imageExt[strings.ToLower(filepath.Ext(name))]
	return ok
}

func hiddenRel(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func imageSize(path string, maxPixels int64) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return 0, 0, errTooManyPixels
	}
	w, h := cfg.Width, cfg.Height
	if rot := exifSwap(path); rot {
		w, h = h, w
	}
	return w, h, nil
}

var errTooManyPixels = errString("image too many pixels")

type errString string

func (e errString) Error() string { return string(e) }

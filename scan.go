package main

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
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
	source photoSource

	mu    sync.Mutex
	state scanState
}

func newScanner(cfg config, store *store, thumbs *thumbCache, source photoSource) *scanner {
	return &scanner{cfg: cfg, store: store, thumbs: thumbs, source: source}
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
	keep := make(map[string]struct{})
	seen := 0
	err := s.source.Walk(ctx, func(object sourceObject) error {
		keep[object.RelPath] = struct{}{}
		seen++
		if err := s.ingest(ctx, object); err != nil {
			slog.Warn("ingest", "path", object.RelPath, "err", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s source: %w", s.source.Name(), err)
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

func (s *scanner) ingest(ctx context.Context, object sourceObject) error {
	existing, ok, err := s.store.getByPath(object.RelPath)
	if err != nil {
		return err
	}
	if ok && s.source.Name() == "local" && existing.SourceVersion == "" &&
		existing.Size == object.Size && existing.MtimeUnix == object.Mtime.Unix() &&
		!existing.Broken && existing.Width > 0 && existing.Height > 0 {
		existing.SourceVersion = object.Version
		if _, err := s.store.upsert(existing); err != nil {
			return err
		}
		if !s.thumbs.exists(existing.ID) {
			if err := s.thumbs.ensure(ctx, existing); err != nil {
				slog.Warn("thumb", "id", existing.ID, "err", err)
			}
		}
		return nil
	}
	unchanged := ok && existing.SourceVersion == object.Version && !existing.Broken && existing.Width > 0
	if unchanged {
		if !s.thumbs.exists(existing.ID) {
			if err := s.thumbs.ensure(ctx, existing); err != nil {
				slog.Warn("thumb", "id", existing.ID, "err", err)
			}
		}
		return nil
	}

	w, h, decErr := imageSize(ctx, s.source, object.RelPath, s.cfg.MaxPixels)
	p := photo{
		RelPath:       object.RelPath,
		Size:          object.Size,
		MtimeUnix:     object.Mtime.Unix(),
		Width:         w,
		Height:        h,
		Broken:        decErr != nil || w == 0 || h == 0,
		SourceVersion: object.Version,
	}
	id, err := s.store.upsert(p)
	if err != nil {
		return err
	}
	p.ID = id
	if p.Broken {
		slog.Warn("skip broken image", "path", object.RelPath, "err", decErr)
		s.thumbs.remove(id)
		return nil
	}
	if err := s.thumbs.ensure(ctx, p); err != nil {
		slog.Warn("thumb", "id", id, "path", object.RelPath, "err", err)
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

func imageSize(ctx context.Context, source photoSource, rel string, maxPixels int64) (int, int, error) {
	f, err := source.Open(ctx, rel)
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
	orientationReader, err := source.Open(ctx, rel)
	if err == nil {
		if orientation, readErr := readOrientation(orientationReader); readErr == nil && orientation >= 5 && orientation <= 8 {
			w, h = h, w
		}
		_ = orientationReader.Close()
	}
	return w, h, nil
}

var errTooManyPixels = errString("image too many pixels")

type errString string

func (e errString) Error() string { return string(e) }

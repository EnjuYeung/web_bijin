package main

import (
	"context"
	"errors"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
	Failed   int       `json:"failed"`
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
	s.state.Failed = 0
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
	failures := 0
	var lastFailure error
	err := s.source.Walk(ctx, func(object sourceObject) error {
		keep[object.Key] = struct{}{}
		seen++
		if err := s.ingest(ctx, object); err != nil {
			failures++
			lastFailure = err
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
	s.state.Failed = failures
	s.mu.Unlock()
	slog.Info("scan done", "files", seen, "removed", len(gone), "failed", failures)
	if failures > 0 {
		return fmt.Errorf("%d image(s) failed: %w", failures, lastFailure)
	}
	return nil
}

func (s *scanner) ingest(ctx context.Context, object sourceObject) error {
	if err := s.thumbs.lock(ctx); err != nil {
		return err
	}
	defer s.thumbs.unlock()
	existing, ok, err := s.store.getBySourceKey(object.Key)
	if err != nil {
		return err
	}
	if ok && existing.SourceVersion == object.Version && (existing.Broken || s.thumbs.exists(existing)) {
		return nil
	}
	// Older local indexes had no source version; retain their working cache.
	if ok && object.Backend == "local" && existing.SourceVersion == "" &&
		existing.Size == object.Size && existing.MtimeUnix == object.Mtime.Unix() &&
		!existing.Broken && existing.Width > 0 && existing.Height > 0 {
		legacy := filepath.Join(s.thumbs.dir, fmt.Sprintf("%d.jpg", existing.ID))
		if _, err := os.Stat(legacy); err == nil {
			existing.SourceVersion = object.Version
			if err := os.Rename(legacy, s.thumbs.path(existing)); err != nil {
				return err
			}
			_, err := s.store.upsert(existing)
			return err
		}
	}
	p := photo{ID: existing.ID, SourceKey: object.Key, RelPath: object.RelPath,
		Size: object.Size, MtimeUnix: object.Mtime.Unix(), SourceVersion: object.Version}
	b, w, h, processErr := s.thumbs.prepare(ctx, object.Key, s.cfg.MaxPixels)
	if processErr != nil {
		var invalid *invalidImageError
		if !errors.As(processErr, &invalid) {
			return processErr
		}
		p.Broken = true
		if _, err := s.store.upsert(p); err != nil {
			return err
		}
		s.thumbs.remove(p.ID)
		return processErr
	}
	p.Width, p.Height = w, h
	if !ok {
		// Reserve a stable ID, but never expose an image before the file is saved.
		pending := p
		pending.Broken, pending.SourceVersion = true, ""
		id, err := s.store.upsert(pending)
		if err != nil {
			return err
		}
		p.ID = id
	}
	if err := s.thumbs.save(p, b); err != nil {
		return err
	}
	if _, err := s.store.upsert(p); err != nil {
		return err
	}
	s.thumbs.removeOld(p, existing)
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

var errTooManyPixels = errors.New("image too many pixels")

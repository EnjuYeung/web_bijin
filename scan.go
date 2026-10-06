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
	Queued   bool      `json:"queued"`
	LastAt   time.Time `json:"lastAt"`
	LastErr  string    `json:"lastErr,omitempty"`
	Seen     int       `json:"seen"`
	Failed   int       `json:"failed"`
	Ready    int       `json:"ready"`
}

// sourceStatus is the latest listing result of one source.
type sourceStatus struct {
	At   time.Time `json:"at"`
	Seen int       `json:"seen"`
	Err  string    `json:"err,omitempty"`
}

type scanner struct {
	cfg     config
	store   *store
	thumbs  *thumbCache
	sources *sourceSet
	wake    chan struct{}

	mu        sync.Mutex
	state     scanState
	perSource map[string]sourceStatus
	events    eventState
}

func newScanner(cfg config, store *store, thumbs *thumbCache, sources *sourceSet) *scanner {
	return &scanner{cfg: cfg, store: store, thumbs: thumbs, sources: sources, wake: make(chan struct{}, 1)}
}

// trigger asks the loop for a scan as soon as the current one, if any, ends.
func (s *scanner) trigger() {
	s.mu.Lock()
	s.state.Queued = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *scanner) sourceStatuses() map[string]sourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]sourceStatus, len(s.perSource))
	for id, st := range s.perSource {
		out[id] = st
	}
	return out
}

func (s *scanner) snapshot() scanState {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
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
		case <-s.wake:
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
	s.state.Queued = false
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

// walk lists every source in turn while new or changed images are processed
// on up to as many goroutines as there are generation slots. A source that
// cannot be listed keeps its existing photos and does not stop the others.
func (s *scanner) walk(ctx context.Context) error {
	// Photos added by storage events while this scan runs are newer than the
	// listing and must survive the cleanup below.
	before, err := s.store.maxID()
	if err != nil {
		return err
	}
	keep := make(map[string]struct{})
	failedSources := make(map[string]bool)
	statuses := make(map[string]sourceStatus)
	var walkErrs []error
	seen := 0
	var (
		wg          sync.WaitGroup
		failMu      sync.Mutex
		failures    int
		lastFailure error
	)
	busy := make(chan struct{}, cap(s.thumbs.gate))
	for _, src := range s.sources.all() {
		n := 0
		err := src.Src.Walk(ctx, func(object sourceObject) error {
			object.Key = src.Prefix + object.RelPath
			keep[object.Key] = struct{}{}
			seen++
			n++
			select {
			case busy <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			wg.Add(1)
			go func() {
				defer func() { <-busy; wg.Done() }()
				if err := s.ingest(ctx, object); err != nil {
					failMu.Lock()
					failures++
					lastFailure = err
					failMu.Unlock()
					slog.Warn("ingest", "source", src.ID, "path", object.RelPath, "err", err)
				}
			}()
			return nil
		})
		if ctx.Err() != nil {
			wg.Wait()
			return ctx.Err()
		}
		st := sourceStatus{At: time.Now(), Seen: n}
		if err != nil {
			failedSources[src.ID] = true
			st.Err = describeSourceError(err)
			walkErrs = append(walkErrs, fmt.Errorf("walk %s (%s): %w", src.ID, src.Name, err))
			slog.Error("scan source", "source", src.ID, "name", src.Name, "err", err)
		}
		statuses[src.ID] = st
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	s.perSource = statuses
	s.mu.Unlock()

	gone, err := s.store.deleteMissing(keep, failedSources, before)
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
	slog.Info("scan done", "files", seen, "removed", len(gone), "failed", failures, "sourceErrors", len(walkErrs))
	if len(walkErrs) > 0 {
		return errors.Join(walkErrs...)
	}
	if failures > 0 {
		return fmt.Errorf("%d image(s) failed: %w", failures, lastFailure)
	}
	return nil
}

// ingest indexes one listed or notified object. The per-key lock makes the
// version check and the commit atomic for that photo; the generation slot is
// only taken when the image really has to be read.
func (s *scanner) ingest(ctx context.Context, object sourceObject) error {
	defer s.thumbs.lockKey(object.Key)()
	existing, ok, err := s.store.getBySourceKey(object.Key)
	if err != nil {
		return err
	}
	if ok && existing.SourceVersion == object.Version && (existing.Broken || s.thumbs.exists(existing)) {
		ready, err := s.wallpapersReady(existing)
		if err != nil || ready {
			return err
		}
		// The photo is current and only its wallpapers are missing, as for
		// photos indexed before wallpapers existed.
		if err := s.thumbs.lock(ctx); err != nil {
			return err
		}
		defer s.thumbs.unlock()
		d, err := s.thumbs.prepare(ctx, object.Key, s.cfg.MaxPixels)
		if err != nil {
			return err
		}
		return s.saveWallpapers(ctx, existing, d.img)
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
	if err := s.thumbs.lock(ctx); err != nil {
		return err
	}
	defer s.thumbs.unlock()
	p := photo{ID: existing.ID, SourceKey: object.Key, RelPath: object.RelPath,
		Size: object.Size, MtimeUnix: object.Mtime.Unix(), SourceVersion: object.Version}
	d, processErr := s.thumbs.prepare(ctx, object.Key, s.cfg.MaxPixels)
	if processErr != nil {
		var invalid *invalidImageError
		if !errors.As(processErr, &invalid) {
			return processErr
		}
		p.Broken = true
		if _, err := s.store.upsert(p); err != nil {
			return err
		}
		if err := s.store.deleteWallpapers(p.ID); err != nil {
			return err
		}
		s.thumbs.remove(p.ID)
		return processErr
	}
	p.Width, p.Height = d.w, d.h
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
	if err := s.thumbs.save(p, d.thumb); err != nil {
		return err
	}
	if _, err := s.store.upsert(p); err != nil {
		return err
	}
	s.thumbs.removeOld(p, existing)
	// The album shows the photo from here on; encoding its wallpapers takes
	// a few seconds more.
	return s.saveWallpapers(ctx, p, d.img)
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

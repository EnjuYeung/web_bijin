package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// photoResult describes this attempt's version, never a previous usable photo.
// A known bad version has Invalid=true and Err=nil: scans don't count it again.
type photoResult struct {
	Version         string
	AlbumUsable     bool
	WallpapersReady bool
	Invalid         bool
	Missing         bool
	Retryable       bool
	Err             error
}

var errSourceChanged = errors.New("photo source changed during processing")

// photoProcessor owns all per-photo sequencing. Callers schedule work, but
// never acquire its locks or inspect files to decide whether processing finished.
type photoProcessor struct {
	store     *store
	thumbs    *thumbCache
	sources   *sourceSet
	maxPixels int64
	workers   int
	mu        sync.Mutex
	keys      map[string]*photoLock
}

type photoLock struct {
	gate  chan struct{}
	users int
}

func newPhotoProcessor(st *store, thumbs *thumbCache, sources *sourceSet, maxPixels int64) *photoProcessor {
	return &photoProcessor{store: st, thumbs: thumbs, sources: sources, maxPixels: maxPixels,
		workers: cap(thumbs.gate), keys: make(map[string]*photoLock)}
}

func (p *photoProcessor) lock(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	l := p.keys[key]
	if l == nil {
		l = &photoLock{gate: make(chan struct{}, 1)}
		p.keys[key] = l
	}
	l.users++
	p.mu.Unlock()
	releaseRef := func() {
		p.mu.Lock()
		l.users--
		if l.users == 0 {
			delete(p.keys, key)
		}
		p.mu.Unlock()
	}
	select {
	case l.gate <- struct{}{}:
		unlock := func() { <-l.gate; releaseRef() }
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

func processingFailure(version string, err error) photoResult {
	return photoResult{Version: version, Retryable: true, Err: err}
}

// ApplyListed reuses scan metadata. Unchanged, complete versions need no Stat.
// Before reading, metadata is checked again after every resource wait.
func (p *photoProcessor) ApplyListed(ctx context.Context, object sourceObject) photoResult {
	unlock, err := p.lock(ctx, object.Key)
	if err != nil {
		return processingFailure(object.Version, err)
	}
	defer unlock()
	return p.process(ctx, object, true)
}

// SyncKey resolves the current source state inside the photo lock, including
// deletion. It never trusts the version or operation in a notification.
func (p *photoProcessor) SyncKey(ctx context.Context, key string) photoResult {
	unlock, err := p.lock(ctx, key)
	if err != nil {
		return processingFailure("", err)
	}
	defer unlock()
	return p.sync(ctx, key, true)
}

func (p *photoProcessor) sync(ctx context.Context, key string, wallpapers bool) photoResult {
	object, err := p.sources.Stat(ctx, key)
	if isNotFound(err) {
		existing, ok, lookupErr := p.store.getBySourceKey(key)
		if lookupErr != nil {
			return processingFailure("", lookupErr)
		}
		if ok {
			if err := p.remove(existing); err != nil {
				return processingFailure("", err)
			}
		}
		return photoResult{Missing: true}
	}
	if err != nil {
		return processingFailure("", err)
	}
	return p.process(ctx, object, wallpapers)
}

func (p *photoProcessor) validate(ctx context.Context, object sourceObject) error {
	current, err := p.sources.Stat(ctx, object.Key)
	if err != nil {
		return err
	}
	if current.Version != object.Version {
		return errSourceChanged
	}
	return ctx.Err()
}

// process holds the photo lock. Thumbnail-only requests follow the same
// validation and publication rules but leave wallpaper work to scans/events.
func (p *photoProcessor) process(ctx context.Context, object sourceObject, wallpapers bool) photoResult {
	result := photoResult{Version: object.Version}
	fail := func(err error) photoResult {
		result.Err, result.Retryable = err, true
		return result
	}
	existing, ok, err := p.store.getBySourceKey(object.Key)
	if err != nil {
		return fail(err)
	}
	same := ok && existing.SourceVersion == object.Version
	if same && existing.Broken {
		result.Invalid = true
		return result
	}
	cached := same && existing.Width > 0 && existing.Height > 0 && p.thumbs.exists(existing)
	if cached {
		result.AlbumUsable = true
		result.WallpapersReady, err = p.wallpapersReady(existing)
		if err != nil {
			return fail(err)
		}
		if !wallpapers || result.WallpapersReady {
			return result
		}
	}
	// Preserve working thumbnails made by older local indexes. Wall backfill
	// uses the normal path on the next pass, without rewriting this thumbnail.
	if ok && object.Backend == "local" && existing.SourceVersion == "" &&
		existing.Size == object.Size && existing.MtimeUnix == object.Mtime.Unix() &&
		!existing.Broken && existing.Width > 0 && existing.Height > 0 {
		legacy := filepath.Join(p.thumbs.dir, fmt.Sprintf("%d.jpg", existing.ID))
		if _, err := os.Stat(legacy); err == nil {
			if err := p.validate(ctx, object); err != nil {
				return fail(err)
			}
			existing.SourceVersion = object.Version
			if err := os.Rename(legacy, p.thumbs.path(existing)); err != nil {
				return fail(err)
			}
			if _, err := p.store.upsert(existing); err != nil {
				return fail(err)
			}
			result.AlbumUsable = true
			result.WallpapersReady, err = p.wallpapersReady(existing)
			if err != nil {
				return fail(err)
			}
			result.Retryable = !result.WallpapersReady
			return result
		}
	}
	if err := p.thumbs.lock(ctx); err != nil {
		return fail(err)
	}
	defer p.thumbs.unlock()
	if err := p.validate(ctx, object); err != nil {
		return fail(err)
	}
	d, processErr := p.thumbs.prepareVersion(ctx, object, p.maxPixels)
	// Check successful reads and confirmed decode failures alike: obsolete
	// bytes must not replace a usable photo or mark the new version as broken.
	if err := p.validate(ctx, object); err != nil {
		return fail(err)
	}
	target := photo{ID: existing.ID, SourceKey: object.Key, RelPath: object.RelPath,
		Size: object.Size, MtimeUnix: object.Mtime.Unix(), SourceVersion: object.Version}
	if processErr != nil {
		var invalid *invalidImageError
		if !errors.As(processErr, &invalid) {
			return fail(processErr)
		}
		target.Broken = true
		id, err := p.store.upsert(target)
		if err != nil {
			return fail(err)
		}
		target.ID = id
		if err := p.store.deleteWallpapers(id); err != nil {
			return fail(err)
		}
		p.thumbs.remove(id)
		result.AlbumUsable, result.WallpapersReady = false, false
		result.Invalid, result.Err = true, processErr
		return result
	}
	target.Width, target.Height = d.w, d.h
	if !cached {
		if !ok {
			pending := target
			pending.Broken, pending.SourceVersion = true, ""
			id, err := p.store.upsert(pending)
			if err != nil {
				return fail(err)
			}
			target.ID = id
		}
		if err := p.thumbs.save(target, d.thumb); err != nil {
			return fail(err)
		}
		if err := p.validate(ctx, object); err != nil {
			_ = os.Remove(p.thumbs.path(target))
			return fail(err)
		}
		if _, err := p.store.upsert(target); err != nil {
			return fail(err)
		}
		p.thumbs.removeOld(target, existing)
	} else {
		target = existing
	}
	// This publication precedes wallpaper encoding; cached reads need no lock.
	result.AlbumUsable = true
	result.WallpapersReady, err = p.wallpapersReady(target)
	if err != nil {
		return fail(err)
	}
	if wallpapers && !result.WallpapersReady {
		if err := p.saveWallpapers(ctx, target, d.img); err != nil {
			return fail(err)
		}
		result.WallpapersReady = true
	}
	return result
}

type photoThumbnail struct {
	Photo       photo
	Bytes       []byte
	NotModified bool
}

func thumbnailETag(p photo) string { return fmt.Sprintf(`"%d-%s"`, p.ID, photoVersion(p)) }

// Thumbnail returns metadata and bytes of the same indexed version. Its fast
// path also works while that version's wallpapers are still being encoded.
func (p *photoProcessor) Thumbnail(ctx context.Context, id int64, ifNoneMatch string) (photoThumbnail, error) {
	current, ok, err := p.store.getByID(id)
	if err != nil {
		return photoThumbnail{}, err
	}
	if !ok || current.Broken {
		return photoThumbnail{}, os.ErrNotExist
	}
	if ifNoneMatch == thumbnailETag(current) {
		return photoThumbnail{Photo: current, NotModified: true}, nil
	}
	if b, err := os.ReadFile(p.thumbs.path(current)); err == nil {
		return photoThumbnail{Photo: current, Bytes: b}, nil
	}
	unlock, err := p.lock(ctx, current.sourceKey())
	if err != nil {
		return photoThumbnail{}, err
	}
	defer unlock()
	// Another request may have repaired or replaced the photo while we waited.
	current, ok, err = p.store.getByID(id)
	if err != nil {
		return photoThumbnail{}, err
	}
	if !ok || current.Broken {
		return photoThumbnail{}, os.ErrNotExist
	}
	if b, err := os.ReadFile(p.thumbs.path(current)); err == nil {
		return photoThumbnail{Photo: current, Bytes: b}, nil
	}
	result := p.sync(ctx, current.sourceKey(), false)
	if result.Err != nil {
		return photoThumbnail{}, result.Err
	}
	if !result.AlbumUsable {
		return photoThumbnail{}, os.ErrNotExist
	}
	current, ok, err = p.store.getByID(id)
	if err != nil {
		return photoThumbnail{}, err
	}
	if !ok || current.Broken {
		return photoThumbnail{}, os.ErrNotExist
	}
	b, err := os.ReadFile(p.thumbs.path(current))
	return photoThumbnail{Photo: current, Bytes: b}, err
}

// RemoveIfUnchanged only removes the candidate's exact indexed version and
// only when its source is still absent. Replaced sources and failed reads stay.
func (p *photoProcessor) RemoveIfUnchanged(ctx context.Context, expected photo) (bool, error) {
	unlock, err := p.lock(ctx, expected.sourceKey())
	if err != nil {
		return false, err
	}
	defer unlock()
	current, ok, err := p.store.getBySourceKey(expected.sourceKey())
	if err != nil || !ok {
		return false, err
	}
	if current.ID != expected.ID || current.SourceVersion != expected.SourceVersion {
		return false, nil
	}
	_, err = p.sources.Stat(ctx, current.sourceKey())
	if err == nil {
		return false, nil
	}
	if !isNotFound(err) && !errors.Is(err, errSourceNotConfigured) {
		return false, err
	}
	if err := p.remove(current); err != nil {
		return false, err
	}
	return true, nil
}

func (p *photoProcessor) remove(current photo) error {
	if err := p.store.deleteByID(current.ID); err != nil {
		return err
	}
	p.thumbs.remove(current.ID)
	return nil
}

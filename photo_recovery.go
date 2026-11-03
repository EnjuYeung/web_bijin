package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
)

type photoRecoveryEntry struct {
	Key     string
	Version string
}

// The entry must still identify the same rejected object in both the index and
// the source. Normal scanning owns changes, removals and new versions.
func (p *photoProcessor) retryRejected(ctx context.Context, entry photoRecoveryEntry) (photoResult, bool) {
	unlock, err := p.lock(ctx, entry.Key)
	if err != nil {
		return processingFailure(entry.Version, err), true
	}
	defer unlock()
	indexed, ok, err := p.store.getBySourceKey(entry.Key)
	if err != nil {
		return processingFailure(entry.Version, err), true
	}
	if !ok || !indexed.Broken || indexed.SourceVersion != entry.Version {
		return photoResult{}, false
	}
	object, err := p.sources.Stat(ctx, entry.Key)
	if isNotFound(err) || (err == nil && object.Version != entry.Version) {
		return photoResult{}, false
	}
	if err != nil {
		return processingFailure(entry.Version, err), true
	}
	return p.process(ctx, object, true, true), true
}

func (p *photoProcessor) recoverRejected(ctx context.Context, manifest string) {
	raw, err := os.ReadFile(manifest)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Error("read photo recovery manifest", "err", err)
		return
	}
	var entries []photoRecoveryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		slog.Error("parse photo recovery manifest", "err", err)
		return
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Key == "" || entry.Version == "" || seen[entry.Key] {
			slog.Error("invalid photo recovery manifest entry", "key", entry.Key)
			return
		}
		seen[entry.Key] = true
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		result, attempted := p.retryRejected(ctx, entry)
		switch {
		case !attempted:
			slog.Info("photo recovery skipped", "key", entry.Key)
		case result.Err != nil:
			slog.Warn("photo recovery failed", "key", entry.Key, "retryable", result.Retryable, "err", result.Err)
		default:
			slog.Info("photo recovery complete", "key", entry.Key, "albumUsable", result.AlbumUsable, "wallpapersReady", result.WallpapersReady)
		}
	}
}

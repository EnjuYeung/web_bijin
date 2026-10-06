package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// Values are immutable after publication. A build never holds mu while waiting
// for SQLite, and a writer invalidates by advancing a revision without waiting
// for a build. A changed revision cannot publish an old snapshot as current.
type revisionCache[T any] struct {
	mu       sync.RWMutex
	buildMu  sync.Mutex
	revision uint64
	ready    bool
	value    T
	builds   atomic.Uint64
}

func (c *revisionCache[T]) get(revision *atomic.Uint64, load func(uint64) (T, error)) (T, error) {
	current := revision.Load()
	c.mu.RLock()
	value, hit := c.value, c.ready && c.revision == current
	c.mu.RUnlock()
	if hit {
		return value, nil
	}
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	current = revision.Load()
	c.mu.RLock()
	value, hit = c.value, c.ready && c.revision == current
	c.mu.RUnlock()
	if hit {
		return value, nil
	}
	c.builds.Add(1)
	value, err := load(current)
	if err != nil {
		var zero T
		return zero, err
	}
	if revision.Load() == current {
		c.mu.Lock()
		c.value, c.revision, c.ready = value, current, true
		c.mu.Unlock()
	}
	return value, nil
}

type cacheLimits struct {
	entries int
	bytes   int64
	idle    time.Duration
}

var photoOrderLimits = cacheLimits{8, 32 << 20, 30 * time.Minute}
var wallFilterLimits = cacheLimits{16, 16 << 20, 30 * time.Minute}

type cacheEntry[V any] struct {
	value V
	bytes int64
	used  time.Time
}

// The count and byte limits include retained keys and values. Builds are
// serialized, so cache misses cannot allocate many full-size arrays at once.
type boundedCache[K comparable, V any] struct {
	mu      sync.Mutex
	buildMu sync.Mutex
	entries map[K]cacheEntry[V]
	bytes   int64
	builds  atomic.Uint64
}

func (c *boundedCache[K, V]) lookup(key K, limits cacheLimits) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, entry := range c.entries {
		if now.Sub(entry.used) >= limits.idle {
			delete(c.entries, k)
			c.bytes -= entry.bytes
		}
	}
	entry, ok := c.entries[key]
	if ok {
		entry.used = now
		c.entries[key] = entry
	}
	return entry.value, ok
}

func (c *boundedCache[K, V]) get(key K, limits cacheLimits, load func() (V, error), size func(V) int64) (V, error) {
	if value, ok := c.lookup(key, limits); ok {
		return value, nil
	}
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	if value, ok := c.lookup(key, limits); ok {
		return value, nil
	}
	c.builds.Add(1)
	value, err := load()
	if err != nil {
		return value, err
	}
	cost := size(value)
	if cost > limits.bytes {
		return value, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[K]cacheEntry[V])
	}
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.bytes
	}
	c.entries[key] = cacheEntry[V]{value: value, bytes: cost, used: time.Now()}
	c.bytes += cost
	for len(c.entries) > limits.entries || c.bytes > limits.bytes {
		var oldest K
		var at time.Time
		for k, entry := range c.entries {
			if at.IsZero() || entry.used.Before(at) {
				oldest, at = k, entry.used
			}
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
	return value, nil
}

type queryCaches struct {
	indexRevision  atomic.Uint64
	sourceRevision atomic.Uint64
	albumRevision  atomic.Uint64
	wallRevision   atomic.Uint64
	peopleRevision atomic.Uint64

	index       revisionCache[*photoIndex]
	owners      revisionCache[map[string]ownerCount]
	albums      revisionCache[[]albumSummary]
	people      revisionCache[map[string]albumPeople]
	walls       revisionCache[*wallIndex]
	wallStats   revisionCache[wallStats]
	orders      boundedCache[photoOrderKey, []rankedPhoto]
	wallFilters boundedCache[wallSelectionKey, []int]
}

func photoVisible(p photo) bool {
	return !p.Broken && p.Width > 0 && p.Height > 0
}

// Call after each committed photo write, including partially completed
// operations. Pending uploads do not change the visible ID set.
func (s *store) photoChanged(before, after photo) {
	s.cache.sourceRevision.Add(1)
	oldVisible, newVisible := photoVisible(before), photoVisible(after)
	if oldVisible != newVisible || (oldVisible && newVisible && photoAlbumID(before.RelPath) != photoAlbumID(after.RelPath)) {
		s.cache.indexRevision.Add(1)
	}
	if oldVisible || newVisible {
		s.cache.albumRevision.Add(1)
	}
	s.cache.wallRevision.Add(1)
}

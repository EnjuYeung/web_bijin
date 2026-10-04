package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Storage notifications arrive as RustFS / MinIO-style webhooks on a listener
// that only the storage can reach. Measured on RustFS 1.0: a failed delivery
// is retried every ~5 s, one that gets no answer is abandoned after ~30 s, and
// newer events wait behind it meanwhile. So a delivery is answered at once and
// the photo is synced in the background; the periodic scan reconciles anything
// lost on the way, such as events sent while this process was down.

// eventState is what the settings page shows about notifications.
type eventState struct {
	Enabled  bool      `json:"enabled"`
	Received int       `json:"received"`
	LastAt   time.Time `json:"lastAt"`
	LastErr  string    `json:"lastErr,omitempty"`
}

func (s *scanner) eventSnapshot() eventState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

// syncObject applies the storage's current state of one key. The storage is
// asked again instead of trusting the event, so repeated, late or reordered
// notifications all end in the same index.
func (s *scanner) syncObject(ctx context.Context, t eventTarget) error {
	object, err := t.src.Stat(ctx, t.rel)
	if isNotFound(err) {
		return s.removeKey(t.key)
	}
	if err != nil {
		return err
	}
	object.Key = t.key
	return s.ingest(ctx, object)
}

func (s *scanner) removeKey(key string) error {
	defer s.thumbs.lockKey(key)()
	p, ok, err := s.store.getBySourceKey(key)
	if err != nil || !ok {
		return err
	}
	if err := s.store.deleteByID(p.ID); err != nil {
		return err
	}
	s.thumbs.remove(p.ID)
	return nil
}

type eventHub struct {
	ctx     context.Context
	token   string
	scanner *scanner
	retry   time.Duration

	mu      sync.Mutex
	pending map[string]bool // key being synced -> notified again meanwhile
	wg      sync.WaitGroup
}

func newEventHub(ctx context.Context, token string, sc *scanner) *eventHub {
	sc.mu.Lock()
	sc.events.Enabled = true
	sc.mu.Unlock()
	return &eventHub{ctx: ctx, token: token, scanner: sc, retry: 15 * time.Second, pending: map[string]bool{}}
}

func (h *eventHub) handler() http.Handler {
	mux := http.NewServeMux()
	// The sender health-checks the root of the endpoint's origin.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /s3-events", h.receive)
	return mux
}

type s3Notification struct {
	Records []struct {
		EventName string `json:"eventName"`
		S3        struct {
			Bucket struct {
				Name string `json:"name"`
			} `json:"bucket"`
			Object struct {
				Key string `json:"key"`
			} `json:"object"`
		} `json:"s3"`
	} `json:"Records"`
}

func (h *eventHub) receive(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var n s3Notification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&n); err != nil {
		// Still answer 200: the sender would resend an unreadable body forever.
		slog.Warn("storage event unreadable", "err", err)
		h.setErr(err)
		w.WriteHeader(http.StatusOK)
		return
	}
	for _, rec := range n.Records {
		if !strings.HasPrefix(rec.EventName, "s3:ObjectCreated:") && !strings.HasPrefix(rec.EventName, "s3:ObjectRemoved:") {
			continue
		}
		// Keys are form-encoded: "+" is a space and "%2B" a plus.
		key, err := url.QueryUnescape(rec.S3.Object.Key)
		if err != nil {
			slog.Warn("storage event key", "key", rec.S3.Object.Key, "err", err)
			continue
		}
		for _, t := range h.scanner.sources.match(rec.S3.Bucket.Name, key) {
			h.schedule(t)
		}
	}
	h.scanner.mu.Lock()
	h.scanner.events.Received++
	h.scanner.events.LastAt = time.Now()
	h.scanner.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// schedule syncs one key in the background. Events for a key that is being
// synced fold into one more pass after the current one.
func (h *eventHub) schedule(t eventTarget) {
	h.mu.Lock()
	if _, busy := h.pending[t.key]; busy {
		h.pending[t.key] = true
		h.mu.Unlock()
		return
	}
	h.pending[t.key] = false
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			h.sync(t)
			h.mu.Lock()
			again := h.pending[t.key]
			if again {
				h.pending[t.key] = false
			} else {
				delete(h.pending, t.key)
			}
			h.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}

// sync retries temporary failures twice before leaving them to the next scan.
func (h *eventHub) sync(t eventTarget) {
	for attempt := 1; ; attempt++ {
		start := time.Now()
		err := h.scanner.syncObject(h.ctx, t)
		var invalid *invalidImageError
		switch {
		case err == nil:
			slog.Info("storage event", "source", t.source, "path", t.rel, "ms", time.Since(start).Milliseconds())
			h.setErr(nil)
			return
		case h.ctx.Err() != nil:
			return
		case errors.As(err, &invalid):
			// Recorded as unreadable; only a new version is tried again.
			slog.Warn("storage event", "source", t.source, "path", t.rel, "err", err)
			return
		case attempt == 3:
			slog.Warn("storage event left for the next scan", "source", t.source, "path", t.rel, "err", err)
			h.setErr(err)
			return
		}
		select {
		case <-time.After(h.retry):
		case <-h.ctx.Done():
			return
		}
	}
}

func (h *eventHub) setErr(err error) {
	h.scanner.mu.Lock()
	defer h.scanner.mu.Unlock()
	h.scanner.events.LastErr = ""
	if err != nil {
		h.scanner.events.LastErr = describeSourceError(err)
	}
}

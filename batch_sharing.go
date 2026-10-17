package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// batchAPI runs batch sharing by album: one task at a time, inside this
// process, one photo after another. Records and their SQL are in
// batch_store.go; permits are written through share_store.go.
type batchAPI struct {
	share  *shareAPI
	mu     sync.Mutex // Lifecycle and item commit share this lock; cancellation cannot race a commit.
	cancel context.CancelFunc
	now    func() time.Time
}

func newBatchAPI(share *shareAPI) *batchAPI {
	a := &batchAPI{share: share, now: time.Now}
	// Persisted permits/results survive; only this process owns a live executor.
	if err := share.store.interruptRunningBatches(a.now()); err != nil {
		panic(err)
	}
	return a
}

func (a *batchAPI) routes(mux *http.ServeMux, gate *authGate) {
	mux.Handle("GET /api/share-batches", gate.protect(http.HandlerFunc(a.list)))
	mux.Handle("GET /api/share-batches/{id}", gate.protect(http.HandlerFunc(a.get)))
	mux.Handle("GET /api/share-batches/{id}/links", gate.protect(http.HandlerFunc(a.links)))
	mux.Handle("POST /api/share-batches/prepare", gate.protectWrite("请在相册里管理批量分享", true, a.prepare))
	mux.Handle("POST /api/share-batches/{id}/start", gate.protectWrite("请在相册里管理批量分享", true, a.start))
	mux.Handle("POST /api/share-batches/{id}/retry", gate.protectWrite("请在相册里管理批量分享", true, a.retry))
	mux.Handle("POST /api/share-batches/{id}/cancel", gate.protectWrite("请在相册里管理批量分享", true, a.stop))
}

func batchBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "批量请求不完整"})
		return false
	}
	return true
}

func (a *batchAPI) answer(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Cache-Control", "private, no-store")
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务已过期或不存在"})
	case errors.Is(err, errBatchConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		slog.Error("batch sharing", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "批量操作暂未完成，请重试核对"})
	}
}

func (a *batchAPI) prepare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Albums []string `json:"albums"`
		Mode   string   `json:"mode"`
	}
	if !batchBody(w, r, &in) {
		return
	}
	if (in.Mode != "open" && in.Mode != "stop") || len(in.Albums) == 0 || len(in.Albums) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择 1–500 本相册和操作"})
		return
	}
	seen := map[string]bool{}
	albums := []string{}
	for _, id := range in.Albums {
		if v, ok := validAlbumID(id); !ok || v != id || len(id) > 4096 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "相册范围无效"})
			return
		}
		if !seen[id] {
			seen[id] = true
			albums = append(albums, id)
		}
	}
	sort.Strings(albums)
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		a.answer(w, nil, err)
		return
	}
	id := base64.RawURLEncoding.EncodeToString(token[:])
	st := a.share.store
	a.mu.Lock()
	defer a.mu.Unlock()
	err := st.cleanupBatches(a.now())
	if err == nil {
		err = st.createBatch(id, in.Mode, albums, a.now())
	}
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	b, err := st.readBatch(id, batchAll)
	a.answer(w, b, err)
}

// get is polled every two seconds while a task runs, so it only reads.
func (a *batchAPI) get(w http.ResponseWriter, r *http.Request) {
	b, err := a.readLive(r.PathValue("id"), batchView)
	a.answer(w, b, err)
}

// readLive is a task as the page may see it: an expired record is gone even
// before cleanupBatches deletes it.
func (a *batchAPI) readLive(id string, items batchItems) (shareBatch, error) {
	b, err := a.share.store.readBatch(id, items)
	if err == nil && batchExpired(b, a.now()) {
		return shareBatch{}, sql.ErrNoRows
	}
	return b, err
}

func (a *batchAPI) list(w http.ResponseWriter, r *http.Request) {
	st := a.share.store
	a.mu.Lock()
	err := st.cleanupBatches(a.now())
	a.mu.Unlock()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	tasks, err := st.listBatches()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	a.answer(w, map[string]any{"tasks": tasks}, nil)
}

func (a *batchAPI) start(w http.ResponseWriter, r *http.Request) { a.launch(w, r, false) }
func (a *batchAPI) retry(w http.ResponseWriter, r *http.Request) { a.launch(w, r, true) }

func (a *batchAPI) launch(w http.ResponseWriter, r *http.Request, retry bool) {
	var in struct {
		Revision *int64 `json:"revision"`
	}
	if !batchBody(w, r, &in) {
		return
	}
	if in.Revision == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请重新核对任务"})
		return
	}
	id := r.PathValue("id")
	st := a.share.store
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := st.cleanupBatches(a.now()); err != nil {
		a.answer(w, nil, err)
		return
	}
	started, attempt, err := st.beginBatchAttempt(id, *in.Revision, retry)
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	if started {
		if a.cancel != nil {
			a.cancel() // the previous attempt has ended; release its context
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		go a.run(ctx, id, attempt)
	}
	b, err := st.readBatch(id, batchView)
	a.answer(w, b, err)
}

func (a *batchAPI) stop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision *int64 `json:"revision"`
	}
	if !batchBody(w, r, &in) {
		return
	}
	if in.Revision == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请重新核对任务"})
		return
	}
	id := r.PathValue("id")
	st := a.share.store
	a.mu.Lock()
	defer a.mu.Unlock()
	cancelled, err := st.cancelBatch(id, *in.Revision, a.now())
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	if cancelled && a.cancel != nil {
		a.cancel()
	}
	b, err := a.readLive(id, batchView)
	a.answer(w, b, err)
}

func (a *batchAPI) run(ctx context.Context, id string, revision int64) {
	st := a.share.store
	defer func() {
		if p := recover(); p != nil {
			slog.Error("batch panic", "err", p)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := st.interruptBatch(id, revision, a.now()); err != nil {
			slog.Error("batch interrupted", "err", err)
		}
	}()
	for ctx.Err() == nil {
		a.mu.Lock()
		mode, it, done, err := st.nextBatchItem(id, revision, a.now())
		a.mu.Unlock()
		if err != nil {
			slog.Error("batch read", "err", err)
			return
		}
		if done {
			return
		}
		fresh, itemErr := a.prepareItem(ctx, mode, it)
		if ctx.Err() != nil {
			return
		}
		if err := a.commitItem(id, revision, mode, it, fresh, itemErr); err != nil {
			slog.Error("batch item", "err", err)
			return
		}
	}
}

// prepareItem checks a photo against the one chosen for the task and, to open
// a share, verifies its original in a snapshot like a single copy does.
func (a *batchAPI) prepareItem(ctx context.Context, mode string, it batchItem) (photoShare, error) {
	st := a.share.store
	p, ok, err := st.getByID(it.ID)
	if err != nil {
		return photoShare{}, err
	}
	if !ok || !photoVisible(p) || photoShareVersion(p) != photoShareVersion(it.Photo) {
		return photoShare{}, errSourceChanged
	}
	current, err := st.shareForPhoto(it.ID)
	if err != nil {
		return photoShare{}, err
	}
	if current.Generation != it.Generation {
		return photoShare{}, errShareStateChanged
	}
	if mode != "open" {
		return photoShare{}, nil
	}
	release, err := a.share.owner.acquire(ctx)
	if err != nil {
		return photoShare{}, err
	}
	snap, err := a.share.snapshot(ctx, it.Photo)
	release()
	if err != nil {
		return photoShare{}, err
	}
	snap.file.Close()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return photoShare{}, err
	}
	return photoShare{Token: base64.RawURLEncoding.EncodeToString(token[:]), SourceKey: it.Photo.sourceKey(), SourceVersion: it.Photo.SourceVersion,
		Scope: snap.scope, Digest: snap.digest, MIME: snap.mime, Extension: snap.extension, Size: snap.size}, nil
}

func (a *batchAPI) commitItem(id string, revision int64, mode string, it batchItem, fresh photoShare, itemErr error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.share.store.commitBatchItem(id, revision, mode, it, fresh, itemErr)
}

// links are the task's addresses that still work. The sources are asked a few
// at a time, so a task of thousands of photos answers within the proxy timeout.
func (a *batchAPI) links(w http.ResponseWriter, r *http.Request) {
	st := a.share.store
	b, err := a.readLive(r.PathValue("id"), batchAll)
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	type candidate struct {
		photo photo
		share photoShare
	}
	var candidates []candidate
	for _, it := range b.Items {
		if it.Status != "success" || it.Path == "" {
			continue
		}
		p, ok, err := st.getByID(it.ID)
		if err != nil {
			a.answer(w, nil, err)
			return
		}
		if !ok || photoShareVersion(p) != photoShareVersion(it.Photo) {
			continue
		}
		sh, err := st.shareForPhoto(it.ID)
		if err != nil {
			a.answer(w, nil, err)
			return
		}
		if sh.path() == it.Path {
			candidates = append(candidates, candidate{p, sh})
		}
	}
	current := make([]bool, len(candidates))
	turns := make(slots, 8)
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := turns.acquire(r.Context())
			if err != nil {
				return
			}
			defer release()
			current[i] = a.share.checkSource(r.Context(), c.photo, c.share) == sourceCurrent
		}()
	}
	wg.Wait()
	paths := []string{}
	for i, c := range candidates {
		if current[i] {
			paths = append(paths, c.share.path())
		}
	}
	a.answer(w, map[string]any{"paths": paths, "excluded": b.Succeeded - len(paths)}, nil)
}

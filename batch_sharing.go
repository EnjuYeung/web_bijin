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
	"strings"
	"sync"
	"time"
)

const batchRetention = 48 * time.Hour

var errBatchConflict = errors.New("任务或图片状态已改变，请重新核对")

type batchAPI struct {
	share  *shareAPI
	mu     sync.Mutex // Lifecycle and item commit share this lock; cancellation cannot race a commit.
	cancel context.CancelFunc
	now    func() time.Time
}
type batchItem struct {
	Ordinal    int    `json:"-"`
	Photo      photo  `json:"-"`
	Generation int64  `json:"-"`
	ID         int64  `json:"id"`
	Album      string `json:"album"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Path       string `json:"path,omitempty"`
}
type shareBatch struct {
	ID        string      `json:"id"`
	Mode      string      `json:"mode"`
	Albums    []string    `json:"albums"`
	Status    string      `json:"status"`
	Revision  int64       `json:"revision"`
	Created   int64       `json:"created"`
	Finished  int64       `json:"finished"`
	Total     int         `json:"total"`
	Shared    int         `json:"shared"`
	Changes   int         `json:"changes"`
	Done      int         `json:"done"`
	Succeeded int         `json:"succeeded"`
	Failed    int         `json:"failed"`
	Items     []batchItem `json:"items"`
}

func (s *store) migrateBatches() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS share_batches (
 id TEXT PRIMARY KEY, mode TEXT NOT NULL, albums TEXT NOT NULL, status TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, finished INTEGER NOT NULL DEFAULT 0,
 total INTEGER NOT NULL, shared INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS share_batch_items (
 batch_id TEXT NOT NULL, ordinal INTEGER NOT NULL, photo TEXT NOT NULL, generation INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(batch_id, ordinal)
);
CREATE INDEX IF NOT EXISTS share_batch_pending ON share_batch_items(batch_id, status, ordinal);
`)
	return err
}
func newBatchAPI(share *shareAPI) *batchAPI {
	a := &batchAPI{share: share, now: time.Now}
	// Persisted permits/results survive; only this process owns a live executor.
	if _, err := share.store.db.Exec("UPDATE share_batches SET status='interrupted', finished=? WHERE status='running'", a.now().Unix()); err != nil {
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
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "批量请求不完整"})
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "批量请求不完整"})
		return false
	}
	return true
}
func (a *batchAPI) answer(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Cache-Control", "private, no-store")
	if err != nil {
		code, message := 500, "批量操作暂未完成，请重试核对"
		if errors.Is(err, sql.ErrNoRows) {
			code, message = 404, "任务已过期或不存在"
		} else if errors.Is(err, errBatchConflict) {
			code, message = 409, err.Error()
		}
		if code == 500 {
			slog.Error("batch sharing", "err", err)
		}
		writeJSON(w, code, map[string]string{"error": message})
		return
	}
	writeJSON(w, 200, v)
}
func (a *batchAPI) cleanup() error {
	tx, err := a.share.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`DELETE FROM share_batches WHERE (finished>0 AND finished<?) OR (status='prepared' AND created<?)
 OR id IN (SELECT id FROM share_batches WHERE status='prepared' ORDER BY created DESC LIMIT -1 OFFSET 16)`, a.now().Add(-batchRetention).Unix(), a.now().Add(-10*time.Minute).Unix())
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM share_batch_items WHERE batch_id NOT IN (SELECT id FROM share_batches)"); err != nil {
		return err
	}
	return tx.Commit()
}
func loadBatch(tx *sql.Tx, id string, details bool) (shareBatch, error) {
	b := shareBatch{Items: []batchItem{}}
	var albums string
	err := tx.QueryRow(`SELECT id,mode,albums,status,revision,created,finished,total,shared FROM share_batches WHERE id=?`, id).Scan(&b.ID, &b.Mode, &albums, &b.Status, &b.Revision, &b.Created, &b.Finished, &b.Total, &b.Shared)
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal([]byte(albums), &b.Albums); err != nil {
		return b, err
	}
	b.Changes = b.Total - b.Shared
	if b.Mode == "stop" {
		b.Changes = b.Shared
	}
	if err = tx.QueryRow("SELECT COALESCE(SUM(status='success'),0),COALESCE(SUM(status='failed'),0) FROM share_batch_items WHERE batch_id=?", id).Scan(&b.Succeeded, &b.Failed); err != nil {
		return b, err
	}
	b.Done = b.Succeeded + b.Failed
	if !details {
		b.Items = nil
		return b, nil
	}
	rows, err := tx.Query("SELECT ordinal,photo,generation,status,error,path FROM share_batch_items WHERE batch_id=? ORDER BY ordinal", id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var it batchItem
		var raw string
		if err = rows.Scan(&it.Ordinal, &raw, &it.Generation, &it.Status, &it.Error, &it.Path); err != nil {
			return b, err
		}
		if err = json.Unmarshal([]byte(raw), &it.Photo); err != nil {
			return b, err
		}
		it.ID = it.Photo.ID
		it.Name = it.Photo.RelPath
		it.Album = photoAlbumID(it.Photo.RelPath)
		b.Items = append(b.Items, it)
	}
	b.Done = b.Succeeded + b.Failed
	return b, rows.Err()
}
func (a *batchAPI) read(id string) (shareBatch, error) {
	tx, err := a.share.store.db.Begin()
	if err != nil {
		return shareBatch{}, err
	}
	defer tx.Rollback()
	return loadBatch(tx, id, true)
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
		writeJSON(w, 400, map[string]string{"error": "请选择 1–500 本相册和操作"})
		return
	}
	seen := map[string]bool{}
	albums := []string{}
	for _, id := range in.Albums {
		if v, ok := validAlbumID(id); !ok || v != id || len(id) > 4096 {
			writeJSON(w, 400, map[string]string{"error": "相册范围无效"})
			return
		}
		if !seen[id] {
			seen[id] = true
			albums = append(albums, id)
		}
	}
	sort.Strings(albums)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.cleanup(); err != nil {
		a.answer(w, nil, err)
		return
	}
	tx, err := a.share.store.db.Begin()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	defer tx.Rollback()
	var token [24]byte
	if _, err = rand.Read(token[:]); err != nil {
		a.answer(w, nil, err)
		return
	}
	id := base64.RawURLEncoding.EncodeToString(token[:])
	args := make([]any, len(albums))
	holders := make([]string, len(albums))
	for i, s := range albums {
		args[i] = s
		holders[i] = "?"
	}
	rows, err := tx.Query(`SELECT p.id,p.rel_path,p.display_path,p.size,p.mtime_unix,p.width,p.height,p.broken,p.source_version,
 ps.generation, CASE WHEN ps.token IS NOT NULL AND ps.source_key=p.rel_path AND ps.source_version=p.source_version THEN 1 ELSE 0 END
 FROM photos p JOIN photo_shares ps ON ps.photo_id=p.id WHERE p.broken=0 AND p.width>0 AND p.height>0 AND p.album_path IN (`+strings.Join(holders, ",")+`) ORDER BY p.album_path,p.display_path,p.id`, args...)
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	items := []batchItem{}
	shared := 0
	found := map[string]bool{}
	for rows.Next() {
		var it batchItem
		var broken, active int
		p := &it.Photo
		err = rows.Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion, &it.Generation, &active)
		if err != nil {
			break
		}
		shared += active
		found[photoAlbumID(p.RelPath)] = true
		items = append(items, it)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	if len(found) != len(albums) {
		a.answer(w, nil, errBatchConflict)
		return
	}
	raw, _ := json.Marshal(albums)
	_, err = tx.Exec("INSERT INTO share_batches(id,mode,albums,status,created,total,shared) VALUES(?,?,?,'prepared',?,?,?)", id, in.Mode, string(raw), a.now().Unix(), len(items), shared)
	if err == nil {
		for i, it := range items {
			p, _ := json.Marshal(it.Photo)
			_, err = tx.Exec("INSERT INTO share_batch_items(batch_id,ordinal,photo,generation) VALUES(?,?,?,?)", id, i, string(p), it.Generation)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	b, err := a.read(id)
	a.answer(w, b, err)
}
func (a *batchAPI) get(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.cleanup(); err != nil {
		a.answer(w, nil, err)
		return
	}
	b, err := a.read(r.PathValue("id"))
	a.answer(w, b, err)
}
func (a *batchAPI) list(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.cleanup(); err != nil {
		a.answer(w, nil, err)
		return
	}
	rows, err := a.share.store.db.Query("SELECT id FROM share_batches WHERE status!='prepared' ORDER BY (status='running') DESC,created DESC,id")
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	out := []shareBatch{}
	for _, id := range ids {
		tx, e := a.share.store.db.Begin()
		if e != nil {
			a.answer(w, nil, e)
			return
		}
		b, e := loadBatch(tx, id, false)
		tx.Rollback()
		if e != nil {
			a.answer(w, nil, e)
			return
		}
		b.Items = nil
		out = append(out, b)
	}
	a.answer(w, map[string]any{"tasks": out}, nil)
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
		writeJSON(w, 400, map[string]string{"error": "请重新核对任务"})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.cleanup(); err != nil {
		a.answer(w, nil, err)
		return
	}
	tx, err := a.share.store.db.Begin()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, r.PathValue("id"), true)
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	// Lost answers and duplicated clicks read the same attempt without restarting it.
	if b.Revision != *in.Revision || (!retry && b.Status != "prepared") || (retry && b.Status == "running") {
		a.answer(w, b, nil)
		return
	}
	if retry && b.Status == "prepared" {
		a.answer(w, nil, errBatchConflict)
		return
	}
	var active int
	err = tx.QueryRow("SELECT COUNT(*) FROM share_batches WHERE status='running'").Scan(&active)
	if err == nil && active > 0 {
		err = errBatchConflict
	}
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	if retry {
		_, err = tx.Exec("UPDATE share_batch_items SET status='pending',error='',path='' WHERE batch_id=? AND status!='success'", b.ID)
	}
	if err == nil {
		_, err = tx.Exec("UPDATE share_batches SET status='running',revision=revision+1,finished=0 WHERE id=?", b.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.run(ctx, b.ID, b.Revision+1)
	b, err = a.read(b.ID)
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
		writeJSON(w, 400, map[string]string{"error": "请重新核对任务"})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := a.read(r.PathValue("id"))
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	if b.Status == "running" && b.Revision == *in.Revision {
		_, err = a.share.store.db.Exec("UPDATE share_batches SET status='cancelled',finished=? WHERE id=?", a.now().Unix(), b.ID)
		if err == nil && a.cancel != nil {
			a.cancel()
		}
	}
	if err == nil {
		b, err = a.read(b.ID)
	}
	a.answer(w, b, err)
}
func (a *batchAPI) run(ctx context.Context, id string, revision int64) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("batch panic", "err", p)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, err := a.share.store.db.Exec("UPDATE share_batches SET status='interrupted',finished=? WHERE id=? AND status='running' AND revision=?", a.now().Unix(), id, revision); err != nil {
			slog.Error("batch interrupted", "err", err)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		var mode, state string
		var currentRevision int64
		var it batchItem
		var raw string
		a.mu.Lock()
		err := a.share.store.db.QueryRow("SELECT mode,status,revision FROM share_batches WHERE id=?", id).Scan(&mode, &state, &currentRevision)
		if err == nil && state == "running" && currentRevision == revision {
			err = a.share.store.db.QueryRow("SELECT ordinal,photo,generation FROM share_batch_items WHERE batch_id=? AND status='pending' ORDER BY ordinal LIMIT 1", id).Scan(&it.Ordinal, &raw, &it.Generation)
		}
		if err == sql.ErrNoRows && state == "running" {
			_, err = a.share.store.db.Exec("UPDATE share_batches SET status='completed',finished=? WHERE id=? AND status='running' AND revision=?", a.now().Unix(), id, revision)
			a.mu.Unlock()
			if err != nil {
				slog.Error("batch finish", "err", err)
			}
			return
		}
		a.mu.Unlock()
		if err != nil {
			slog.Error("batch read", "err", err)
			return
		}
		if state != "running" || currentRevision != revision {
			return
		}
		if err = json.Unmarshal([]byte(raw), &it.Photo); err != nil {
			slog.Error("batch decode", "err", err)
			return
		}
		it.ID = it.Photo.ID
		fresh := photoShare{}
		var snap *shareSnapshot
		p, ok, e := a.share.store.getByID(it.ID)
		if e != nil {
			err = e
		} else if !ok || !photoVisible(p) || photoShareVersion(p) != photoShareVersion(it.Photo) {
			err = errSourceChanged
		} else {
			current, e := a.share.store.shareForPhoto(it.ID)
			if e != nil {
				err = e
			} else if current.Generation != it.Generation {
				err = errShareStateChanged
			}
		}
		if err == nil && mode == "open" {
			err = a.share.acquire(ctx)
			if err == nil {
				snap, err = a.share.snapshot(ctx, it.Photo)
				<-a.share.gate
			}
			if err == nil {
				var token [32]byte
				_, err = rand.Read(token[:])
				fresh = photoShare{Token: base64.RawURLEncoding.EncodeToString(token[:]), SourceKey: it.Photo.sourceKey(), SourceVersion: it.Photo.SourceVersion, Scope: snap.scope, Digest: snap.digest, MIME: snap.mime, Extension: snap.extension, Size: snap.size}
			}
		}
		if snap != nil {
			snap.file.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if commitErr := a.commitItem(id, revision, mode, it, fresh, err); commitErr != nil {
			slog.Error("batch item", "err", commitErr)
			return
		}
	}
}
func batchFailure(err error) string {
	if errors.Is(err, errShareStateChanged) {
		return "分享状态已有较新的操作，请重新核对范围"
	}
	if shareSourceChanged(err) {
		return "原图已改变、移走或删除，请重新核对范围"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "读取原图超时，可以重试"
	}
	return "原图暂时无法读取或处理，可以重试"
}
func (a *batchAPI) commitItem(id string, revision int64, mode string, it batchItem, fresh photoShare, itemErr error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	tx, err := a.share.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var currentRevision int64
	if err = tx.QueryRow("SELECT status,revision FROM share_batches WHERE id=?", id).Scan(&state, &currentRevision); err != nil {
		return err
	}
	if state != "running" || currentRevision != revision {
		return nil
	}
	var sh photoShare
	changed := false
	if itemErr == nil {
		// Stop validates photo identity too, because integer ids can be reused.
		var raw string
		err = tx.QueryRow("SELECT rel_path || ':' || source_version FROM photos WHERE id=?", it.ID).Scan(&raw)
		if err == sql.ErrNoRows {
			itemErr = errSourceChanged
		} else if err != nil {
			return err
		} else if raw != it.Photo.sourceKey()+":"+it.Photo.SourceVersion {
			itemErr = errSourceChanged
		}
		if itemErr == nil {
			current, e := scanShare(tx.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE photo_id=?", it.ID), it.ID)
			if e != nil {
				return e
			}
			if current.Generation != it.Generation {
				itemErr = errShareStateChanged
			}
		}
		if itemErr == nil {
			if mode == "open" {
				sh, changed, itemErr = a.share.store.writeShare(tx, it.Photo, it.Generation, fresh, false)
			} else {
				sh, changed, itemErr = a.share.store.writeStopShare(tx, it.ID, it.Generation)
			}
		}
	}
	if itemErr != nil && shareSourceChanged(itemErr) {
		// Keep the same-generation old permit from appearing active after a detected swap.
		result, e := tx.Exec("UPDATE photo_shares SET token=NULL,generation=generation+1 WHERE photo_id=? AND generation=? AND token IS NOT NULL", it.ID, it.Generation)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		changed = changed || n > 0
	}
	status, message, path := "success", "", ""
	if itemErr != nil {
		status, message = "failed", batchFailure(itemErr)
	} else if mode == "open" {
		path = sh.path()
	}
	if _, err = tx.Exec("UPDATE share_batch_items SET status=?,error=?,path=? WHERE batch_id=? AND ordinal=? AND status='pending'", status, message, path, id, it.Ordinal); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if changed {
		a.share.store.cache.shareRevision.Add(1)
	}
	return nil
}
func (a *batchAPI) links(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if err := a.cleanup(); err != nil {
		a.mu.Unlock()
		a.answer(w, nil, err)
		return
	}
	b, err := a.read(r.PathValue("id"))
	a.mu.Unlock()
	if err != nil {
		a.answer(w, nil, err)
		return
	}
	paths := []string{}
	for _, it := range b.Items {
		if it.Status != "success" || it.Path == "" {
			continue
		}
		p, ok, e := a.share.store.getByID(it.ID)
		if e != nil {
			a.answer(w, nil, e)
			return
		}
		if !ok || photoShareVersion(p) != photoShareVersion(it.Photo) {
			continue
		}
		sh, e := a.share.store.shareForPhoto(it.ID)
		if e != nil {
			a.answer(w, nil, e)
			return
		}
		if sh.path() != it.Path {
			continue
		}
		scope, e := a.share.scope(p.sourceKey())
		obj, statErr := a.share.sources.Stat(r.Context(), p.sourceKey())
		if e == nil && statErr == nil && scope == sh.Scope && obj.Version == sh.SourceVersion && obj.Size == sh.Size {
			paths = append(paths, it.Path)
		}
	}
	a.answer(w, map[string]any{"paths": paths, "excluded": b.Succeeded - len(paths)}, nil)
}

// Status shares the existing filtered index revision; no per-album source reads.
func (s *store) albumShareCounts() (map[string]int, error) {
	idx, err := s.sharedPhotoIndex()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for id, items := range idx.albums {
		counts[id] = len(items)
	}
	return counts, nil
}

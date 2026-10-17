package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Batch sharing tasks live in SQLite next to the permits they change: the
// albums a task covers, every photo with the permit generation it was checked
// against, and each photo's result. batch_sharing.go runs the tasks.

const (
	batchRetention   = 48 * time.Hour   // finished, cancelled and interrupted records
	batchPrepareTTL  = 10 * time.Minute // a checked range waiting for 确认
	batchPreparedMax = 16
)

var errBatchConflict = errors.New("任务或图片状态已改变，请重新核对")

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

// batchItems says which photos a read of a task carries.
type batchItems int

const (
	batchCounts batchItems = iota // none: a task list only shows counts
	batchView                     // what the page shows: the failures while running, every photo afterwards
	batchAll                      // every photo
)

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

// interruptRunningBatches runs at start-up: only a live process owns an
// executor, so a task still marked running stopped with the old process.
func (s *store) interruptRunningBatches(now time.Time) error {
	_, err := s.db.Exec("UPDATE share_batches SET status='interrupted', finished=? WHERE status='running'", now.Unix())
	return err
}

// batchExpired is a record past its keeping time; cleanupBatches deletes the
// same records, at the next list, prepare or start.
func batchExpired(b shareBatch, now time.Time) bool {
	return (b.Finished > 0 && b.Finished < now.Add(-batchRetention).Unix()) ||
		(b.Status == "prepared" && b.Created < now.Add(-batchPrepareTTL).Unix())
}

// cleanupBatches forgets expired records. The permits they made stay.
func (s *store) cleanupBatches(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM share_batches WHERE (finished>0 AND finished<?) OR (status='prepared' AND created<?)
 OR id IN (SELECT id FROM share_batches WHERE status='prepared' ORDER BY created DESC LIMIT -1 OFFSET ?)`,
		now.Add(-batchRetention).Unix(), now.Add(-batchPrepareTTL).Unix(), batchPreparedMax)
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil || len(ids) == 0 {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec("DELETE FROM share_batch_items WHERE batch_id=?", id); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM share_batches WHERE id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanIDs(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadBatch(tx *sql.Tx, id string, items batchItems) (shareBatch, error) {
	var b shareBatch
	var albums string
	err := tx.QueryRow(`SELECT id,mode,albums,status,revision,created,finished,total,shared FROM share_batches WHERE id=?`, id).
		Scan(&b.ID, &b.Mode, &albums, &b.Status, &b.Revision, &b.Created, &b.Finished, &b.Total, &b.Shared)
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
	err = tx.QueryRow("SELECT COALESCE(SUM(status='success'),0),COALESCE(SUM(status='failed'),0) FROM share_batch_items WHERE batch_id=?", id).
		Scan(&b.Succeeded, &b.Failed)
	if err != nil {
		return b, err
	}
	b.Done = b.Succeeded + b.Failed
	if items == batchCounts {
		return b, nil
	}
	// A running task is polled every two seconds; only its failures are news.
	query := "SELECT ordinal,photo,generation,status,error,path FROM share_batch_items WHERE batch_id=?"
	if items == batchView && b.Status == "running" {
		query += " AND status='failed'"
	}
	rows, err := tx.Query(query+" ORDER BY ordinal", id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b.Items = []batchItem{}
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
	return b, rows.Err()
}

func (s *store) readBatch(id string, items batchItems) (shareBatch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return shareBatch{}, err
	}
	defer tx.Rollback()
	return loadBatch(tx, id, items)
}

// listBatches is every started task, the running one first, then newest.
func (s *store) listBatches() ([]shareBatch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT id FROM share_batches WHERE status!='prepared' ORDER BY (status='running') DESC,created DESC,id")
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	tasks := []shareBatch{}
	for _, id := range ids {
		b, err := loadBatch(tx, id, batchCounts)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, b)
	}
	return tasks, nil
}

// createBatch fixes the range of a new task: every visible photo directly in
// the chosen albums, across sources, with the permit generation seen now.
// Every album must still have a photo, or the owner checked something else.
func (s *store) createBatch(id, mode string, albums []string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	args := make([]any, len(albums))
	for i, album := range albums {
		args[i] = album
	}
	rows, err := tx.Query(`SELECT photos.id, photos.rel_path, photos.display_path, photos.size, photos.mtime_unix,
 photos.width, photos.height, photos.source_version, share.generation, `+sharedPhotos+`
 FROM photos JOIN photo_shares share ON share.photo_id = photos.id
 WHERE `+visiblePhotos+` AND photos.album_path IN (`+strings.TrimSuffix(strings.Repeat("?,", len(albums)), ",")+`)
 ORDER BY photos.album_path, photos.display_path, photos.id`, args...)
	if err != nil {
		return err
	}
	items := []batchItem{}
	shared := 0
	found := map[string]bool{}
	for rows.Next() {
		var it batchItem
		var active bool
		p := &it.Photo
		if err = rows.Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &p.SourceVersion, &it.Generation, &active); err != nil {
			break
		}
		if active {
			shared++
		}
		found[photoAlbumID(p.RelPath)] = true
		items = append(items, it)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(found) != len(albums) {
		return errBatchConflict
	}
	raw, _ := json.Marshal(albums)
	_, err = tx.Exec("INSERT INTO share_batches(id,mode,albums,status,created,total,shared) VALUES(?,?,?,'prepared',?,?,?)", id, mode, string(raw), now.Unix(), len(items), shared)
	if err != nil {
		return err
	}
	for i, it := range items {
		p, _ := json.Marshal(it.Photo)
		if _, err = tx.Exec("INSERT INTO share_batch_items(batch_id,ordinal,photo,generation) VALUES(?,?,?,?)", id, i, string(p), it.Generation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// beginBatchAttempt starts a prepared task, or retries a stopped one's failed
// and remaining photos, as a new attempt. A lost answer or a second click
// reads the same attempt instead: started is false and nothing changes.
func (s *store) beginBatchAttempt(id string, revision int64, retry bool) (started bool, attempt int64, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	b, err := loadBatch(tx, id, batchCounts)
	if err != nil {
		return false, 0, err
	}
	if b.Revision != revision || (!retry && b.Status != "prepared") || (retry && b.Status == "running") {
		return false, 0, nil
	}
	if retry && b.Status == "prepared" {
		return false, 0, errBatchConflict
	}
	var active int
	if err = tx.QueryRow("SELECT COUNT(*) FROM share_batches WHERE status='running'").Scan(&active); err != nil {
		return false, 0, err
	}
	if active > 0 {
		return false, 0, errBatchConflict
	}
	if retry {
		if _, err = tx.Exec("UPDATE share_batch_items SET status='pending',error='',path='' WHERE batch_id=? AND status!='success'", id); err != nil {
			return false, 0, err
		}
	}
	if _, err = tx.Exec("UPDATE share_batches SET status='running',revision=revision+1,finished=0 WHERE id=?", id); err != nil {
		return false, 0, err
	}
	return true, b.Revision + 1, tx.Commit()
}

// cancelBatch stops the given attempt; finished photos keep their results.
func (s *store) cancelBatch(id string, revision int64, now time.Time) (bool, error) {
	res, err := s.db.Exec("UPDATE share_batches SET status='cancelled',finished=? WHERE id=? AND status='running' AND revision=?", now.Unix(), id, revision)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// interruptBatch marks an attempt that stopped without finishing.
func (s *store) interruptBatch(id string, revision int64, now time.Time) error {
	_, err := s.db.Exec("UPDATE share_batches SET status='interrupted',finished=? WHERE id=? AND status='running' AND revision=?", now.Unix(), id, revision)
	return err
}

// nextBatchItem is the attempt's next pending photo. done means the attempt is
// over: it just completed, was cancelled, or a newer attempt replaced it.
func (s *store) nextBatchItem(id string, revision int64, now time.Time) (mode string, it batchItem, done bool, err error) {
	var state string
	var current int64
	if err = s.db.QueryRow("SELECT mode,status,revision FROM share_batches WHERE id=?", id).Scan(&mode, &state, &current); err != nil {
		return mode, it, false, err
	}
	if state != "running" || current != revision {
		return mode, it, true, nil
	}
	var raw string
	err = s.db.QueryRow("SELECT ordinal,photo,generation FROM share_batch_items WHERE batch_id=? AND status='pending' ORDER BY ordinal LIMIT 1", id).
		Scan(&it.Ordinal, &raw, &it.Generation)
	if err == sql.ErrNoRows {
		_, err = s.db.Exec("UPDATE share_batches SET status='completed',finished=? WHERE id=? AND status='running' AND revision=?", now.Unix(), id, revision)
		return mode, it, true, err
	}
	if err != nil {
		return mode, it, false, err
	}
	if err = json.Unmarshal([]byte(raw), &it.Photo); err != nil {
		return mode, it, false, err
	}
	it.ID = it.Photo.ID
	return mode, it, false, nil
}

// commitBatchItem saves one photo's permit change and its result in one
// transaction, unless the attempt has meanwhile been cancelled or replaced.
func (s *store) commitBatchItem(id string, revision int64, mode string, it batchItem, fresh photoShare, itemErr error) error {
	tx, err := s.db.Begin()
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
		if itemErr, err = batchItemCurrent(tx, it); err != nil {
			return err
		}
	}
	if itemErr == nil {
		if mode == "open" {
			sh, changed, itemErr = s.writeShare(tx, it.Photo, it.Generation, fresh, false)
		} else {
			sh, changed, itemErr = s.writeStopShare(tx, it.ID, it.Generation)
		}
	}
	if itemErr != nil && shareSourceChanged(itemErr) {
		// Keep the same-generation old permit from appearing active after a detected swap.
		result, e := tx.Exec(retireShares+"photo_id=? AND generation=? AND token IS NOT NULL", it.ID, it.Generation)
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
		s.cache.shareRevision.Add(1)
	}
	return nil
}

// batchItemCurrent says why a photo no longer belongs to its task, if it does
// not: errSourceChanged when the photo is not the one chosen (stopping checks
// this too, because integer ids can be reused), errShareStateChanged when a
// newer single-photo action came first. err is a failed database read.
func batchItemCurrent(tx *sql.Tx, it batchItem) (itemErr, err error) {
	var raw string
	err = tx.QueryRow("SELECT rel_path || ':' || source_version FROM photos WHERE id=?", it.ID).Scan(&raw)
	if err == sql.ErrNoRows || (err == nil && raw != it.Photo.sourceKey()+":"+it.Photo.SourceVersion) {
		return errSourceChanged, nil
	}
	if err != nil {
		return nil, err
	}
	current, err := scanShare(tx.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE photo_id=?", it.ID), it.ID)
	if err != nil {
		return nil, err
	}
	if current.Generation != it.Generation {
		return errShareStateChanged, nil
	}
	return nil, nil
}

// batchFailure is the reason a photo shows in the task's results.
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

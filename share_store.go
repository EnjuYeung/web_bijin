package main

import (
	"database/sql"
	"errors"
	"fmt"
)

var errShareStateChanged = errors.New("share state changed")

// An inactive row keeps its generation so a delayed write cannot reopen it.
type photoShare struct {
	PhotoID                                                  int64
	Token                                                    string
	Generation                                               int64
	SourceKey, SourceVersion, Scope, Digest, MIME, Extension string
	Size                                                     int64
}

const shareColumns = "photo_id, COALESCE(token, ''), generation, source_key, source_version, source_scope, sha256, mime, extension, size"

// retireShares ends permits: the address stops working and the generation
// moves on, so a delayed write for the old one is refused. Append a WHERE.
const retireShares = "UPDATE photo_shares SET token = NULL, generation = generation + 1 WHERE "
const sharedPhotos = `EXISTS (SELECT 1 FROM photo_shares ps WHERE ps.photo_id = photos.id
 AND ps.token IS NOT NULL AND ps.source_key = photos.rel_path AND ps.source_version = photos.source_version)`

func (s *store) migrateShares() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS photo_shares (
 photo_id INTEGER PRIMARY KEY,
 token TEXT UNIQUE,
 generation INTEGER NOT NULL DEFAULT 0,
 source_key TEXT NOT NULL DEFAULT '',
 source_version TEXT NOT NULL DEFAULT '',
 source_scope TEXT NOT NULL DEFAULT '',
 sha256 TEXT NOT NULL DEFAULT '',
 mime TEXT NOT NULL DEFAULT '',
 extension TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO photo_shares (photo_id) SELECT id FROM photos;
DROP TRIGGER IF EXISTS photos_insert_shares;
CREATE TRIGGER photos_insert_shares AFTER INSERT ON photos BEGIN
 INSERT OR IGNORE INTO photo_shares (photo_id) VALUES (NEW.id);
END;
DROP TRIGGER IF EXISTS photos_delete_shares;
CREATE TRIGGER photos_delete_shares BEFORE DELETE ON photos BEGIN
 UPDATE photo_shares SET token = NULL, generation = generation + 1 WHERE photo_id = OLD.id;
END;
DROP TRIGGER IF EXISTS photos_change_shares;
CREATE TRIGGER photos_change_shares AFTER UPDATE ON photos
WHEN OLD.rel_path != NEW.rel_path OR OLD.source_version != NEW.source_version OR OLD.size != NEW.size OR OLD.mtime_unix != NEW.mtime_unix
 OR OLD.display_path != NEW.display_path OR NEW.broken != 0 OR NEW.width <= 0 OR NEW.height <= 0
BEGIN
 UPDATE photo_shares SET token = NULL, generation = generation + 1
 WHERE photo_id = NEW.id;
END;
`)
	return err
}

func scanShare(row *sql.Row, id int64) (photoShare, error) {
	sh := photoShare{PhotoID: id}
	err := row.Scan(&sh.PhotoID, &sh.Token, &sh.Generation, &sh.SourceKey, &sh.SourceVersion,
		&sh.Scope, &sh.Digest, &sh.MIME, &sh.Extension, &sh.Size)
	if err == sql.ErrNoRows {
		return photoShare{PhotoID: id}, nil
	}
	return sh, err
}

func (s *store) shareForPhoto(id int64) (photoShare, error) {
	return scanShare(s.db.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE photo_id = ?", id), id)
}

func (s *store) shareForToken(token string) (photoShare, error) {
	return scanShare(s.db.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE token = ?", token), 0)
}

func (s *store) retireShare(token string) error {
	res, err := s.db.Exec(retireShares+"token = ?", token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if n > 0 {
		s.cache.shareRevision.Add(1)
	}
	return err
}

func (s *store) saveShare(p photo, expected int64, fresh photoShare) (photoShare, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return photoShare{}, err
	}
	defer tx.Rollback()
	sh, changed, err := s.writeShare(tx, p, expected, fresh, true)
	if err != nil && !changed {
		return photoShare{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return photoShare{}, commitErr
	}
	if changed {
		s.cache.shareRevision.Add(1)
	}
	return sh, err
}

// A batch does not converge with newer single-photo writes: their intent wins.
func (s *store) writeShare(tx *sql.Tx, p photo, expected int64, fresh photoShare, converge bool) (photoShare, bool, error) {
	var matches int
	err := tx.QueryRow(`SELECT COUNT(*) FROM photos WHERE id = ? AND rel_path = ?
 AND source_version = ? AND size = ? AND mtime_unix = ? AND `+visiblePhotos,
		p.ID, p.sourceKey(), p.SourceVersion, p.Size, p.MtimeUnix).Scan(&matches)
	if err != nil {
		return photoShare{}, false, err
	}
	if matches != 1 {
		return photoShare{}, false, errSourceChanged
	}
	current, err := scanShare(tx.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE photo_id = ?", p.ID), p.ID)
	if err != nil {
		return photoShare{}, false, err
	}
	// Two copies that started with the same inactive generation converge.
	if current.Generation != expected && !(converge && current.Token != "" && current.Generation == expected+1) {
		return photoShare{}, false, errShareStateChanged
	}
	if current.Token != "" {
		if current.Digest != fresh.Digest || current.Scope != fresh.Scope || current.SourceKey != fresh.SourceKey || current.SourceVersion != fresh.SourceVersion {
			if _, err = tx.Exec(retireShares+"photo_id = ?", p.ID); err != nil {
				return photoShare{}, false, err
			}
			return photoShare{}, true, errSourceChanged
		}
		return current, false, nil
	}
	fresh.PhotoID, fresh.Generation = p.ID, current.Generation+1
	_, err = tx.Exec(`INSERT INTO photo_shares
 (photo_id, token, generation, source_key, source_version, source_scope, sha256, mime, extension, size)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(photo_id) DO UPDATE SET token=excluded.token, generation=excluded.generation,
 source_key=excluded.source_key, source_version=excluded.source_version, source_scope=excluded.source_scope,
 sha256=excluded.sha256, mime=excluded.mime, extension=excluded.extension, size=excluded.size`,
		fresh.PhotoID, fresh.Token, fresh.Generation, fresh.SourceKey, fresh.SourceVersion,
		fresh.Scope, fresh.Digest, fresh.MIME, fresh.Extension, fresh.Size)
	if err != nil {
		return photoShare{}, false, err
	}
	return fresh, true, nil
}

func (s *store) stopShare(id, expected int64) (photoShare, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return photoShare{}, err
	}
	defer tx.Rollback()
	sh, changed, err := s.writeStopShare(tx, id, expected)
	if err != nil {
		return photoShare{}, err
	}
	if err = tx.Commit(); err != nil {
		return photoShare{}, err
	}
	if changed {
		s.cache.shareRevision.Add(1)
	}
	return sh, nil
}

func (s *store) writeStopShare(tx *sql.Tx, id, expected int64) (photoShare, bool, error) {
	current, err := scanShare(tx.QueryRow("SELECT "+shareColumns+" FROM photo_shares WHERE photo_id = ?", id), id)
	if err != nil {
		return photoShare{}, false, err
	}
	if current.Token == "" && (current.Generation == expected || current.Generation == expected+1) {
		return current, false, nil
	}
	if current.Generation != expected {
		return photoShare{}, false, errShareStateChanged
	}
	_, err = tx.Exec(retireShares+"photo_id = ?", id)
	if err != nil {
		return photoShare{}, false, err
	}
	current.Token, current.Generation = "", current.Generation+1
	return current, true, nil
}

func (sh photoShare) path() string {
	if sh.Token == "" {
		return ""
	}
	return fmt.Sprintf("/i/%s.%s", sh.Token, sh.Extension)
}

// albumShareCounts is how many visible photos of each album are shared. It
// shares the filtered index revision and reads no sources.
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

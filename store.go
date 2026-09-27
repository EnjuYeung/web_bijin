package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type photo struct {
	ID            int64
	SourceKey     string
	RelPath       string
	Size          int64
	MtimeUnix     int64
	Width         int
	Height        int
	Broken        bool
	SourceVersion string
}

func (p photo) sourceKey() string {
	if p.SourceKey != "" {
		return p.SourceKey
	}
	return p.RelPath
}

type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) Close() error {
	return s.db.Close()
}

func (s *store) migrate() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS photos (
  id INTEGER PRIMARY KEY,
  rel_path TEXT NOT NULL UNIQUE,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  broken INTEGER NOT NULL DEFAULT 0,
  source_version TEXT NOT NULL DEFAULT '',
  display_path TEXT NOT NULL DEFAULT ''
);
DROP INDEX IF EXISTS photos_mtime;
`); err != nil {
		return err
	}
	hasVersion, err := s.hasColumn("photos", "source_version")
	if err != nil {
		return err
	}
	if !hasVersion {
		_, err = s.db.Exec(`ALTER TABLE photos ADD COLUMN source_version TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
	}
	hasDisplayPath, err := s.hasColumn("photos", "display_path")
	if err != nil {
		return err
	}
	if !hasDisplayPath {
		_, err = s.db.Exec(`ALTER TABLE photos ADD COLUMN display_path TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`UPDATE photos SET display_path = rel_path WHERE display_path = ''`)
	}
	return err
}

func (s *store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *store) getByID(id int64) (photo, bool, error) {
	var p photo
	var broken int
	err := s.db.QueryRow(
		`SELECT id, rel_path, display_path, size, mtime_unix, width, height, broken, source_version FROM photos WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion)
	if err == sql.ErrNoRows {
		return photo{}, false, nil
	}
	if err != nil {
		return photo{}, false, err
	}
	p.Broken = broken != 0
	return p, true, nil
}

func (s *store) getBySourceKey(rel string) (photo, bool, error) {
	var p photo
	var broken int
	err := s.db.QueryRow(
		`SELECT id, rel_path, display_path, size, mtime_unix, width, height, broken, source_version FROM photos WHERE rel_path = ?`,
		rel,
	).Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion)
	if err == sql.ErrNoRows {
		return photo{}, false, nil
	}
	if err != nil {
		return photo{}, false, err
	}
	p.Broken = broken != 0
	return p, true, nil
}

func (s *store) upsert(p photo) (int64, error) {
	key := p.SourceKey
	if key == "" {
		key = p.RelPath
	}
	existing, ok, err := s.getBySourceKey(key)
	if err != nil {
		return 0, err
	}
	broken := 0
	if p.Broken {
		broken = 1
	}
	if ok {
		_, err = s.db.Exec(
			`UPDATE photos SET display_path=?, size=?, mtime_unix=?, width=?, height=?, broken=?, source_version=? WHERE id=?`,
			p.RelPath, p.Size, p.MtimeUnix, p.Width, p.Height, broken, p.SourceVersion, existing.ID,
		)
		return existing.ID, err
	}
	res, err := s.db.Exec(
		`INSERT INTO photos (rel_path, display_path, size, mtime_unix, width, height, broken, source_version) VALUES (?,?,?,?,?,?,?,?)`,
		key, p.RelPath, p.Size, p.MtimeUnix, p.Width, p.Height, broken, p.SourceVersion,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *store) deleteMissing(keep map[string]struct{}) ([]photo, error) {
	rows, err := s.db.Query(`SELECT id, rel_path, display_path FROM photos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gone []photo
	for rows.Next() {
		var p photo
		if err := rows.Scan(&p.ID, &p.SourceKey, &p.RelPath); err != nil {
			return nil, err
		}
		if _, ok := keep[p.SourceKey]; !ok {
			gone = append(gone, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range gone {
		if _, err := s.db.Exec(`DELETE FROM photos WHERE id=?`, p.ID); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

func (s *store) countOK() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM photos WHERE broken=0 AND width>0 AND height>0`).Scan(&n)
	return n, err
}

func (s *store) listOK() ([]photo, error) {
	rows, err := s.db.Query(`SELECT id, rel_path, display_path, size, mtime_unix, width, height, broken, source_version
	      FROM photos
	      WHERE broken=0 AND width>0 AND height>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []photo
	for rows.Next() {
		var p photo
		var broken int
		if err := rows.Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion); err != nil {
			return nil, err
		}
		p.Broken = broken != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type photo struct {
	ID        int64
	RelPath   string
	Size      int64
	MtimeUnix int64
	Width     int
	Height    int
	Broken    bool
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
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS photos (
  id INTEGER PRIMARY KEY,
  rel_path TEXT NOT NULL UNIQUE,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  broken INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS photos_mtime ON photos(mtime_unix DESC, id DESC);
`)
	return err
}

func (s *store) getByID(id int64) (photo, bool, error) {
	var p photo
	var broken int
	err := s.db.QueryRow(
		`SELECT id, rel_path, size, mtime_unix, width, height, broken FROM photos WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken)
	if err == sql.ErrNoRows {
		return photo{}, false, nil
	}
	if err != nil {
		return photo{}, false, err
	}
	p.Broken = broken != 0
	return p, true, nil
}

func (s *store) getByPath(rel string) (photo, bool, error) {
	var p photo
	var broken int
	err := s.db.QueryRow(
		`SELECT id, rel_path, size, mtime_unix, width, height, broken FROM photos WHERE rel_path = ?`,
		rel,
	).Scan(&p.ID, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken)
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
	existing, ok, err := s.getByPath(p.RelPath)
	if err != nil {
		return 0, err
	}
	broken := 0
	if p.Broken {
		broken = 1
	}
	if ok {
		_, err = s.db.Exec(
			`UPDATE photos SET size=?, mtime_unix=?, width=?, height=?, broken=? WHERE id=?`,
			p.Size, p.MtimeUnix, p.Width, p.Height, broken, existing.ID,
		)
		return existing.ID, err
	}
	res, err := s.db.Exec(
		`INSERT INTO photos (rel_path, size, mtime_unix, width, height, broken) VALUES (?,?,?,?,?,?)`,
		p.RelPath, p.Size, p.MtimeUnix, p.Width, p.Height, broken,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *store) deleteMissing(keep map[string]struct{}) ([]photo, error) {
	rows, err := s.db.Query(`SELECT id, rel_path FROM photos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gone []photo
	for rows.Next() {
		var p photo
		if err := rows.Scan(&p.ID, &p.RelPath); err != nil {
			return nil, err
		}
		if _, ok := keep[p.RelPath]; !ok {
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
	rows, err := s.db.Query(`SELECT id, rel_path, size, mtime_unix, width, height, broken
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
		if err := rows.Scan(&p.ID, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken); err != nil {
			return nil, err
		}
		p.Broken = broken != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func safeRelPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("bad path")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(osPathSep)) {
		return "", fmt.Errorf("bad path")
	}
	full := filepath.Join(root, clean)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	sep := string(osPathSep)
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+sep) {
		return "", fmt.Errorf("outside root")
	}
	return absFull, nil
}

const osPathSep = filepath.Separator

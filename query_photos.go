package main

import (
	"sort"
	"strings"
)

const visiblePhotos = "broken = 0 AND width > 0 AND height > 0"
const photoColumns = "id, rel_path, display_path, size, mtime_unix, width, height, broken, source_version"

func (s *store) migratePhotoQueries() error {
	has, err := s.hasColumn("photos", "album_path")
	if err != nil {
		return err
	}
	if !has {
		if _, err := s.db.Exec(`ALTER TABLE photos ADD COLUMN album_path TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Also repairs records written by an older binary after a rollback.
	rows, err := s.db.Query(`SELECT id, display_path FROM photos WHERE album_path = ''`)
	if err != nil {
		return err
	}
	type backfill struct {
		id    int64
		album string
	}
	var missing []backfill
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return err
		}
		missing = append(missing, backfill{id, photoAlbumID(path)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.Prepare(`UPDATE photos SET album_path = ? WHERE id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, row := range missing {
			if _, err := stmt.Exec(row.album, row.id); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`
CREATE INDEX IF NOT EXISTS photos_visible_id ON photos(id)
WHERE broken = 0 AND width > 0 AND height > 0;
CREATE INDEX IF NOT EXISTS photos_visible_album_time ON photos(album_path, mtime_unix DESC, id DESC)
WHERE broken = 0 AND width > 0 AND height > 0;
`)
	return err
}

type photoIndex struct {
	snapshot uint64
	ids      []int64
	albums   map[string][]int64
}

func (s *store) photoIndex() (*photoIndex, error) {
	return s.cache.index.get(&s.cache.indexRevision, func(uint64) (*photoIndex, error) {
		rows, err := s.db.Query("SELECT id, album_path FROM photos WHERE " + visiblePhotos)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		index := &photoIndex{snapshot: s.cache.index.builds.Load(), albums: make(map[string][]int64)}
		for rows.Next() {
			var id int64
			var album string
			if err := rows.Scan(&id, &album); err != nil {
				return nil, err
			}
			index.ids = append(index.ids, id)
			index.albums[album] = append(index.albums[album], id)
		}
		return index, rows.Err()
	})
}

type rankedPhoto struct {
	rank uint64
	id   int64
}
type photoOrderKey struct {
	snapshot uint64
	seed     int64
	album    string
}

func (s *store) photoOrder(index *photoIndex, seed int64, album string) ([]rankedPhoto, error) {
	key := photoOrderKey{index.snapshot, seed, album}
	return s.cache.orders.get(key, photoOrderLimits, func() ([]rankedPhoto, error) {
		ids := index.ids
		if album != "" {
			ids = index.albums[album]
		}
		order := make([]rankedPhoto, len(ids))
		for i, id := range ids {
			order[i] = rankedPhoto{photoRank(seed, id), id}
		}
		sort.Slice(order, func(i, j int) bool {
			return order[i].rank < order[j].rank || (order[i].rank == order[j].rank && order[i].id < order[j].id)
		})
		return order, nil
	}, func(order []rankedPhoto) int64 { return int64(len(order))*16 + int64(len(album)) + 128 })
}

func (s *store) photosByIDs(order []rankedPhoto, album string) (map[int64]photo, error) {
	out := make(map[int64]photo, len(order))
	if len(order) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(order)+1)
	for _, p := range order {
		args = append(args, p.id)
	}
	query := "SELECT " + photoColumns + " FROM photos WHERE " + visiblePhotos +
		" AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(order)), ",") + ")"
	if album != "" {
		query += " AND album_path = ?"
		args = append(args, album)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p photo
		var broken int
		if err := rows.Scan(&p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion); err != nil {
			return nil, err
		}
		p.Broken = broken != 0
		out[p.ID] = p
	}
	return out, rows.Err()
}

func (s *store) photoPage(seed int64, album string, afterRank uint64, afterID int64, limit int) ([]photo, int, *string, error) {
	index, err := s.photoIndex()
	if err != nil {
		return nil, 0, nil, err
	}
	order, err := s.photoOrder(index, seed, album)
	if err != nil {
		return nil, 0, nil, err
	}
	start := 0
	if afterID > 0 {
		start = sort.Search(len(order), func(i int) bool {
			return order[i].rank > afterRank || (order[i].rank == afterRank && order[i].id > afterID)
		})
	}
	limit = pickLimit(limit)
	photos := make([]photo, 0, limit)
	consumed := start
	for consumed < len(order) && len(photos) < limit {
		end := min(consumed+limit-len(photos), len(order))
		batch := order[consumed:end]
		found, err := s.photosByIDs(batch, album)
		if err != nil {
			return nil, 0, nil, err
		}
		for _, entry := range batch {
			if p, ok := found[entry.id]; ok {
				photos = append(photos, p)
			}
		}
		consumed = end
	}
	var next *string
	if consumed > start && consumed < len(order) {
		cursor := formatCursor(seed, photo{ID: order[consumed-1].id})
		next = &cursor
	}
	return photos, len(order), next, nil
}

func (s *store) albumSummaries() ([]albumSummary, error) {
	return s.cache.albums.get(&s.cache.albumRevision, func(uint64) ([]albumSummary, error) {
		// The correlated cover lookup uses the album/time index. This statement
		// reads one consistent snapshot and returns one complete cover per album.
		rows, err := s.db.Query(`
WITH summary AS (
 SELECT album_path, COUNT(*) AS photo_count, MIN(mtime_unix) AS added
 FROM photos WHERE ` + visiblePhotos + ` GROUP BY album_path
)
SELECT a.album_path, a.photo_count, a.added,
 p.id, p.rel_path, p.display_path, p.size, p.mtime_unix, p.width, p.height, p.broken, p.source_version
FROM summary a JOIN photos p ON p.id = (
 SELECT id FROM photos WHERE album_path = a.album_path AND ` + visiblePhotos + `
 ORDER BY mtime_unix DESC, id DESC LIMIT 1
)`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var albums []albumSummary
		for rows.Next() {
			var a albumSummary
			var broken int
			p := &a.Cover
			if err := rows.Scan(&a.ID, &a.Count, &a.Added, &p.ID, &p.SourceKey, &p.RelPath, &p.Size, &p.MtimeUnix, &p.Width, &p.Height, &broken, &p.SourceVersion); err != nil {
				return nil, err
			}
			p.Broken = broken != 0
			a.Name = albumDisplayName(a.ID)
			albums = append(albums, a)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		sort.Slice(albums, func(i, j int) bool {
			left, right := strings.ToLower(albums[i].Name), strings.ToLower(albums[j].Name)
			if left == right {
				return albums[i].ID < albums[j].ID
			}
			return left < right
		})
		return albums, nil
	})
}

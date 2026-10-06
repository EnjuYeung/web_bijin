package main

import (
	"math/rand/v2"
	"sort"
	"strings"
)

// The previous query algorithms are retained only as test oracles. They are
// not compiled into the application. Existing regression tests stay intact.

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

func groupAlbums(photos []photo) []albumSummary {
	byID := make(map[string]*albumSummary)
	for _, p := range photos {
		id := photoAlbumID(p.RelPath)
		a := byID[id]
		if a == nil {
			a = &albumSummary{ID: id, Name: albumDisplayName(id)}
			byID[id] = a
		}
		a.Count++
		if a.Count == 1 || p.MtimeUnix < a.Added {
			a.Added = p.MtimeUnix
		}
		if a.Cover.ID == 0 || p.MtimeUnix > a.Cover.MtimeUnix || (p.MtimeUnix == a.Cover.MtimeUnix && p.ID > a.Cover.ID) {
			a.Cover = p
		}
	}

	out := make([]albumSummary, 0, len(byID))
	for _, a := range byID {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		ni := strings.ToLower(out[i].Name)
		nj := strings.ToLower(out[j].Name)
		if ni == nj {
			return out[i].ID < out[j].ID
		}
		return ni < nj
	})
	return out
}

func filterAlbum(photos []photo, albumID string) []photo {
	out := make([]photo, 0)
	for _, p := range photos {
		if photoAlbumID(p.RelPath) == albumID {
			out = append(out, p)
		}
	}
	return out
}

func sortByRank(photos []photo, seed int64) {
	sort.SliceStable(photos, func(i, j int) bool {
		ri, rj := photoRank(seed, photos[i].ID), photoRank(seed, photos[j].ID)
		if ri != rj {
			return ri < rj
		}
		return photos[i].ID < photos[j].ID
	})
}

func pageAfter(photos []photo, seed int64, afterRank uint64, afterID int64, limit int) []photo {
	limit = pickLimit(limit)
	start := 0
	if afterID > 0 {
		start = len(photos)
		for i, p := range photos {
			r := photoRank(seed, p.ID)
			if r > afterRank || (r == afterRank && p.ID > afterID) {
				start = i
				break
			}
		}
	}
	if start >= len(photos) {
		return nil
	}
	end := start + limit
	if end > len(photos) {
		end = len(photos)
	}
	return photos[start:end]
}

func pickWallpaper(rows []wallpaper) (wallpaper, bool) {
	if len(rows) == 0 {
		return wallpaper{}, false
	}
	var order []int64
	byPhoto := map[int64][]wallpaper{}
	for _, r := range rows {
		if _, ok := byPhoto[r.PhotoID]; !ok {
			order = append(order, r.PhotoID)
		}
		byPhoto[r.PhotoID] = append(byPhoto[r.PhotoID], r)
	}
	group := byPhoto[order[rand.IntN(len(order))]]
	return group[rand.IntN(len(group))], true
}

func (s *store) wallpaperCandidates(f wallFilter) ([]wallpaper, error) {
	rows, err := s.db.Query(`SELECT w.photo_id, w.profile, w.source_version, w.orientation, w.width, w.height,
	  w.bytes, w.sha256, w.color, w.file, p.width, p.height`+wallCurrent+`
	  AND (?1 = '' OR w.orientation = ?1) AND (?2 = '' OR w.profile = ?2) AND w.width >= ?3 AND w.height >= ?4`,
		f.orientation, f.profile, f.minW, f.minH)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wallpaper
	for rows.Next() {
		var v wallpaper
		if err := rows.Scan(&v.PhotoID, &v.Profile, &v.SourceVersion, &v.Orientation, &v.Width, &v.Height,
			&v.Bytes, &v.SHA256, &v.Color, &v.File, &v.PhotoW, &v.PhotoH); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

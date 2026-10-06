package main

import (
	"database/sql"
	"math/rand/v2"
)

type wallCandidate struct {
	profile, orientation string
	width, height        int
}

func (v wallCandidate) matches(f wallFilter) bool {
	return (f.profile == "" || f.profile == v.profile) &&
		(f.orientation == "" || f.orientation == v.orientation) &&
		v.width >= f.minW && v.height >= f.minH
}

type wallGroup struct {
	id       int64
	variants []wallCandidate
}

type wallIndex struct {
	snapshot uint64
	photos   []wallGroup
}

type wallSelectionKey struct {
	snapshot uint64
	filter   wallFilter
}

func (s *store) wallIndex() (*wallIndex, error) {
	return s.cache.walls.get(&s.cache.wallRevision, func(uint64) (*wallIndex, error) {
		rows, err := s.db.Query(`SELECT w.photo_id, w.profile, w.orientation, w.width, w.height` +
			wallCurrent + ` ORDER BY w.photo_id, w.profile`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		index := &wallIndex{snapshot: s.cache.walls.builds.Load()}
		for rows.Next() {
			var id int64
			var v wallCandidate
			if err := rows.Scan(&id, &v.profile, &v.orientation, &v.width, &v.height); err != nil {
				return nil, err
			}
			// Share the strings repeated in every generated wallpaper row.
			switch v.profile {
			case "desktop-3840":
				v.profile = "desktop-3840"
			case "mobile-1440":
				v.profile = "mobile-1440"
			}
			switch v.orientation {
			case "landscape":
				v.orientation = "landscape"
			case "portrait":
				v.orientation = "portrait"
			case "square":
				v.orientation = "square"
			}
			last := len(index.photos) - 1
			if last < 0 || index.photos[last].id != id {
				index.photos = append(index.photos, wallGroup{id: id})
				last++
			}
			index.photos[last].variants = append(index.photos[last].variants, v)
		}
		return index, rows.Err()
	})
}

func (s *store) wallSelection(index *wallIndex, f wallFilter) ([]int, error) {
	key := wallSelectionKey{index.snapshot, f}
	return s.cache.wallFilters.get(key, wallFilterLimits, func() ([]int, error) {
		var selected []int
		for i, group := range index.photos {
			for _, variant := range group.variants {
				if variant.matches(f) {
					selected = append(selected, i)
					break
				}
			}
		}
		return selected, nil
	}, func(selected []int) int64 {
		return int64(cap(selected))*8 + int64(len(f.profile)+len(f.orientation)) + 192
	})
}

// Uniform over matching photos, then uniform over that photo's matching
// variants. Details are fetched live, so an old candidate cannot serve an
// invalid version, a deleted photo, or a photo that no longer matches.
func (s *store) randomWallpaper(f wallFilter) (wallpaper, bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		index, err := s.wallIndex()
		if err != nil {
			return wallpaper{}, false, err
		}
		selected, err := s.wallSelection(index, f)
		if err != nil {
			return wallpaper{}, false, err
		}
		if len(selected) == 0 {
			return wallpaper{}, false, nil
		}
		group := index.photos[selected[rand.IntN(len(selected))]]
		var chosen wallCandidate
		matches := 0
		for _, v := range group.variants {
			if v.matches(f) {
				matches++
				if rand.IntN(matches) == 0 {
					chosen = v
				}
			}
		}
		v, ok, err := s.currentWallpaper(group.id, chosen.profile, f)
		if err != nil {
			return wallpaper{}, false, err
		}
		if ok {
			return v, true, nil
		}
		// Handles a write racing this request, including external repairs.
		s.cache.wallRevision.Add(1)
	}
	return wallpaper{}, false, nil
}

func (s *store) currentWallpaper(id int64, profile string, f wallFilter) (wallpaper, bool, error) {
	var v wallpaper
	err := s.db.QueryRow(`SELECT w.photo_id, w.profile, w.source_version, w.orientation, w.width, w.height,
 w.bytes, w.sha256, w.color, w.file, p.width, p.height`+wallCurrent+`
 AND w.photo_id = ? AND w.profile = ?
 AND (? = '' OR w.orientation = ?) AND (? = '' OR w.profile = ?)
 AND w.width >= ? AND w.height >= ?`,
		id, profile, f.orientation, f.orientation, f.profile, f.profile, f.minW, f.minH).
		Scan(&v.PhotoID, &v.Profile, &v.SourceVersion, &v.Orientation, &v.Width, &v.Height,
			&v.Bytes, &v.SHA256, &v.Color, &v.File, &v.PhotoW, &v.PhotoH)
	if err == sql.ErrNoRows {
		return wallpaper{}, false, nil
	}
	return v, err == nil, err
}

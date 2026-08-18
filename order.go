package main

import (
	"sort"
	"strconv"
	"strings"
)

func photoRank(seed, id int64) uint64 {
	h := uint64(seed) ^ 0x9e3779b97f4a7c15
	h ^= uint64(id) * 0xbf58476d1ce4e5b9
	h ^= h >> 30
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
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
	if limit <= 0 || limit > 80 {
		limit = 40
	}
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

func parseCursor(after string) (rank uint64, id int64) {
	if after == "" {
		return 0, 0
	}
	parts := strings.SplitN(after, "-", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	rank, _ = strconv.ParseUint(parts[0], 10, 64)
	id, _ = strconv.ParseInt(parts[1], 10, 64)
	return rank, id
}

func formatCursor(seed int64, p photo) string {
	return strconv.FormatUint(photoRank(seed, p.ID), 10) + "-" + strconv.FormatInt(p.ID, 10)
}

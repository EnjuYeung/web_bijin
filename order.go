package main

import (
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

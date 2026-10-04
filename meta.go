package main

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func photoTitle(rel string) string {
	return filepath.Base(filepath.FromSlash(rel))
}

func photoFormat(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".jpg", ".jpeg":
		return "JPEG"
	case ".png":
		return "PNG"
	case ".webp":
		return "WEBP"
	case ".gif":
		return "GIF"
	default:
		ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(rel), "."))
		if ext == "" {
			return ""
		}
		return ext
	}
}

var dateLocations sync.Map

func formatDateInTZ(unix int64, tz string) (date string, year int) {
	cached, ok := dateLocations.Load(tz)
	if !ok {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			loc = time.Local
		}
		cached, _ = dateLocations.LoadOrStore(tz, loc)
	}
	loc := cached.(*time.Location)
	t := time.Unix(unix, 0).In(loc)
	return t.Format("2006-01-02"), t.Year()
}

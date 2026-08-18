package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
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

func humanBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatDateInTZ(unix int64, tz string) (date string, year int) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.Local
	}
	t := time.Unix(unix, 0).In(loc)
	return t.Format("2006-01-02"), t.Year()
}

func splitByOrient(photos []photo, portrait bool) (match, rest []photo) {
	for _, p := range photos {
		if p.Broken || p.Width <= 0 || p.Height <= 0 {
			continue
		}
		isPort := p.Height > p.Width
		isLand := p.Width > p.Height
		if (portrait && isPort) || (!portrait && isLand) {
			match = append(match, p)
			continue
		}
		rest = append(rest, p)
	}
	return match, rest
}

func pickBackground(photos []photo, portrait bool) (photo, bool) {
	match, rest := splitByOrient(photos, portrait)
	pool := match
	if len(pool) == 0 {
		pool = rest
	}
	if len(pool) == 0 {
		return photo{}, false
	}
	return pool[rand.Intn(len(pool))], true
}

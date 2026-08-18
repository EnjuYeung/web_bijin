package main

import (
	"path"
	"sort"
	"strings"
)

const rootAlbumID = "."

type albumSummary struct {
	ID    string
	Name  string
	Count int
	Cover photo
}

func photoAlbumID(rel string) string {
	dir := path.Dir(path.Clean(rel))
	if dir == "" {
		return rootAlbumID
	}
	return dir
}

func albumDisplayName(id string) string {
	if id == rootAlbumID {
		return "根目录"
	}
	return path.Base(id)
}

func validAlbumID(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") {
		return "", false
	}
	clean := path.Clean(raw)
	if clean != raw || clean == ".." || strings.HasPrefix(clean, "../") || hiddenRel(clean+"/photo.jpg") {
		return "", false
	}
	return clean, true
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

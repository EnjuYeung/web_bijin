package main

import (
	"path"
	"strings"
)

const rootAlbumID = "."

type albumSummary struct {
	ID    string
	Name  string
	Count int
	Cover photo
	// Added is the earliest photo time: the upload time in object storage,
	// the modification time for local files.
	Added int64
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

package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed all:web
var webEmbed embed.FS

func newRouter(st *store, sc *scanner, thumbs *thumbCache, sources *sourceSet, tz string, gate *authGate) http.Handler {
	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"tz":     tz,
			"status": sc.snapshot(),
		})
	}))
	mux.Handle("GET /api/login-bg", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleLoginBg(w, r, st, sources)
	}))
	mux.Handle("POST /api/login", http.HandlerFunc(gate.handleLogin))
	mux.Handle("GET /login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate.signedIn(r) {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFileFS(w, r, webFS, "login.html")
	}))
	mux.Handle("GET /_next/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		info, err := fs.Stat(webFS, name)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			if compressed, err := fs.Stat(webFS, name+".gz"); err == nil && compressed.Mode().IsRegular() {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
				name += ".gz"
			}
		}
		http.ServeFileFS(w, r, webFS, name)
	}))
	mux.Handle("GET /favicon.svg", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, webFS, "favicon.svg")
	}))
	mux.Handle("GET /api/photos", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleList(w, r, st, sc, tz)
	})))
	mux.Handle("GET /api/albums", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleAlbums(w, r, st, sc, tz)
	})))
	mux.Handle("GET /thumb/{id}", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleThumb(w, r, st, thumbs)
	})))
	mux.Handle("GET /original/{id}", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleOriginal(w, r, st, sources)
	})))
	settings := &settingsAPI{store: st, scanner: sc, sources: sources}
	mux.Handle("GET /api/settings", gate.protect(http.HandlerFunc(settings.get)))
	mux.Handle("POST /api/storages", gate.protect(http.HandlerFunc(settings.create)))
	mux.Handle("PUT /api/storages/{id}", gate.protect(http.HandlerFunc(settings.update)))
	mux.Handle("DELETE /api/storages/{id}", gate.protect(http.HandlerFunc(settings.remove)))
	mux.Handle("POST /api/storages/test", gate.protect(http.HandlerFunc(settings.test)))
	mux.Handle("GET /{$}", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFileFS(w, r, webFS, "index.html")
	})))
	return withLog(mux)
}

func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		parts := strings.Split(encoding, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(key, "q") {
				quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || quality <= 0 || quality > 1 {
					return false
				}
			}
		}
		return true
	}
	return false
}

func handleList(w http.ResponseWriter, r *http.Request, st *store, sc *scanner, tz string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	seed, _ := strconv.ParseInt(r.URL.Query().Get("seed"), 10, 64)
	if seed == 0 {
		seed = time.Now().UnixNano()
		if seed < 0 {
			seed = -seed
		}
		if seed == 0 {
			seed = 1
		}
	}
	afterRank, afterID := parseCursor(r.URL.Query().Get("after"))
	all, err := st.listOK()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "list failed"})
		return
	}
	var selectedAlbum *albumRef
	if r.URL.Query().Has("album") {
		albumID, ok := validAlbumID(r.URL.Query().Get("album"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid album"})
			return
		}
		all = filterAlbum(all, albumID)
		selectedAlbum = &albumRef{ID: albumID, Name: albumDisplayName(albumID)}
	}
	sortByRank(all, seed)
	photos := pageAfter(all, seed, afterRank, afterID, limit)
	out := make([]photoItem, 0, len(photos))
	for _, p := range photos {
		out = append(out, makePhotoItem(p, tz))
	}
	var next *string
	if len(photos) == pickLimit(limit) {
		c := formatCursor(seed, photos[len(photos)-1])
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"photos": out,
		"total":  len(all),
		"next":   next,
		"seed":   strconv.FormatInt(seed, 10),
		"tz":     tz,
		"status": sc.snapshot(),
		"album":  selectedAlbum,
	})
}

type albumRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type photoItem struct {
	ID     int64  `json:"id"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Format string `json:"format"`
	Size   int64  `json:"size"`
	Thumb  string `json:"thumb"`
	Src    string `json:"src"`
	Mtime  int64  `json:"mtime"`
	Date   string `json:"date"`
	Year   int    `json:"year"`
}

func makePhotoItem(p photo, tz string) photoItem {
	date, year := formatDateInTZ(p.MtimeUnix, tz)
	return photoItem{
		ID:     p.ID,
		W:      p.Width,
		H:      p.Height,
		Name:   filepath.ToSlash(p.RelPath),
		Title:  photoTitle(p.RelPath),
		Format: photoFormat(p.RelPath),
		Size:   p.Size,
		Thumb:  fmt.Sprintf("/thumb/%d?v=%s", p.ID, photoVersion(p)),
		Src:    fmt.Sprintf("/original/%d?v=%s", p.ID, photoVersion(p)),
		Mtime:  p.MtimeUnix,
		Date:   date,
		Year:   year,
	}
}

func handleAlbums(w http.ResponseWriter, r *http.Request, st *store, sc *scanner, tz string) {
	all, err := st.listOK()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "list failed"})
		return
	}
	type item struct {
		ID    string    `json:"id"`
		Name  string    `json:"name"`
		Count int       `json:"count"`
		Cover photoItem `json:"cover"`
	}
	albums := groupAlbums(all)
	out := make([]item, 0, len(albums))
	for _, a := range albums {
		out = append(out, item{
			ID:    a.ID,
			Name:  a.Name,
			Count: a.Count,
			Cover: makePhotoItem(a.Cover, tz),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"albums": out,
		"total":  len(out),
		"tz":     tz,
		"status": sc.snapshot(),
	})
}

func pickLimit(limit int) int {
	if limit <= 0 || limit > 80 {
		return 40
	}
	return limit
}

func handleLoginBg(w http.ResponseWriter, r *http.Request, st *store, source imageOpener) {
	all, err := st.listOK()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	portrait := r.URL.Query().Get("orient") == "port"
	p, ok := pickBackground(all, portrait)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := source.Open(r.Context(), p.sourceKey())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(p.RelPath), time.Time{}, f)
}

func handleThumb(w http.ResponseWriter, r *http.Request, st *store, thumbs *thumbCache) {
	p, ok := photoFromReq(w, r, st)
	if !ok {
		return
	}
	if p.Broken {
		http.NotFound(w, r)
		return
	}
	etag := fmt.Sprintf(`"%d-%s"`, p.ID, photoVersion(p))
	w.Header().Set("ETag", etag)
	// Thumbnail URLs carry the source version, so a changed photo gets a new
	// URL and the browser never has to revalidate an old one.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	b, err := thumbs.serveBytes(r.Context(), p)
	if err != nil {
		http.Error(w, "thumb failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// originalLinker is implemented by sources that can send the browser straight
// to object storage for an original.
type originalLinker interface {
	originalLink(ctx context.Context, key, version string) (link string, ok bool, err error)
}

func handleOriginal(w http.ResponseWriter, r *http.Request, st *store, source imageOpener) {
	p, ok := photoFromReq(w, r, st)
	if !ok {
		return
	}
	if linker, ok := source.(originalLinker); ok {
		link, direct, err := linker.originalLink(r.Context(), p.sourceKey(), photoVersion(p))
		if err != nil {
			slog.Warn("presign original, streaming instead", "id", p.ID, "err", err)
		} else if direct {
			// The link stays valid well beyond an hour, so reopening a photo
			// skips this request as well.
			w.Header().Set("Cache-Control", "private, max-age=3600")
			http.Redirect(w, r, link, http.StatusFound)
			return
		}
	}
	f, err := source.Open(r.Context(), p.sourceKey())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	etag := fmt.Sprintf(`"%d-%s-orig"`, p.ID, photoVersion(p))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, filepath.Base(p.RelPath), time.Time{}, f)
}

func photoFromReq(w http.ResponseWriter, r *http.Request, st *store) (photo, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return photo{}, false
	}
	p, ok, err := st.getByID(id)
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return photo{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return photo{}, false
	}
	return p, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(lw, r)
		if (strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/health") || lw.code >= 400 {
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "code", lw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func photoVersion(p photo) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", p.SourceVersion, p.Size, p.MtimeUnix)))
	return fmt.Sprintf("%x", sum[:12])
}

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webEmbed embed.FS

func newRouter(st *store, sc *scanner, thumbs *thumbCache, photosDir, tz string, gate *authGate) http.Handler {
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
		handleLoginBg(w, r, st, photosDir)
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
	mux.Handle("GET /app.css", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, webFS, "app.css")
	}))
	mux.Handle("GET /app.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, webFS, "app.js")
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
		handleOriginal(w, r, st, photosDir)
	})))
	mux.Handle("GET /{$}", gate.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFileFS(w, r, webFS, "index.html")
	})))
	return withLog(mux)
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
		Thumb:  fmt.Sprintf("/thumb/%d", p.ID),
		Src:    fmt.Sprintf("/original/%d", p.ID),
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

func handleLoginBg(w http.ResponseWriter, r *http.Request, st *store, photosDir string) {
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
	full, err := safeRelPath(photosDir, filepath.FromSlash(p.RelPath))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(p.RelPath), stat.ModTime(), f)
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
	etag := fmt.Sprintf(`"%d-%d-%d"`, p.ID, p.Size, p.MtimeUnix)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	b, err := thumbs.serveBytes(p)
	if err != nil {
		http.Error(w, "thumb failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

func handleOriginal(w http.ResponseWriter, r *http.Request, st *store, photosDir string) {
	p, ok := photoFromReq(w, r, st)
	if !ok {
		return
	}
	full, err := safeRelPath(photosDir, filepath.FromSlash(p.RelPath))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	etag := fmt.Sprintf(`"%d-%d-%d-orig"`, p.ID, p.Size, p.MtimeUnix)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(p.RelPath), stat.ModTime(), f)
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
		if strings.HasPrefix(r.URL.Path, "/api/") || lw.code >= 400 {
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

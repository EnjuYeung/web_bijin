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

func newRouter(st *store, sc *scanner, thumbs *thumbCache, photosDir string) http.Handler {
	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": sc.snapshot(),
		})
	}))
	mux.Handle("GET /api/photos", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleList(w, r, st, sc)
	}))
	mux.Handle("GET /thumb/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleThumb(w, r, st, thumbs)
	}))
	mux.Handle("GET /original/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleOriginal(w, r, st, photosDir)
	}))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			name := strings.TrimPrefix(r.URL.Path, "/")
			if _, err := fs.Stat(webFS, name); err == nil && !strings.Contains(name, "..") {
				http.FileServer(http.FS(webFS)).ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, webFS, "index.html")
	}))
	return withLog(mux)
}

func handleList(w http.ResponseWriter, r *http.Request, st *store, sc *scanner) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var afterMtime, afterID int64
	if after := r.URL.Query().Get("after"); after != "" {
		parts := strings.SplitN(after, "-", 2)
		if len(parts) == 2 {
			afterMtime, _ = strconv.ParseInt(parts[0], 10, 64)
			afterID, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	photos, err := st.list(afterMtime, afterID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "list failed"})
		return
	}
	total, err := st.countOK()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "count failed"})
		return
	}
	type item struct {
		ID    int64  `json:"id"`
		W     int    `json:"w"`
		H     int    `json:"h"`
		Name  string `json:"name"`
		Thumb string `json:"thumb"`
		Src   string `json:"src"`
		Mtime int64  `json:"mtime"`
	}
	out := make([]item, 0, len(photos))
	for _, p := range photos {
		out = append(out, item{
			ID:    p.ID,
			W:     p.Width,
			H:     p.Height,
			Name:  filepath.ToSlash(p.RelPath),
			Thumb: fmt.Sprintf("/thumb/%d", p.ID),
			Src:   fmt.Sprintf("/original/%d", p.ID),
			Mtime: p.MtimeUnix,
		})
	}
	var next *string
	if len(photos) == pickLimit(limit) {
		c := fmt.Sprintf("%d-%d", photos[len(photos)-1].MtimeUnix, photos[len(photos)-1].ID)
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"photos": out,
		"total":  total,
		"next":   next,
		"status": sc.snapshot(),
	})
}

func pickLimit(limit int) int {
	if limit <= 0 || limit > 80 {
		return 40
	}
	return limit
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

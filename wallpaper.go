package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/webp"
)

// Random wallpapers: besides its thumbnail, every displayable photo except a
// GIF is kept as a screen-sized WebP that other websites may use as a
// background through the public /v1/backgrounds/random API. Profiles, the
// square rule and the URL shapes are those nas-background served before it
// was merged in here, so the sites using it keep working.

type wallProfile struct {
	Name        string
	Orientation string
	W, H        int
}

var wallProfiles = []wallProfile{
	{Name: "desktop-3840", Orientation: "landscape", W: 3840, H: 2160},
	{Name: "mobile-1440", Orientation: "portrait", W: 1440, H: 2560},
}

// Quality 80 and method 4 are the Sharp settings nas-background used.
var wallOptions = webp.Options{Quality: 80, Method: 4}

// wallOrientation counts shapes within 10% of square as square; those get
// both profiles.
func wallOrientation(w, h int) string {
	ratio := float64(w) / float64(h)
	switch {
	case ratio >= 0.9 && ratio <= 1.1:
		return "square"
	case w > h:
		return "landscape"
	}
	return "portrait"
}

func wallProfilesFor(w, h int) []wallProfile {
	orientation := wallOrientation(w, h)
	var out []wallProfile
	for _, p := range wallProfiles {
		if orientation == "square" || p.Orientation == orientation {
			out = append(out, p)
		}
	}
	return out
}

// fitInside scales w×h into the box without enlarging, rounding like Sharp.
func fitInside(w, h, boxW, boxH int) (int, int) {
	if w <= boxW && h <= boxH {
		return w, h
	}
	if float64(w)/float64(h) > float64(boxW)/float64(boxH) {
		return boxW, max(1, int(math.Round(float64(h)*float64(boxW)/float64(w))))
	}
	return max(1, int(math.Round(float64(w)*float64(boxH)/float64(h)))), boxH
}

// wantsWallpaper leaves out GIFs, which are mostly animations.
func wantsWallpaper(rel string) bool {
	return isImageName(rel) && !strings.EqualFold(path.Ext(rel), ".gif")
}

// wallpaper is one stored wallpaper of a photo version.
type wallpaper struct {
	PhotoID       int64
	Profile       string
	SourceVersion string
	Orientation   string
	Width, Height int
	Bytes         int64
	SHA256        string
	Color         string
	File          string
	// PhotoW and PhotoH are the photo's own size, for the JSON answer.
	PhotoW, PhotoH int
}

// mediaPath is the public, immutable URL path; it changes with the content.
func (w wallpaper) mediaPath() string {
	return "/media/sha256/" + w.SHA256[:2] + "/" + w.SHA256 + ".webp"
}

func wallFile(p photo, profile string) string {
	return fmt.Sprintf("%d-%s-%s.webp", p.ID, photoVersion(p), profile)
}

// wallpapersReady reports whether a photo version has all its wallpapers.
func (s *scanner) wallpapersReady(p photo) (bool, error) {
	if p.Broken || !wantsWallpaper(p.RelPath) {
		return true, nil
	}
	rows, err := s.store.wallpapersOf(p.ID)
	if err != nil {
		return false, err
	}
	want := wallProfilesFor(p.Width, p.Height)
	if len(rows) != len(want) {
		return false, nil
	}
	have := make(map[string]wallpaper, len(rows))
	for _, r := range rows {
		have[r.Profile] = r
	}
	for _, prof := range want {
		r, ok := have[prof.Name]
		if !ok || r.SourceVersion != p.SourceVersion || r.File != wallFile(p, prof.Name) {
			return false, nil
		}
		if info, err := os.Stat(filepath.Join(s.thumbs.wallDir, r.File)); err != nil || !info.Mode().IsRegular() {
			return false, nil
		}
	}
	return true, nil
}

// saveWallpapers encodes the wallpapers of a committed photo version from its
// decoded image and replaces the photo's rows; files of earlier versions go
// afterwards. Caller holds the photo's key lock and a generation slot.
func (s *scanner) saveWallpapers(ctx context.Context, p photo, img image.Image) error {
	if !wantsWallpaper(p.RelPath) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	dir := s.thumbs.wallDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	orientation := wallOrientation(w, h)
	var rows []wallpaper
	keep := map[string]bool{}
	color := ""
	for _, prof := range wallProfilesFor(w, h) {
		if err := ctx.Err(); err != nil {
			return err
		}
		ww, wh := fitInside(w, h, prof.W, prof.H)
		out := img
		if ww != w || wh != h {
			out = imaging.Resize(img, ww, wh, imaging.Lanczos)
		}
		var buf bytes.Buffer
		if err := webp.Encode(&buf, out, wallOptions); err != nil {
			return fmt.Errorf("encode wallpaper: %w", err)
		}
		if color == "" {
			px := imaging.Resize(out, 1, 1, imaging.Box).Pix
			color = fmt.Sprintf("#%02x%02x%02x", px[0], px[1], px[2])
		}
		file := wallFile(p, prof.Name)
		if err := writeFileAtomic(filepath.Join(dir, file), buf.Bytes()); err != nil {
			return err
		}
		keep[file] = true
		sum := sha256.Sum256(buf.Bytes())
		rows = append(rows, wallpaper{PhotoID: p.ID, Profile: prof.Name, SourceVersion: p.SourceVersion, Orientation: orientation,
			Width: ww, Height: wh, Bytes: int64(buf.Len()), SHA256: hex.EncodeToString(sum[:]), Color: color, File: file})
	}
	// Encoding cannot be interrupted, so a late finish is caught here.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.store.replaceWallpapers(p.ID, rows); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%d-*.webp", p.ID)))
	for _, f := range old {
		if !keep[filepath.Base(f)] {
			_ = os.Remove(f)
		}
	}
	return nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// wallID is a stable UUID-shaped id of one photo version (UUIDv8 layout).
func wallID(v wallpaper) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("bijin-wallpaper|%d|%s", v.PhotoID, v.SourceVersion)))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var (
	wallSHA     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	wallName    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	wallNumber  = regexp.MustCompile(`^[0-9]{1,6}$`)
	wallOrients = map[string]bool{"landscape": true, "portrait": true, "square": true}
)

// parseWallQuery reads the parameters nas-background accepted. Unknown ones
// are ignored, so a page may add one to tell two backgrounds apart.
func parseWallQuery(q url.Values) (f wallFilter, format string, err error) {
	one := func(name string, valid func(string) bool) (string, error) {
		values := q[name]
		if len(values) == 0 {
			return "", nil
		}
		if len(values) > 1 || !valid(values[0]) {
			return "", fmt.Errorf("invalid %s", name)
		}
		return values[0], nil
	}
	size := func(name string) (int, error) {
		v, err := one(name, func(v string) bool {
			n, err := strconv.Atoi(v)
			return wallNumber.MatchString(v) && err == nil && n >= 1 && n <= 100_000
		})
		if v == "" || err != nil {
			return 0, err
		}
		return strconv.Atoi(v)
	}
	if f.orientation, err = one("orientation", func(v string) bool { return wallOrients[v] }); err != nil {
		return
	}
	if f.profile, err = one("profile", wallName.MatchString); err != nil {
		return
	}
	if f.minW, err = size("minWidth"); err != nil {
		return
	}
	if f.minH, err = size("minHeight"); err != nil {
		return
	}
	if format, err = one("format", func(v string) bool { return v == "redirect" || v == "json" }); err != nil {
		return
	}
	if format == "" {
		format = "redirect"
	}
	return f, format, nil
}

// publicHeaders lets any website embed or fetch wallpapers: they carry no
// cookies and nothing private.
func publicHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
}

func handleWallpaperPreflight(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, If-None-Match")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// handleRandomWallpaper redirects to a random wallpaper by default, or
// describes it with format=json; 204 means nothing matches.
func handleRandomWallpaper(w http.ResponseWriter, r *http.Request, st *store) {
	publicHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	f, format, err := parseWallQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	v, ok, err := st.randomWallpaper(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "list failed"})
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if format == "redirect" {
		http.Redirect(w, r, v.mediaPath(), http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"background": map[string]any{"id": wallID(v), "width": v.PhotoW, "height": v.PhotoH,
			"orientation": v.Orientation, "dominantColor": v.Color},
		"variant": map[string]any{"profile": v.Profile, "width": v.Width, "height": v.Height,
			"mime": "image/webp", "bytes": v.Bytes, "sha256": v.SHA256, "path": v.mediaPath()},
		"url": v.mediaPath(),
	})
}

// handleWallpaperMedia serves one current wallpaper by content hash. A
// removed or replaced photo's old URL is answered with 404.
func handleWallpaperMedia(w http.ResponseWriter, r *http.Request, st *store, dir string) {
	publicHeaders(w)
	sha, ok := strings.CutSuffix(r.PathValue("name"), ".webp")
	if !ok || !wallSHA.MatchString(sha) || r.PathValue("prefix") != sha[:2] {
		http.NotFound(w, r)
		return
	}
	file, found, err := st.wallpaperFile(sha)
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(dir, filepath.Base(file)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/webp")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+sha+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}

// wallFilter narrows the random choice; zero values match everything.
type wallFilter struct {
	orientation, profile string
	minW, minH           int
}

func (s *store) migrateWallpapers() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS wallpapers (
  photo_id INTEGER NOT NULL,
  profile TEXT NOT NULL,
  source_version TEXT NOT NULL,
  orientation TEXT NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  color TEXT NOT NULL,
  file TEXT NOT NULL,
  PRIMARY KEY (photo_id, profile)
);
CREATE INDEX IF NOT EXISTS wallpapers_sha256 ON wallpapers(sha256);
`)
	return err
}

// wallCurrent keeps wallpapers of removed, broken or changed photos out.
const wallCurrent = ` FROM wallpapers w JOIN photos p ON p.id = w.photo_id
 WHERE p.broken = 0 AND p.width > 0 AND p.height > 0 AND w.source_version = p.source_version`

func (s *store) wallpaperFile(sha string) (string, bool, error) {
	var file string
	err := s.db.QueryRow(`SELECT w.file`+wallCurrent+` AND w.sha256 = ? LIMIT 1`, sha).Scan(&file)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return file, err == nil, err
}

func (s *store) wallpapersOf(photoID int64) ([]wallpaper, error) {
	rows, err := s.db.Query(`SELECT photo_id, profile, source_version, orientation, width, height, bytes, sha256, color, file
	  FROM wallpapers WHERE photo_id = ?`, photoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wallpaper
	for rows.Next() {
		var v wallpaper
		if err := rows.Scan(&v.PhotoID, &v.Profile, &v.SourceVersion, &v.Orientation, &v.Width, &v.Height,
			&v.Bytes, &v.SHA256, &v.Color, &v.File); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *store) replaceWallpapers(photoID int64, list []wallpaper) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM wallpapers WHERE photo_id = ?`, photoID); err != nil {
		return err
	}
	for _, v := range list {
		if _, err := tx.Exec(`INSERT INTO wallpapers (photo_id, profile, source_version, orientation, width, height, bytes, sha256, color, file)
		  VALUES (?,?,?,?,?,?,?,?,?,?)`, photoID, v.Profile, v.SourceVersion, v.Orientation, v.Width, v.Height, v.Bytes, v.SHA256, v.Color, v.File); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cache.wallRevision.Add(1)
	return nil
}

func (s *store) deleteWallpapers(photoID int64) error {
	_, err := s.db.Exec(`DELETE FROM wallpapers WHERE photo_id = ?`, photoID)
	if err == nil {
		s.cache.wallRevision.Add(1)
	}
	return err
}

// wallStats is what the settings page shows about wallpapers.
type wallStats struct {
	Photos    int   `json:"photos"`
	Ready     int   `json:"ready"`
	Landscape int   `json:"landscape"`
	Portrait  int   `json:"portrait"`
	Square    int   `json:"square"`
	Bytes     int64 `json:"bytes"`
}

func (s *store) wallpaperStats() (wallStats, error) {
	return s.cache.wallStats.get(&s.cache.wallRevision, func(uint64) (wallStats, error) {
		return s.loadWallpaperStats()
	})
}

func (s *store) loadWallpaperStats() (wallStats, error) {
	var st wallStats
	tx, err := s.db.Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	photos, err := tx.Query("SELECT display_path FROM photos WHERE " + visiblePhotos)
	if err != nil {
		return st, err
	}
	for photos.Next() {
		var path string
		if err := photos.Scan(&path); err != nil {
			photos.Close()
			return st, err
		}
		if wantsWallpaper(path) {
			st.Photos++
		}
	}
	err = photos.Err()
	photos.Close()
	if err != nil {
		return st, err
	}
	rows, err := tx.Query(`SELECT w.photo_id, w.orientation, w.bytes` + wallCurrent)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	seen := map[int64]bool{}
	for rows.Next() {
		var id, size int64
		var orientation string
		if err := rows.Scan(&id, &orientation, &size); err != nil {
			return st, err
		}
		st.Bytes += size
		if seen[id] {
			continue
		}
		seen[id] = true
		st.Ready++
		switch orientation {
		case "landscape":
			st.Landscape++
		case "portrait":
			st.Portrait++
		default:
			st.Square++
		}
	}
	return st, rows.Err()
}

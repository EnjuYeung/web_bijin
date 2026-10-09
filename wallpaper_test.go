package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// noRedirect shows redirects instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func gradientJPEG(w, h int, c color.RGBA) []byte {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetRGBA(x, y, color.RGBA{c.R + uint8(x/3), c.G + uint8(y/3), c.B, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 85}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func tinyGIF() []byte {
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 40, 80), color.Palette{color.Black, color.White}), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func (m *memSource) openCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opens
}

type wallHarness struct {
	st  *store
	src *memSource
	th  *thumbCache
	sc  *scanner
	srv *httptest.Server
}

// newWallHarness serves the whole app over files scanned as the local photos.
func newWallHarness(t *testing.T, files map[string][]byte) *wallHarness {
	t.Helper()
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := newMemSource(files)
	set := newSourceSet(src)
	th := newThumbCache(filepath.Join(data, "thumbs"), filepath.Join(data, "wallpapers"), set, 2)
	sc := newScanner(config{MaxPixels: 64_000_000}, st, th, set)
	srv := httptest.NewServer(newRouter(st, sc, th, set, "Asia/Shanghai", testGate()))
	t.Cleanup(srv.Close)
	sc.run(context.Background())
	return &wallHarness{st: st, src: src, th: th, sc: sc, srv: srv}
}

func (h *wallHarness) photo(t *testing.T, rel string) photo {
	t.Helper()
	p, ok, err := h.st.getBySourceKey(rel)
	if err != nil || !ok {
		t.Fatalf("photo %s: ok=%v err=%v", rel, ok, err)
	}
	return p
}

func (h *wallHarness) walls(t *testing.T, rel string) []wallpaper {
	t.Helper()
	rows, err := h.st.wallpapersOf(h.photo(t, rel).ID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Profile < rows[j].Profile })
	return rows
}

// get requests path anonymously without following redirects.
func (h *wallHarness) get(t *testing.T, method, path string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

// random returns where /v1/backgrounds/random sent the browser.
func (h *wallHarness) random(t *testing.T, query string) string {
	t.Helper()
	res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random"+query, nil)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("random%s answered %d", query, res.StatusCode)
	}
	return res.Header.Get("Location")
}

func TestWallpaperGeometry(t *testing.T) {
	// Sizes taken from nas-background's Sharp output for real photos.
	cases := []struct {
		w, h        int
		orientation string
		want        []string
	}{
		{8256, 5504, "landscape", []string{"desktop-3840 3240x2160"}},
		{9000, 6209, "landscape", []string{"desktop-3840 3131x2160"}},
		{4640, 8256, "portrait", []string{"mobile-1440 1439x2560"}},
		{2731, 4275, "portrait", []string{"mobile-1440 1440x2254"}},
		{1705, 2560, "portrait", []string{"mobile-1440 1440x2162"}},
		{7000, 7000, "square", []string{"desktop-3840 2160x2160", "mobile-1440 1440x1440"}},
		{750, 750, "square", []string{"desktop-3840 750x750", "mobile-1440 750x750"}},
		{1100, 1000, "square", []string{"desktop-3840 1100x1000", "mobile-1440 1100x1000"}},
		{900, 1000, "square", []string{"desktop-3840 900x1000", "mobile-1440 900x1000"}},
		{1111, 1000, "landscape", []string{"desktop-3840 1111x1000"}},
		{899, 1000, "portrait", []string{"mobile-1440 899x1000"}},
	}
	for _, c := range cases {
		if got := wallOrientation(c.w, c.h); got != c.orientation {
			t.Errorf("%dx%d orientation %s, want %s", c.w, c.h, got, c.orientation)
		}
		var got []string
		for _, p := range wallProfilesFor(c.w, c.h) {
			w, h := fitInside(c.w, c.h, p.W, p.H)
			got = append(got, fmt.Sprintf("%s %dx%d", p.Name, w, h))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%dx%d wallpapers %v, want %v", c.w, c.h, got, c.want)
		}
	}
	for rel, want := range map[string]bool{"a.jpg": true, "b.JPEG": true, "c.png": true, "d.webp": true, "e.gif": false, "f.GIF": false, "g.txt": false} {
		if wantsWallpaper(rel) != want {
			t.Errorf("wantsWallpaper(%s) = %v", rel, !want)
		}
	}
}

func lifecycleFiles() map[string][]byte {
	return map[string][]byte{
		"横/wide.jpg":   gradientJPEG(640, 360, color.RGBA{180, 40, 30, 255}),
		"竖/tall.jpg":   gradientJPEG(360, 640, color.RGBA{30, 60, 170, 255}),
		"方/square.jpg": gradientJPEG(500, 520, color.RGBA{40, 150, 90, 255}),
		"动图/dot.gif":   tinyGIF(),
		"broken.jpg":   []byte("this is not a jpeg at all"),
	}
}

// TestWallpaperLifecycle follows nas-background's acceptance order: make the
// wallpapers, read them through the API, replace a photo, delete it.
func TestWallpaperLifecycle(t *testing.T) {
	h := newWallHarness(t, lifecycleFiles())
	ctx := context.Background()

	// 1. Made from real images, with the right profiles and sizes.
	want := map[string][]string{
		"横/wide.jpg":   {"desktop-3840 640x360 landscape"},
		"竖/tall.jpg":   {"mobile-1440 360x640 portrait"},
		"方/square.jpg": {"desktop-3840 500x520 square", "mobile-1440 500x520 square"},
		"动图/dot.gif":   nil,
		"broken.jpg":   nil,
	}
	for rel, profiles := range want {
		var got []string
		for _, w := range h.walls(t, rel) {
			got = append(got, fmt.Sprintf("%s %dx%d %s", w.Profile, w.Width, w.Height, w.Orientation))
			raw, err := os.ReadFile(filepath.Join(h.th.wallDir, w.File))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
			if err != nil || format != "webp" || cfg.Width != w.Width || cfg.Height != w.Height {
				t.Fatalf("%s %s: %s %dx%d err=%v", rel, w.Profile, format, cfg.Width, cfg.Height, err)
			}
			if hex.EncodeToString(sum[:]) != w.SHA256 || int64(len(raw)) != w.Bytes || !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(w.Color) {
				t.Fatalf("%s %s row does not match its file: %+v", rel, w.Profile, w)
			}
		}
		if !slices.Equal(got, profiles) {
			t.Fatalf("%s wallpapers %v, want %v", rel, got, profiles)
		}
	}
	if !h.photo(t, "broken.jpg").Broken {
		t.Fatal("broken.jpg should be recorded as unreadable")
	}

	// 2. Read back through the public API without logging in.
	wide := h.walls(t, "横/wide.jpg")[0]
	tall := h.walls(t, "竖/tall.jpg")[0]
	square := h.walls(t, "方/square.jpg")
	res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random?orientation=landscape", nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != wide.mediaPath() ||
		res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("random landscape: %d %v", res.StatusCode, res.Header)
	}
	res, body := h.get(t, http.MethodGet, wide.mediaPath(), nil)
	sum := sha256.Sum256(body)
	if res.StatusCode != http.StatusOK || hex.EncodeToString(sum[:]) != wide.SHA256 ||
		res.Header.Get("Content-Type") != "image/webp" || res.Header.Get("ETag") != `"`+wide.SHA256+`"` ||
		res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("media: %d %v", res.StatusCode, res.Header)
	}
	if res, body := h.get(t, http.MethodHead, wide.mediaPath(), nil); res.StatusCode != http.StatusOK || len(body) != 0 || res.ContentLength != wide.Bytes {
		t.Fatalf("HEAD media: %d len %d body %d", res.StatusCode, res.ContentLength, len(body))
	}
	res, body = h.get(t, http.MethodGet, wide.mediaPath(), map[string]string{"Range": "bytes=0-9"})
	if res.StatusCode != http.StatusPartialContent || len(body) != 10 || res.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-9/%d", wide.Bytes) {
		t.Fatalf("range: %d %d %v", res.StatusCode, len(body), res.Header)
	}
	if res, _ := h.get(t, http.MethodGet, wide.mediaPath(), map[string]string{"If-None-Match": `"` + wide.SHA256 + `"`}); res.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation answered %d", res.StatusCode)
	}

	res, body = h.get(t, http.MethodGet, "/v1/backgrounds/random?orientation=portrait&format=json", nil)
	var answer struct {
		Background struct {
			ID            string `json:"id"`
			Width, Height int
			Orientation   string `json:"orientation"`
			DominantColor string `json:"dominantColor"`
		} `json:"background"`
		Variant struct {
			Profile       string `json:"profile"`
			Width, Height int
			Mime          string `json:"mime"`
			Bytes         int64  `json:"bytes"`
			SHA256        string `json:"sha256"`
			Path          string `json:"path"`
		} `json:"variant"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("json: %d %s %v", res.StatusCode, body, err)
	}
	b, v := answer.Background, answer.Variant
	if v.Profile != "mobile-1440" || v.Width != 360 || v.Height != 640 || v.Mime != "image/webp" || v.Bytes != tall.Bytes ||
		v.SHA256 != tall.SHA256 || v.Path != tall.mediaPath() || answer.URL != tall.mediaPath() ||
		b.Width != 360 || b.Height != 640 || b.Orientation != "portrait" || b.DominantColor != tall.Color ||
		!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(b.ID) {
		t.Fatalf("json answer %s", body)
	}

	squarePaths := []string{square[0].mediaPath(), square[1].mediaPath()}
	for query, allowed := range map[string][]string{
		"?orientation=square":                 squarePaths,
		"?profile=mobile-1440":                {tall.mediaPath(), square[1].mediaPath()},
		"?minWidth=600":                       {wide.mediaPath()},
		"?minHeight=600":                      {tall.mediaPath()},
		"?orientation=landscape&t=1700000000": {wide.mediaPath()},
	} {
		for i := 0; i < 8; i++ {
			if got := h.random(t, query); !slices.Contains(allowed, got) {
				t.Fatalf("random%s gave %s, want one of %v", query, got, allowed)
			}
		}
	}
	for _, query := range []string{"?minWidth=5000", "?profile=desktop-9999", "?orientation=landscape&minHeight=361"} {
		if res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random"+query, nil); res.StatusCode != http.StatusNoContent {
			t.Fatalf("random%s answered %d, want 204", query, res.StatusCode)
		}
	}
	for _, query := range []string{"?orientation=diagonal", "?minWidth=0", "?minWidth=abc", "?minWidth=1000000", "?minHeight=-5",
		"?format=xml", "?profile=Desktop", "?orientation=landscape&orientation=portrait"} {
		if res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random"+query, nil); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("random%s answered %d, want 400", query, res.StatusCode)
		}
	}
	// Each photo is equally likely; the square one's two sizes do not double it.
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		got := h.random(t, "")
		switch {
		case got == wide.mediaPath():
			counts["wide"]++
		case got == tall.mediaPath():
			counts["tall"]++
		case slices.Contains(squarePaths, got):
			counts["square"]++
		default:
			t.Fatalf("random gave unknown %s", got)
		}
	}
	for _, name := range []string{"wide", "tall", "square"} {
		if counts[name] < 60 || counts[name] > 140 {
			t.Fatalf("uneven random choice %v", counts)
		}
	}

	// Unchanged photos are not read again.
	opens := h.src.openCount()
	h.sc.run(ctx)
	if h.src.openCount() != opens {
		t.Fatalf("rescan read %d unchanged photo(s)", h.src.openCount()-opens)
	}

	// 3. A replaced photo gets new wallpapers and a new URL; the old one is gone.
	id := h.photo(t, "横/wide.jpg").ID
	h.src.set("横/wide.jpg", gradientJPEG(800, 400, color.RGBA{20, 200, 60, 255}))
	h.sc.run(ctx)
	renewed := h.walls(t, "横/wide.jpg")
	if h.photo(t, "横/wide.jpg").ID != id || len(renewed) != 1 || renewed[0].SHA256 == wide.SHA256 || renewed[0].Width != 800 || renewed[0].Height != 400 {
		t.Fatalf("replaced photo: %+v", renewed)
	}
	if res, _ := h.get(t, http.MethodGet, wide.mediaPath(), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("old media answered %d", res.StatusCode)
	}
	if got := h.random(t, "?orientation=landscape"); got != renewed[0].mediaPath() {
		t.Fatalf("random after replace %s", got)
	}
	if res, body := h.get(t, http.MethodGet, renewed[0].mediaPath(), nil); res.StatusCode != http.StatusOK || int64(len(body)) != renewed[0].Bytes {
		t.Fatalf("new media answered %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.th.wallDir, wide.File)); !os.IsNotExist(err) {
		t.Fatalf("old wallpaper file kept: %v", err)
	}

	// 4. A deleted photo leaves the API and the disk.
	h.src.remove("横/wide.jpg")
	h.sc.run(ctx)
	if res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random?orientation=landscape", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("random after delete answered %d", res.StatusCode)
	}
	if res, _ := h.get(t, http.MethodGet, renewed[0].mediaPath(), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted media answered %d", res.StatusCode)
	}
	if left, _ := filepath.Glob(filepath.Join(h.th.wallDir, fmt.Sprintf("%d-*", id))); len(left) != 0 {
		t.Fatalf("files left %v", left)
	}
	if rows, err := h.st.wallpapersOf(id); err != nil || len(rows) != 0 {
		t.Fatalf("rows left %v %v", rows, err)
	}
}

func TestWallpaperBackfillForIndexedPhotos(t *testing.T) {
	h := newWallHarness(t, map[string][]byte{
		"a.jpg":   gradientJPEG(300, 200, color.RGBA{90, 20, 20, 255}),
		"b.jpg":   gradientJPEG(200, 300, color.RGBA{20, 90, 20, 255}),
		"dot.gif": tinyGIF(),
	})
	// Forget the wallpapers, as for photos indexed before they existed.
	if _, err := h.st.db.Exec(`DELETE FROM wallpapers`); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(h.th.wallDir); err != nil {
		t.Fatal(err)
	}
	before, _ := h.st.listOK()
	thumbs := map[string]os.FileInfo{}
	for _, p := range before {
		info, err := os.Stat(h.th.path(p))
		if err != nil {
			t.Fatal(err)
		}
		thumbs[p.RelPath] = info
	}
	opens := h.src.openCount()

	h.sc.run(context.Background())
	if got := h.src.openCount() - opens; got != 2 {
		t.Fatalf("backfill read %d photos, want the 2 that take wallpapers", got)
	}
	after, _ := h.st.listOK()
	sort.Slice(before, func(i, j int) bool { return before[i].ID < before[j].ID })
	sort.Slice(after, func(i, j int) bool { return after[i].ID < after[j].ID })
	if !slices.Equal(before, after) {
		t.Fatalf("photos changed: %+v -> %+v", before, after)
	}
	for _, p := range after {
		info, err := os.Stat(h.th.path(p))
		if err != nil || !info.ModTime().Equal(thumbs[p.RelPath].ModTime()) || info.Size() != thumbs[p.RelPath].Size() {
			t.Fatalf("thumbnail of %s rewritten", p.RelPath)
		}
		if ready, err := h.sc.photos.wallpapersReady(p); err != nil || !ready {
			t.Fatalf("%s not backfilled: %v", p.RelPath, err)
		}
	}
	if len(h.walls(t, "a.jpg")) != 1 || len(h.walls(t, "b.jpg")) != 1 || len(h.walls(t, "dot.gif")) != 0 {
		t.Fatal("unexpected wallpapers after backfill")
	}
	h.sc.run(context.Background())
	if got := h.src.openCount() - opens; got != 2 {
		t.Fatalf("second scan read again: %d", got)
	}
}

func TestWallpaperFailureKeepsPhotoAndRetries(t *testing.T) {
	h := newWallHarness(t, map[string][]byte{})
	// A file where the wallpaper directory belongs makes every save fail.
	if err := os.WriteFile(h.th.wallDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.src.set("a.jpg", gradientJPEG(300, 200, color.RGBA{90, 20, 20, 255}))
	h.sc.run(context.Background())
	p := h.photo(t, "a.jpg")
	if p.Broken || !h.th.exists(p) || len(h.walls(t, "a.jpg")) != 0 {
		t.Fatalf("photo should be in the album without wallpapers: %+v", p)
	}
	if n, _ := h.st.countOK(); n != 1 {
		t.Fatalf("album has %d photos", n)
	}
	if s := h.sc.snapshot(); s.Failed != 1 || s.LastErr == "" {
		t.Fatalf("failure not reported: %+v", s)
	}
	if err := os.Remove(h.th.wallDir); err != nil {
		t.Fatal(err)
	}
	h.sc.run(context.Background())
	if len(h.walls(t, "a.jpg")) != 1 || h.sc.snapshot().Failed != 0 {
		t.Fatalf("wallpaper not retried: %+v", h.sc.snapshot())
	}
}

func TestWallpaperFollowsStorageEvents(t *testing.T) {
	f, st, th, sc := s3Harness(t, 2, nil)
	hub := testHub(t, sc)
	srv := httptest.NewServer(newRouter(st, sc, th, sc.sources, "Asia/Shanghai", testGate()))
	t.Cleanup(srv.Close)
	h := &wallHarness{st: st, th: th, sc: sc, srv: srv}

	f.put("壁纸/new.jpg", gradientJPEG(480, 270, color.RGBA{60, 30, 120, 255}))
	deliver(t, hub, testEventToken, eventBody("s3:ObjectCreated:Put", "壁纸/new.jpg"))
	hub.wg.Wait()
	rows := h.walls(t, storageKeyPrefix(1)+"壁纸/new.jpg")
	if len(rows) != 1 || h.random(t, "?orientation=landscape") != rows[0].mediaPath() {
		t.Fatalf("uploaded photo has no wallpaper: %+v", rows)
	}
	f.del("壁纸/new.jpg")
	deliver(t, hub, testEventToken, eventBody("s3:ObjectRemoved:Delete", "壁纸/new.jpg"))
	hub.wg.Wait()
	if res, _ := h.get(t, http.MethodGet, "/v1/backgrounds/random", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("random after removal answered %d", res.StatusCode)
	}
	if res, _ := h.get(t, http.MethodGet, rows[0].mediaPath(), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed media answered %d", res.StatusCode)
	}
	if left, _ := filepath.Glob(filepath.Join(th.wallDir, "*.webp")); len(left) != 0 {
		t.Fatalf("files left %v", left)
	}
}

func TestLoginBackgroundUsesWallpapers(t *testing.T) {
	h := newWallHarness(t, map[string][]byte{
		"wide.jpg": gradientJPEG(640, 360, color.RGBA{180, 40, 30, 255}),
		"tall.jpg": gradientJPEG(360, 640, color.RGBA{30, 60, 170, 255}),
	})
	for orient, rel := range map[string]string{"land": "wide.jpg", "port": "tall.jpg"} {
		res, _ := h.get(t, http.MethodGet, "/api/login-bg?orient="+orient, nil)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != h.walls(t, rel)[0].mediaPath() || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("login-bg %s: %d %v", orient, res.StatusCode, res.Header)
		}
	}
	// The browser ends at a public WebP, never at the original.
	res, err := http.Get(h.srv.URL + "/api/login-bg?orient=port")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("login-bg target %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	only := newWallHarness(t, map[string][]byte{"wide.jpg": gradientJPEG(640, 360, color.RGBA{180, 40, 30, 255})})
	if res, _ := only.get(t, http.MethodGet, "/api/login-bg?orient=port", nil); res.Header.Get("Location") != only.walls(t, "wide.jpg")[0].mediaPath() {
		t.Fatalf("portrait screen without portrait wallpapers: %d %v", res.StatusCode, res.Header)
	}
	empty := newWallHarness(t, map[string][]byte{})
	if res, _ := empty.get(t, http.MethodGet, "/api/login-bg?orient=land", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("login-bg without photos %d", res.StatusCode)
	}
}

func TestOnlyRandomAndMediaArePublic(t *testing.T) {
	h := newWallHarness(t, map[string][]byte{"wide.jpg": gradientJPEG(640, 360, color.RGBA{180, 40, 30, 255})})
	wide := h.walls(t, "wide.jpg")[0]
	// nas-background's catalog, probes and metrics are not carried over.
	for _, path := range []string{"/v1/catalog", "/status", "/metrics", "/healthz", "/readyz", "/v1/backgrounds",
		"/media/sha256/zz/" + wide.SHA256 + ".webp", "/media/sha256/" + wide.SHA256[:2] + "/" + strings.ToUpper(wide.SHA256) + ".webp",
		"/media/sha256/ab/abc.webp", "/media/sha256/" + wide.SHA256[:2] + "/" + wide.SHA256 + ".jpg",
		"/media/sha256/00/" + strings.Repeat("0", 64) + ".webp"} {
		if res, _ := h.get(t, http.MethodGet, path, nil); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, res.StatusCode)
		}
	}
	if res, _ := h.get(t, http.MethodPost, "/v1/backgrounds/random", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST random answered %d", res.StatusCode)
	}
	for _, path := range []string{"/v1/backgrounds/random", wide.mediaPath()} {
		res, _ := h.get(t, http.MethodOptions, path, nil)
		if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "GET") {
			t.Fatalf("preflight %s: %d %v", path, res.StatusCode, res.Header)
		}
	}
	// The album itself stays behind the login.
	id := h.photo(t, "wide.jpg").ID
	for _, path := range []string{"/api/photos", "/api/albums", "/api/settings", fmt.Sprintf("/thumb/%d", id), fmt.Sprintf("/original/%d", id)} {
		if res, _ := h.get(t, http.MethodGet, path, nil); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s answered %d without login", path, res.StatusCode)
		}
	}
}

func TestSettingsShowWallpaperStats(t *testing.T) {
	files := lifecycleFiles()
	h := newWallHarness(t, files)
	cookie := loginCookie(t, h.srv, "juen", "secret")
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/settings", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Wallpapers wallStats `json:"wallpapers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, rel := range []string{"横/wide.jpg", "竖/tall.jpg", "方/square.jpg"} {
		for _, w := range h.walls(t, rel) {
			total += w.Bytes
		}
	}
	want := wallStats{Photos: 3, Ready: 3, Landscape: 1, Portrait: 1, Square: 1, Bytes: total}
	if body.Wallpapers != want {
		t.Fatalf("stats %+v, want %+v", body.Wallpapers, want)
	}
}

func TestOldDatabaseGainsWallpaperTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE photos (id INTEGER PRIMARY KEY, rel_path TEXT NOT NULL UNIQUE, size INTEGER NOT NULL,
	  mtime_unix INTEGER NOT NULL, width INTEGER NOT NULL DEFAULT 0, height INTEGER NOT NULL DEFAULT 0, broken INTEGER NOT NULL DEFAULT 0);
	  INSERT INTO photos (rel_path, size, mtime_unix, width, height) VALUES ('a.jpg', 10, 1700000000, 300, 200);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if rows, err := st.wallpaperCandidates(wallFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("wallpapers after migration %v %v", rows, err)
	}
	if all, err := st.listOK(); err != nil || len(all) != 1 {
		t.Fatalf("photos after migration %v %v", all, err)
	}
}

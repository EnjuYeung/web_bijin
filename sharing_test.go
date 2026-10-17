package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/webp"
)

type shareHarness struct {
	st         *store
	sources    *sourceSet
	sc         *scanner
	thumbs     *thumbCache
	srv        *httptest.Server
	cookie     *http.Cookie
	root, data string
}

type shareResponse struct {
	Active     bool   `json:"active"`
	Generation int64  `json:"generation"`
	Version    string `json:"version"`
	Path       string `json:"path"`
}

func tinyJPEG(w, h int) []byte { return gradientJPEG(w, h, color.RGBA{10, 20, 30, 255}) }

func newShareHarness(t *testing.T, files map[string][]byte) *shareHarness {
	t.Helper()
	h := &shareHarness{root: t.TempDir(), data: t.TempDir()}
	for name, data := range files {
		path := filepath.Join(h.root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.start(t)
	h.sc.run(context.Background())
	t.Cleanup(func() { h.srv.Close(); h.st.Close() })
	return h
}

func (h *shareHarness) start(t *testing.T) {
	t.Helper()
	var err error
	h.st, err = openStore(filepath.Join(h.data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	h.sources = newSourceSet(&localPhotoSource{root: h.root})
	h.thumbs = newThumbCache(filepath.Join(h.data, "thumbs"), filepath.Join(h.data, "wallpapers"), h.sources, 2)
	h.sc = newScanner(config{PhotosDir: h.root, DataDir: h.data, MaxPixels: 64_000_000}, h.st, h.thumbs, h.sources)
	h.srv = httptest.NewServer(newRouter(h.st, h.sc, h.thumbs, h.sources, "Asia/Shanghai", testGate()))
	h.cookie = loginCookie(t, h.srv, "juen", "secret")
}

func shareRequest(t *testing.T, srv *httptest.Server, cookie *http.Cookie, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, data
}

func shareState(t *testing.T, srv *httptest.Server, cookie *http.Cookie, id int64) shareResponse {
	t.Helper()
	res, raw := shareRequest(t, srv, cookie, "GET", fmt.Sprintf("/api/photos/%d/share", id), nil, nil)
	if res.StatusCode != 200 {
		t.Fatalf("state %d: %s", res.StatusCode, raw)
	}
	var out shareResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func shareChange(t *testing.T, srv *httptest.Server, cookie *http.Cookie, id int64, current shareResponse, stop bool) shareResponse {
	t.Helper()
	method := "POST"
	if stop {
		method = "DELETE"
	}
	res, raw := shareRequest(t, srv, cookie, method, fmt.Sprintf("/api/photos/%d/share", id), map[string]any{"version": current.Version, "generation": current.Generation}, nil)
	if res.StatusCode != 200 {
		t.Fatalf("%s share %d: %s", method, res.StatusCode, raw)
	}
	var out shareResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *shareHarness) photo(t *testing.T, key string) photo {
	t.Helper()
	p, ok, err := h.st.getBySourceKey(key)
	if err != nil || !ok {
		t.Fatalf("indexed %s: ok=%v err=%v", key, ok, err)
	}
	return p
}

func (h *shareHarness) share(t *testing.T, key string) shareResponse {
	t.Helper()
	p := h.photo(t, key)
	return shareChange(t, h.srv, h.cookie, p.ID, shareState(t, h.srv, h.cookie, p.ID), false)
}

func shareFormats(t *testing.T) map[string][]byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(x * 7), uint8(y * 9), 120, 255})
		}
	}
	var p, w, g bytes.Buffer
	if err := png.Encode(&p, im); err != nil {
		t.Fatal(err)
	}
	if err := webp.Encode(&w, im, webp.Options{Quality: 80, Method: 4}); err != nil {
		t.Fatal(err)
	}
	frameA := image.NewPaletted(image.Rect(0, 0, 32, 24), color.Palette{color.Black, color.White})
	frameB := image.NewPaletted(frameA.Bounds(), frameA.Palette)
	for i := range frameB.Pix {
		frameB.Pix[i] = 1
	}
	if err := gif.EncodeAll(&g, &gif.GIF{Image: []*image.Paletted{frameA, frameB}, Delay: []int{2, 3}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"a.jpg": tinyJPEG(32, 24), "b.png": p.Bytes(), "c.webp": w.Bytes(), "d.gif": g.Bytes()}
}

func TestSharingOriginalFormatsAndHTTP(t *testing.T) {
	files := shareFormats(t)
	h := newShareHarness(t, files)
	for name, want := range files {
		t.Run(name, func(t *testing.T) {
			p := h.photo(t, name)
			state := shareState(t, h.srv, h.cookie, p.ID)
			if state.Active || state.Path != "" {
				t.Fatal("original is shared by default")
			}
			for _, path := range []string{fmt.Sprintf("/original/%d", p.ID), fmt.Sprintf("/api/photos/%d/share", p.ID), "/api/photos?shared=1"} {
				res, _ := shareRequest(t, h.srv, nil, "GET", path, nil, nil)
				if res.StatusCode != 401 {
					t.Fatalf("private %s: %d", path, res.StatusCode)
				}
			}
			shared := shareChange(t, h.srv, h.cookie, p.ID, state, false)
			if !shared.Active || shared.Path == "" {
				t.Fatal("copy did not enable sharing")
			}
			res, got := shareRequest(t, h.srv, nil, "GET", shared.Path, nil, nil)
			if res.StatusCode != 200 || !bytes.Equal(got, want) || res.Header.Get("Location") != "" {
				t.Fatalf("original byte response: %d", res.StatusCode)
			}
			if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
				t.Fatal("public response or cache headers")
			}
			format := strings.TrimPrefix(filepath.Ext(name), ".")
			if format == "jpg" {
				format = "jpeg"
			}
			if res.Header.Get("Content-Type") != "image/"+format {
				t.Fatal("wrong original MIME")
			}
			etag := res.Header.Get("ETag")
			if etag == "" {
				t.Fatal("missing ETag")
			}
			if name == "d.gif" {
				animation, err := gif.DecodeAll(bytes.NewReader(got))
				if err != nil || len(animation.Image) != 2 {
					t.Fatal("GIF animation changed")
				}
			}
			res, got = shareRequest(t, h.srv, nil, "HEAD", shared.Path, nil, nil)
			if res.StatusCode != 200 || len(got) != 0 || res.Header.Get("Content-Length") != fmt.Sprint(len(want)) {
				t.Fatal("HEAD")
			}
			res, got = shareRequest(t, h.srv, nil, "GET", shared.Path, nil, map[string]string{"Range": "bytes=0-9"})
			if res.StatusCode != 206 || !bytes.Equal(got, want[:10]) {
				t.Fatal("Range")
			}
			res, _ = shareRequest(t, h.srv, nil, "GET", shared.Path, nil, map[string]string{"If-None-Match": etag})
			if res.StatusCode != 304 {
				t.Fatal("conditional response")
			}
			res, _ = shareRequest(t, h.srv, nil, "OPTIONS", shared.Path, nil, nil)
			if res.StatusCode != 204 {
				t.Fatal("preflight")
			}
			again := shareChange(t, h.srv, h.cookie, p.ID, shared, false)
			if again.Path != shared.Path {
				t.Fatal("repeat copy changed the link")
			}
			stopped := shareChange(t, h.srv, h.cookie, p.ID, again, true)
			if stopped.Active || stopped.Path != "" {
				t.Fatal("stop still active")
			}
			for _, headers := range []map[string]string{nil, {"Range": "bytes=0-9"}, {"If-None-Match": etag}} {
				res, _ = shareRequest(t, h.srv, nil, "GET", shared.Path, nil, headers)
				if res.StatusCode != 404 {
					t.Fatal("stop bypassed")
				}
			}
			newLink := shareChange(t, h.srv, h.cookie, p.ID, stopped, false)
			if newLink.Path == shared.Path {
				t.Fatal("restart restored old identity")
			}
			res, _ = shareRequest(t, h.srv, nil, "GET", shared.Path, nil, nil)
			if res.StatusCode != 404 {
				t.Fatal("old link resurrected")
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(h.data, ".share-tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatal("original temporary files retained")
	}
}

func TestSharingPersistsAndRefusesStaleWrites(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"a.jpg": tinyJPEG(32, 24)})
	p := h.photo(t, "a.jpg")
	before := shareState(t, h.srv, h.cookie, p.ID)
	first := h.share(t, "a.jpg")
	h.srv.Close()
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	if after := shareState(t, h.srv, h.cookie, p.ID); after.Path != first.Path {
		t.Fatal("restart lost sharing")
	}
	res, _ := shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("restart link unavailable")
	}
	stopped := shareChange(t, h.srv, h.cookie, p.ID, first, true)
	res, _ = shareRequest(t, h.srv, h.cookie, "POST", fmt.Sprintf("/api/photos/%d/share", p.ID), map[string]any{"version": before.Version, "generation": before.Generation}, nil)
	if res.StatusCode != 409 {
		t.Fatal("delayed copy reopened stopped sharing")
	}
	second := shareChange(t, h.srv, h.cookie, p.ID, stopped, false)
	res, _ = shareRequest(t, h.srv, h.cookie, "DELETE", fmt.Sprintf("/api/photos/%d/share", p.ID), map[string]any{"version": first.Version, "generation": first.Generation}, nil)
	if res.StatusCode != 409 {
		t.Fatal("old stop canceled the newer sharing")
	}
	res, _ = shareRequest(t, h.srv, nil, "GET", second.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("newer share changed")
	}
}

func TestSharingConcurrentCopiesConverge(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"a.jpg": tinyJPEG(32, 24)})
	p := h.photo(t, "a.jpg")
	state := shareState(t, h.srv, h.cookie, p.ID)
	body, _ := json.Marshal(map[string]any{"version": state.Version, "generation": state.Generation})
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 12)
	for i := 0; i < 12; i++ {
		go func() {
			req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/photos/%d/share", h.srv.URL, p.ID), bytes.NewReader(body))
			req.AddCookie(h.cookie)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer res.Body.Close()
			var state shareResponse
			err = json.NewDecoder(res.Body).Decode(&state)
			if res.StatusCode != 200 {
				err = fmt.Errorf("copy: %d", res.StatusCode)
			}
			results <- result{path: state.Path, err: err}
		}()
	}
	path := ""
	for i := 0; i < 12; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if path != "" && path != got.path {
			t.Fatal("concurrent copies created multiple links")
		}
		path = got.path
	}
}

func TestSharingSwapBeforeScanAndIDReuse(t *testing.T) {
	a, b := tinyJPEG(32, 24), tinyJPEG(24, 32)
	h := newShareHarness(t, map[string][]byte{"a.jpg": a})
	p := h.photo(t, "a.jpg")
	first := h.share(t, "a.jpg")
	if err := os.WriteFile(filepath.Join(h.root, "a.jpg"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("old link sent changed bytes before scan")
	}
	h.sc.run(context.Background())
	second := h.share(t, "a.jpg")
	res, got := shareRequest(t, h.srv, nil, "GET", second.Path, nil, nil)
	if res.StatusCode != 200 || !bytes.Equal(got, b) {
		t.Fatal("explicit replacement sharing")
	}
	if err := h.st.deleteByID(p.ID); err != nil {
		t.Fatal(err)
	}
	h.sc.run(context.Background())
	reused := h.photo(t, "a.jpg")
	if reused.ID != p.ID {
		t.Fatal("fixture did not exercise integer ID reuse")
	}
	for _, path := range []string{first.Path, second.Path} {
		res, _ = shareRequest(t, h.srv, nil, "GET", path, nil, nil)
		if res.StatusCode != 404 {
			t.Fatal("reused ID restored old link")
		}
	}
	third := h.share(t, "a.jpg")
	if third.Path == first.Path || third.Path == second.Path {
		t.Fatal("old identity reused")
	}
	if err := os.Rename(filepath.Join(h.root, "a.jpg"), filepath.Join(h.root, "moved.jpg")); err != nil {
		t.Fatal(err)
	}
	res, _ = shareRequest(t, h.srv, nil, "GET", third.Path, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("moved image remained shared")
	}
	if err := os.Rename(filepath.Join(h.root, "moved.jpg"), filepath.Join(h.root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	res, _ = shareRequest(t, h.srv, nil, "GET", third.Path, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("restoring file resurrected link")
	}
}

type shareControlledSource struct {
	*localPhotoSource
	mu      sync.Mutex
	hook    func()
	chunk   int
	offline bool
}

func (s *shareControlledSource) Stat(ctx context.Context, key string) (sourceObject, error) {
	s.mu.Lock()
	offline := s.offline
	s.mu.Unlock()
	if offline {
		return sourceObject{}, errors.New("storage temporarily offline")
	}
	return s.localPhotoSource.Stat(ctx, key)
}
func (s *shareControlledSource) OpenVersion(ctx context.Context, o sourceObject) (readSeekCloser, error) {
	f, err := s.localPhotoSource.OpenVersion(ctx, o)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	hook, chunk := s.hook, s.chunk
	s.mu.Unlock()
	return &shareControlledReader{readSeekCloser: f, hook: hook, chunk: chunk}, nil
}

type shareControlledReader struct {
	readSeekCloser
	hook  func()
	chunk int
	once  sync.Once
}

func (r *shareControlledReader) Read(b []byte) (int, error) {
	if r.chunk > 0 && len(b) > r.chunk {
		b = b[:r.chunk]
	}
	n, err := r.readSeekCloser.Read(b)
	if n > 0 && r.hook != nil {
		r.once.Do(r.hook)
	}
	return n, err
}

func TestSharingSameMetadataSwapAndInterruptedRead(t *testing.T) {
	a, b := tinyJPEG(32, 24), tinyJPEG(24, 32)
	size := max(len(a), len(b))
	a = append(a, make([]byte, size-len(a))...)
	b = append(b, make([]byte, size-len(b))...)
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint("during=", during), func(t *testing.T) {
			h := newShareHarness(t, map[string][]byte{"a.jpg": a})
			controlled := &shareControlledSource{localPhotoSource: &localPhotoSource{root: h.root}}
			h.sources.local = controlled
			first := h.share(t, "a.jpg")
			path := filepath.Join(h.root, "a.jpg")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			swap := func() {
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Error(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Error(err)
				}
			}
			if during {
				controlled.mu.Lock()
				controlled.hook, controlled.chunk = swap, 32
				controlled.mu.Unlock()
			} else {
				swap()
			}
			res, _ := shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
			if res.StatusCode != 404 {
				t.Fatal("same-metadata replacement leaked bytes")
			}
			controlled.mu.Lock()
			controlled.hook = nil
			controlled.mu.Unlock()
			if err := os.WriteFile(path, a, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			res, _ = shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
			if res.StatusCode != 404 {
				t.Fatal("old file restoration revived permission")
			}
		})
	}
}

func TestSharingStopDuringReadAndTemporaryFailure(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"a.jpg": tinyJPEG(32, 24)})
	controlled := &shareControlledSource{localPhotoSource: &localPhotoSource{root: h.root}}
	h.sources.local = controlled
	p := h.photo(t, "a.jpg")
	first := h.share(t, "a.jpg")
	controlled.mu.Lock()
	controlled.offline = true
	controlled.mu.Unlock()
	res, _ := shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
	if res.StatusCode != 503 {
		t.Fatal("temporary fault not reported")
	}
	if current := shareState(t, h.srv, h.cookie, p.ID); !current.Active {
		t.Fatal("temporary fault revoked permission")
	}
	controlled.mu.Lock()
	controlled.offline = false
	controlled.mu.Unlock()
	res, _ = shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("temporary fault recovery")
	}
	controlled.mu.Lock()
	controlled.hook = func() { shareChange(t, h.srv, h.cookie, p.ID, first, true) }
	controlled.mu.Unlock()
	res, _ = shareRequest(t, h.srv, nil, "GET", first.Path, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("stopped during read still sent bytes")
	}
}

func TestSharingS3ConditionalTransferAndPrivateDirectView(t *testing.T) {
	f, st, th, sc := s3Harness(t, 2, func(c *s3Config) { c.DirectOriginal = true })
	a, b := tinyJPEG(32, 24), tinyJPEG(24, 32)
	f.put("a.jpg", a)
	sc.run(context.Background())
	srv := httptest.NewServer(newRouter(st, sc, th, sc.sources, "Asia/Shanghai", testGate()))
	defer srv.Close()
	cookie := loginCookie(t, srv, "juen", "secret")
	p, ok, err := st.getBySourceKey(storageKeyPrefix(1) + "a.jpg")
	if err != nil || !ok {
		t.Fatal(err)
	}
	res, _ := shareRequest(t, srv, cookie, "GET", fmt.Sprintf("/original/%d", p.ID), nil, nil)
	if res.StatusCode != 302 {
		t.Fatal("private direct view changed")
	}
	shared := shareChange(t, srv, cookie, p.ID, shareState(t, srv, cookie, p.ID), false)
	var matched atomic.Bool
	f.mu.Lock()
	f.beforeServe = func(r *http.Request) {
		if r.Method == "GET" && r.Header.Get("If-Match") != "" {
			matched.Store(true)
		}
	}
	f.mu.Unlock()
	res, got := shareRequest(t, srv, nil, "GET", shared.Path, nil, nil)
	if res.StatusCode != 200 || sha256.Sum256(got) != sha256.Sum256(a) || res.Header.Get("Location") != "" || !matched.Load() {
		t.Fatal("S3 share bypassed conditional application transfer")
	}
	f.put("a.jpg", b)
	res, _ = shareRequest(t, srv, nil, "GET", shared.Path, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("S3 replacement before notification leaked")
	}
	sc.run(context.Background())
	f.mu.Lock()
	f.beforeServe = nil
	f.mu.Unlock()
	fresh := shareChange(t, srv, cookie, p.ID, shareState(t, srv, cookie, p.ID), false)
	res, got = shareRequest(t, srv, nil, "GET", fresh.Path, nil, nil)
	if res.StatusCode != 200 || !bytes.Equal(got, b) {
		t.Fatal("new S3 original sharing")
	}
}

func TestSharingFilteredPaginationAndWriteGate(t *testing.T) {
	files := map[string][]byte{}
	for i := 0; i < 37; i++ {
		files[fmt.Sprintf("album%d/%02d.jpg", i%2, i)] = tinyJPEG(16, 12)
	}
	h := newShareHarness(t, files)
	active := map[int64]shareResponse{}
	for i := 0; i < 37; i += 2 {
		key := fmt.Sprintf("album%d/%02d.jpg", i%2, i)
		active[h.photo(t, key).ID] = h.share(t, key)
	}
	var seen []int64
	after := ""
	for page := 0; page < 10; page++ {
		res, raw := shareRequest(t, h.srv, h.cookie, "GET", "/api/photos?shared=1&limit=7&seed=42&after="+after, nil, nil)
		if res.StatusCode != 200 {
			t.Fatal("list status")
		}
		var data struct {
			Photos []photoItem `json:"photos"`
			Total  int         `json:"total"`
			Next   *string     `json:"next"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Total != len(active) {
			t.Fatalf("filtered count: %d", data.Total)
		}
		for _, p := range data.Photos {
			if _, ok := active[p.ID]; !ok {
				t.Fatal("unshared in result")
			}
			seen = append(seen, p.ID)
			if p.Version == "" {
				t.Fatal("missing selected version")
			}
		}
		if data.Next == nil {
			break
		}
		after = *data.Next
	}
	if len(seen) != len(active) {
		t.Fatal("filtered pagination omitted or duplicated photos")
	}
	res, raw := shareRequest(t, h.srv, h.cookie, "GET", "/api/photos?shared=1&album=album1&seed=42", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(raw, []byte(`"total":0`)) {
		t.Fatal("album filter scope")
	}
	id := seen[0]
	endpoint := fmt.Sprintf("/api/photos/%d/share", id)
	state := active[id]
	body := map[string]any{"version": state.Version, "generation": state.Generation}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		res, _ = shareRequest(t, h.srv, nil, method, endpoint, body, nil)
		if res.StatusCode != 401 {
			t.Fatal("anonymous share management")
		}
	}
	for _, method := range []string{"POST", "DELETE"} {
		res, _ = shareRequest(t, h.srv, h.cookie, method, endpoint, body, map[string]string{"Origin": "https://other.example"})
		if res.StatusCode != 403 {
			t.Fatal("cross-site write")
		}
	}
	res, _ = shareRequest(t, h.srv, h.cookie, "POST", endpoint, map[string]any{"generation": state.Generation, "version": "stale"}, nil)
	if res.StatusCode != 400 {
		t.Fatal("invalid request")
	}
	shareChange(t, h.srv, h.cookie, id, state, true)
	res, raw = shareRequest(t, h.srv, h.cookie, "GET", "/api/photos?shared=1&seed=42&limit=200", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(raw, []byte(fmt.Sprintf(`"total":%d`, len(active)-1))) {
		t.Fatal("stop did not invalidate shared order/count")
	}
}

// Keep the cancellation slot reusable even if cancellation races acquisition.
func TestSharingCanceledReadDoesNotKeepSlot(t *testing.T) {
	a := newShareAPI(nil, nil, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20; i++ {
		if release, err := a.public.acquire(ctx); err == nil {
			release()
			t.Fatal("canceled acquisition succeeded")
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := a.public.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// Visitors holding every public slot do not keep the owner's copy waiting.
func TestSharingVisitorsDoNotTakeOwnerSlots(t *testing.T) {
	a := newShareAPI(nil, nil, t.TempDir())
	for i := 0; i < cap(a.public); i++ {
		release, err := a.public.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.public.acquire(ctx); err == nil {
		t.Fatal("a third visitor got a slot")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	release, err := a.owner.acquire(ctx)
	if err != nil {
		t.Fatal("owner waited behind visitors:", err)
	}
	release()
}

func TestSharingBadAddressesNeverExposeOriginalOrCancelValidLink(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"a.jpg": tinyJPEG(32, 24)})
	shared := h.share(t, "a.jpg")
	for _, path := range []string{"/i/1.jpg", "/i/" + strings.Repeat("A", 4000) + ".jpg", shared.Path + ".jpg", strings.TrimSuffix(shared.Path, "jpg") + "png"} {
		res, _ := shareRequest(t, h.srv, nil, "GET", path, nil, nil)
		if res.StatusCode != 404 || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("invalid address was accepted or cacheable")
		}
	}
	res, _ := shareRequest(t, h.srv, nil, "GET", shared.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("malformed request canceled a valid share")
	}
	res, _ = shareRequest(t, h.srv, h.cookie, "GET", "/api/photos/99999/share", nil, nil)
	if res.StatusCode != 404 || res.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing private state can be cached")
	}
}

func TestSharingDelayedFirstCopyCannotTargetReimportedPhoto(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"a.jpg": tinyJPEG(32, 24)})
	p := h.photo(t, "a.jpg")
	before := shareState(t, h.srv, h.cookie, p.ID)
	if err := h.st.deleteByID(p.ID); err != nil {
		t.Fatal(err)
	}
	// Same file, metadata, source key and integer id: only the retained
	// generation can tell this delayed request from a newly chosen photo.
	h.sc.run(context.Background())
	reimported := h.photo(t, "a.jpg")
	if reimported.ID != p.ID {
		t.Fatal("fixture did not reuse photo id")
	}
	res, _ := shareRequest(t, h.srv, h.cookie, "POST", fmt.Sprintf("/api/photos/%d/share", p.ID), map[string]any{"version": before.Version, "generation": before.Generation}, nil)
	if res.StatusCode != 409 {
		t.Fatal("delayed copy shared a reimported photo")
	}
	current := shareState(t, h.srv, h.cookie, p.ID)
	if current.Active || current.Generation <= before.Generation {
		t.Fatal("deletion lost the modification generation")
	}
	fresh := h.share(t, "a.jpg")
	res, _ = shareRequest(t, h.srv, nil, "GET", fresh.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("new explicit sharing failed")
	}
}

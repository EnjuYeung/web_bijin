package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixReader struct{ *bytes.Reader }

func (fixReader) Close() error { return nil }

type fixSource struct {
	data   []byte
	object sourceObject
	opens  atomic.Int32
	fail   bool
}

func (s *fixSource) Walk(ctx context.Context, visit func(sourceObject) error) error {
	return visit(s.object)
}
func (s *fixSource) Stat(ctx context.Context, rel string) (sourceObject, error) {
	return s.object, ctx.Err()
}

func (s *fixSource) Open(context.Context, string) (readSeekCloser, error) {
	s.opens.Add(1)
	if s.fail {
		return nil, errors.New("injected read failure")
	}
	return fixReader{bytes.NewReader(s.data)}, nil
}
func fixtureJPEG(c color.RGBA) []byte {
	im := image.NewRGBA(image.Rect(0, 0, 128, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			im.SetRGBA(x, y, color.RGBA{c.R + uint8(x), c.G + uint8(y), c.B, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 85}); err != nil {
		panic(err)
	}
	return b.Bytes()
}
func fixSetup(t *testing.T) (*store, *fixSource, *thumbCache, *scanner) {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := &fixSource{data: fixtureJPEG(color.RGBA{20, 20, 30, 255})}
	src.object = sourceObject{Key: "a.jpg", RelPath: "a.jpg", Size: int64(len(src.data)), Mtime: time.Unix(1700000000, 0), Version: "v1", Backend: "s3"}
	th := newThumbCache(t.TempDir(), t.TempDir(), src, 1)
	return st, src, th, newScanner(config{MaxPixels: 64_000_000}, st, th, newSourceSet(src))
}
func TestFixReadFailurePreservesPhotoAndRetries(t *testing.T) {
	st, src, th, sc := fixSetup(t)
	sc.run(context.Background())
	old, _, _ := st.getBySourceKey("a.jpg")
	before, _ := os.ReadFile(th.path(old))
	src.object.Version = "v2"
	src.fail = true
	sc.run(context.Background())
	got, _, _ := st.getByID(old.ID)
	if got.SourceVersion != "v1" || got.Broken || sc.snapshot().Ready != 1 || sc.snapshot().LastErr == "" || sc.snapshot().Failed != 1 {
		t.Fatalf("lost visible state: %+v %+v", got, sc.snapshot())
	}
	after, _ := os.ReadFile(th.path(old))
	if !bytes.Equal(before, after) {
		t.Fatal("old thumbnail changed")
	}
	src.fail = false
	src.data = fixtureJPEG(color.RGBA{100, 50, 200, 255})
	sc.run(context.Background())
	got, _, _ = st.getByID(old.ID)
	after, err := os.ReadFile(th.path(got))
	if err != nil || got.SourceVersion != "v2" || bytes.Equal(before, after) || sc.snapshot().LastErr != "" {
		t.Fatalf("retry failed: %v %+v", err, got)
	}
	if _, err := os.Stat(th.path(old)); !os.IsNotExist(err) {
		t.Fatal("obsolete cache retained")
	}
}
func TestFixThumbnailWriteFailureRetries(t *testing.T) {
	st, src, th, sc := fixSetup(t)
	sc.run(context.Background())
	old, _, _ := st.getBySourceKey("a.jpg")
	src.object.Version = "v2"
	src.data = fixtureJPEG(color.RGBA{100, 50, 200, 255})
	next := old
	next.SourceVersion = "v2"
	if err := os.Mkdir(th.path(next), 0700); err != nil {
		t.Fatal(err)
	}
	sc.run(context.Background())
	got, _, _ := st.getByID(old.ID)
	if got.SourceVersion != "v1" || !th.exists(old) || sc.snapshot().LastErr == "" {
		t.Fatal("failed write committed")
	}
	if err := os.Remove(th.path(next)); err != nil {
		t.Fatal(err)
	}
	sc.run(context.Background())
	got, _, _ = st.getByID(old.ID)
	if got.SourceVersion != "v2" || !th.exists(got) {
		t.Fatal("write did not recover")
	}
}
func TestFixCorruptImageSkippedUntilVersionChanges(t *testing.T) {
	for _, kind := range []string{"invalid", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			st, src, _, sc := fixSetup(t)
			if kind == "invalid" {
				src.data = []byte("not an image")
			} else {
				src.data = src.data[:len(src.data)-100]
				if _, _, err := image.DecodeConfig(bytes.NewReader(src.data)); err != nil {
					t.Fatal("fixture needs valid header")
				}
			}
			sc.run(context.Background())
			p, ok, _ := st.getBySourceKey("a.jpg")
			if !ok || !p.Broken || sc.snapshot().Ready != 0 || sc.snapshot().LastErr == "" {
				t.Fatal("corruption not reported")
			}
			before := src.opens.Load()
			sc.run(context.Background())
			if src.opens.Load() != before {
				t.Fatal("unchanged corrupt file reread")
			}
			src.data = fixtureJPEG(color.RGBA{1, 2, 3, 255})
			src.object.Version = "v2"
			sc.run(context.Background())
			if sc.snapshot().Ready != 1 {
				t.Fatal("fixed image not restored")
			}
		})
	}
}
func TestFixConcurrentMissingThumbnailAndCancelledWait(t *testing.T) {
	st, src, th, sc := fixSetup(t)
	p := photo{ID: 1, RelPath: "a.jpg", SourceVersion: "v1", Width: 128, Height: 96}
	if _, err := st.upsert(p); err != nil {
		t.Fatal(err)
	}
	if err := th.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := testThumbnailError(sc.photos, ctx, p.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait not cancelled: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := testThumbnailBytes(sc.photos, context.Background(), p.ID)
			if err != nil || len(b) == 0 {
				t.Errorf("thumbnail: %v", err)
			}
		}()
	}
	th.unlock()
	wg.Wait()
	if src.opens.Load() != 1 {
		t.Fatalf("source opens = %d", src.opens.Load())
	}
}
func TestFixPasswordChangeRevokesCookie(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	old := newAuthGate("user", "old", nil, key)
	cookie := old.sign(time.Now().Add(time.Hour).Unix())
	if !newAuthGate("user", "old", nil, key).validSession(cookie) {
		t.Fatal("restart invalidated unchanged credentials")
	}
	if newAuthGate("user", "new", nil, key).validSession(cookie) {
		t.Fatal("password change retained session")
	}
}
func TestFixCacheTracksSourceVersion(t *testing.T) {
	st, src, _, sc := fixSetup(t)
	sc.run(context.Background())
	p, _, _ := st.getBySourceKey("a.jpg")
	oldItem := makePhotoItem(p, "Asia/Shanghai")
	request := func(original bool, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/image", nil)
		r.SetPathValue("id", fmt.Sprint(p.ID))
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		if original {
			handleOriginal(w, r, st, src)
		} else {
			handleThumb(w, r, sc.photos)
		}
		return w
	}
	for _, original := range []bool{false, true} {
		old := request(original, "")
		if old.Code != 200 {
			t.Fatal(old.Code)
		}
		if got := request(original, old.Header().Get("ETag")); got.Code != 304 {
			t.Fatal("unchanged image not validated")
		}
		src.object.Version += "-updated"
		sc.run(context.Background())
		got := request(original, old.Header().Get("ETag"))
		// Thumbnail URLs carry the version and may be cached for good; a
		// streamed original keeps its URL and must be revalidated.
		cache := "private, max-age=31536000, immutable"
		if original {
			cache = "private, no-cache"
		}
		if got.Code != 200 || got.Header().Get("ETag") == old.Header().Get("ETag") || got.Header().Get("Cache-Control") != cache {
			t.Fatal("stale cached response")
		}
	}
	p, _, _ = st.getByID(p.ID)
	item := makePhotoItem(p, "Asia/Shanghai")
	if oldItem.Thumb == item.Thumb || oldItem.Src == item.Src {
		t.Fatal("URLs did not change")
	}
}
func TestFixS3BodyCancellationAndSingleRead(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(fmt.Sprint(stall), func(t *testing.T) {
			data := fixtureJPEG(color.RGBA{20, 20, 30, 255})
			var heads, gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				if r.Method == "HEAD" {
					heads.Add(1)
					return
				}
				gets.Add(1)
				if stall {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				_, _ = w.Write(data)
			}))
			defer srv.Close()
			src, err := newS3PhotoSource(s3Config{Endpoint: srv.URL, Bucket: "test", Region: "us-east-1", AccessKey: "test", SecretKey: "test"})
			if err != nil {
				t.Fatal(err)
			}
			th := newThumbCache(t.TempDir(), t.TempDir(), src, 1)
			ctx := context.Background()
			var cancel context.CancelFunc
			if stall {
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			start := time.Now()
			err = prepareTestThumbnail(th, ctx, photo{ID: 1, RelPath: "a.jpg"})
			if stall {
				if err == nil || time.Since(start) > 2*time.Second {
					t.Fatalf("deadline failed: %v", err)
				}
			} else if err != nil || heads.Load() != 1 || gets.Load() != 1 {
				t.Fatalf("err=%v HEAD=%d GET=%d", err, heads.Load(), gets.Load())
			}
		})
	}
}
func TestFixPersistentVersionAndIndexMigration(t *testing.T) {
	st, src, th, sc := fixSetup(t)
	sc.run(context.Background())
	p, _, _ := st.getBySourceKey("a.jpg")
	if _, err := st.db.Exec("CREATE INDEX photos_mtime ON photos(mtime_unix)"); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name='photos_mtime'").Scan(&n); err != nil || n != 0 {
		t.Fatal("obsolete index remains")
	}
	var dbPath string
	var seq int
	var name string
	if err := st.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	st.Close()
	reopened, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sc = newScanner(config{MaxPixels: 64_000_000}, reopened, th, newSourceSet(src))
	src.opens.Store(0)
	sc.run(context.Background())
	if src.opens.Load() != 0 || sc.snapshot().Ready != 1 || !th.exists(p) {
		t.Fatal("restart lost persisted cache")
	}
}

func TestFixOrientationAndPixelLimit(t *testing.T) {
	st, src, th, sc := fixSetup(t)
	// JPEG APP1 with a little-endian TIFF orientation=6 (90 degrees clockwise).
	app1 := []byte{0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	src.data = append(append(append([]byte{}, src.data[:2]...), app1...), src.data[2:]...)
	sc.run(context.Background())
	p, _, _ := st.getBySourceKey("a.jpg")
	b, err := os.ReadFile(th.path(p))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || p.Width != 96 || p.Height != 128 || cfg.Width != 96 || cfg.Height != 128 {
		t.Fatalf("orientation: %+v %+v %v", p, cfg, err)
	}
	src.object.Version = "v2"
	sc = newScanner(config{MaxPixels: 100}, st, th, sc.sources)
	sc.run(context.Background())
	p, _, _ = st.getByID(p.ID)
	if !p.Broken || sc.snapshot().Ready != 0 || !errors.Is(sc.walk(context.Background()), nil) {
		t.Fatal("pixel limit not retained")
	}
}

func TestFixS3DefaultDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("45 second production deadline")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", "1000")
		if r.Method == "HEAD" {
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	src, err := newS3PhotoSource(s3Config{Endpoint: srv.URL, Bucket: "test", Region: "us-east-1", AccessKey: "test", SecretKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	th := newThumbCache(t.TempDir(), t.TempDir(), src, 1)
	start := time.Now()
	err = prepareTestThumbnail(th, context.Background(), photo{ID: 1, RelPath: "a.jpg"})
	if err == nil || time.Since(start) > imageReadTimeout+3*time.Second {
		t.Fatalf("unbounded body read: %v after %s", err, time.Since(start))
	}
	if err := th.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	th.unlock()
	t.Logf("body failed after %s; generation gate released", time.Since(start))
}

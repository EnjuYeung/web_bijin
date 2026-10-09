package main

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testEventToken = "event-token-0123456789"

// s3Harness indexes bucket "jan" of a fake S3 service as storage 1 next to
// an empty local directory.
func s3Harness(t *testing.T, workers int, edit func(*s3Config)) (*fakeS3, *store, *thumbCache, *scanner) {
	t.Helper()
	f := newFakeS3(t, "jan")
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := f.config()
	if edit != nil {
		edit(&cfg)
	}
	set := newSourceSet(newMemSource(map[string][]byte{}))
	set.useStorages([]s3Config{cfg})
	th := newThumbCache(filepath.Join(t.TempDir(), "thumbs"), filepath.Join(t.TempDir(), "wallpapers"), set, workers)
	return f, st, th, newScanner(config{MaxPixels: 64_000_000}, st, th, set)
}

func testHub(t *testing.T, sc *scanner) *eventHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := newEventHub(ctx, testEventToken, sc)
	h.retry = 10 * time.Millisecond
	return h
}

func eventBody(name, key string) string {
	return fmt.Sprintf(`{"EventName":%q,"Records":[{"eventName":%q,"s3":{"bucket":{"name":"jan"},"object":{"key":%q}}}]}`,
		name, name, url.QueryEscape(key))
}

func deliver(t *testing.T, h *eventHub, token, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/s3-events", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.handler().ServeHTTP(w, req)
	return w.Code
}

func objectGets(f *fakeS3, key string) int {
	n := 0
	for _, r := range f.log() {
		if strings.HasPrefix(r, "GET /jan/"+key+"?") {
			n++
		}
	}
	return n
}

func TestEventsCreateUpdateAndRemove(t *testing.T) {
	f, st, th, sc := s3Harness(t, 2, nil)
	h := testHub(t, sc)
	key := storageKeyPrefix(1) + "相册/a.jpg"

	f.put("相册/a.jpg", fixtureJPEG(color.RGBA{10, 20, 30, 255}))
	if code := deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "相册/a.jpg")); code != 200 {
		t.Fatalf("delivery answered %d", code)
	}
	h.wg.Wait()
	p, ok, err := st.getBySourceKey(key)
	if err != nil || !ok || p.Broken || p.RelPath != "相册/a.jpg" || !th.exists(p) {
		t.Fatalf("created photo not indexed: %+v ok=%v err=%v", p, ok, err)
	}

	f.put("相册/a.jpg", append(fixtureJPEG(color.RGBA{200, 20, 30, 255}), 0, 0, 0))
	deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "相册/a.jpg"))
	h.wg.Wait()
	updated, _, _ := st.getBySourceKey(key)
	if updated.ID != p.ID || updated.SourceVersion == p.SourceVersion || !th.exists(updated) || th.exists(p) {
		t.Fatalf("overwrite not applied: before %+v after %+v", p, updated)
	}

	f.del("相册/a.jpg")
	deliver(t, h, testEventToken, eventBody("s3:ObjectRemoved:Delete", "相册/a.jpg"))
	h.wg.Wait()
	if _, ok, _ := st.getBySourceKey(key); ok || th.exists(updated) {
		t.Fatal("removed photo still indexed")
	}
	if ev := sc.eventSnapshot(); !ev.Enabled || ev.Received != 3 || ev.LastAt.IsZero() || ev.LastErr != "" {
		t.Fatalf("event status %+v", ev)
	}
}

func TestEventFromRustFSPayload(t *testing.T) {
	// A real RustFS delivery: keys are form-encoded, "+" is a space, "%2B" a plus.
	raw, err := os.ReadFile("testdata/rustfs-event-put.json")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), ".bijin-spike%2F", "%E7%9B%B8%E5%86%8C%2F", 1)
	f, st, _, sc := s3Harness(t, 1, nil)
	h := testHub(t, sc)
	f.put("相册/공백 test (1)+plus.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	if code := deliver(t, h, testEventToken, body); code != 200 {
		t.Fatal(code)
	}
	h.wg.Wait()
	if _, ok, _ := st.getBySourceKey(storageKeyPrefix(1) + "相册/공백 test (1)+plus.jpg"); !ok {
		t.Fatalf("decoded key not indexed; S3 requests: %v", f.log())
	}
}

func TestEventsRejectAndIgnore(t *testing.T) {
	f, st, _, sc := s3Harness(t, 1, nil)
	h := testHub(t, sc)
	f.put("notes.txt", []byte("not a photo"))
	f.put(".hidden/a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	f.put("other.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	for _, token := range []string{"", "wrong-token-0123456789"} {
		if code := deliver(t, h, token, eventBody("s3:ObjectCreated:Put", "other.jpg")); code != http.StatusUnauthorized {
			t.Fatalf("token %q answered %d", token, code)
		}
	}
	for _, body := range []string{
		eventBody("s3:ObjectCreated:Put", "notes.txt"),
		eventBody("s3:ObjectCreated:Put", ".hidden/a.jpg"),
		eventBody("s3:ObjectAccessed:Get", "other.jpg"),
		strings.Replace(eventBody("s3:ObjectCreated:Put", "other.jpg"), `"name":"jan"`, `"name":"elsewhere"`, 1),
		"{not json",
	} {
		// Answered 200 so the sender does not resend them forever.
		if code := deliver(t, h, testEventToken, body); code != 200 {
			t.Fatalf("%s answered %d", body, code)
		}
	}
	h.wg.Wait()
	if n, _ := st.countOK(); n != 0 || len(f.log()) != 0 {
		t.Fatalf("ignored events touched the index or storage: %d photos, requests %v", n, f.log())
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		w := httptest.NewRecorder()
		h.handler().ServeHTTP(w, httptest.NewRequest(method, "/", nil))
		if w.Code != 200 {
			t.Fatalf("health check %s answered %d", method, w.Code)
		}
	}
}

func TestEventForVanishedObjectIsHarmless(t *testing.T) {
	_, st, _, sc := s3Harness(t, 1, nil)
	h := testHub(t, sc)
	deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "gone.jpg"))
	h.wg.Wait()
	if n, _ := st.countOK(); n != 0 || sc.eventSnapshot().LastErr != "" {
		t.Fatalf("vanished object: %d photos, %+v", n, sc.eventSnapshot())
	}
}

func TestEventRetriesTemporaryFailure(t *testing.T) {
	f, st, _, sc := s3Harness(t, 1, nil)
	h := testHub(t, sc)
	h.retry = 150 * time.Millisecond
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	f.setDown(true)
	deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "a.jpg"))
	time.Sleep(50 * time.Millisecond)
	f.setDown(false)
	h.wg.Wait()
	if _, ok, _ := st.getBySourceKey(storageKeyPrefix(1) + "a.jpg"); !ok {
		t.Fatal("temporary failure not retried")
	}
}

func TestEventsForOneKeyFoldTogether(t *testing.T) {
	f, st, _, sc := s3Harness(t, 2, nil)
	f.delay = 100 * time.Millisecond
	h := testHub(t, sc)
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	for i := 0; i < 10; i++ {
		deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "a.jpg"))
	}
	h.wg.Wait()
	if n, _ := st.countOK(); n != 1 || objectGets(f, "a.jpg") != 1 {
		t.Fatalf("photos=%d object reads=%d", n, objectGets(f, "a.jpg"))
	}
}

// A scan, a notification and a page asking for the missing thumbnail of the
// same photo read the original once.
func TestScanEventAndThumbRequestReadOnce(t *testing.T) {
	f, st, th, sc := s3Harness(t, 4, nil)
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	sc.run(context.Background())
	p, ok, _ := st.getBySourceKey(storageKeyPrefix(1) + "a.jpg")
	if !ok || os.Remove(th.path(p)) != nil {
		t.Fatal("setup")
	}
	before := objectGets(f, "a.jpg")
	f.delay = 150 * time.Millisecond
	h := testHub(t, sc)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); sc.run(context.Background()) }()
	go func() {
		defer wg.Done()
		deliver(t, h, testEventToken, eventBody("s3:ObjectCreated:Put", "a.jpg"))
		h.wg.Wait()
	}()
	go func() {
		defer wg.Done()
		if b, err := testThumbnailBytes(sc.photos, context.Background(), p.ID); err != nil || len(b) == 0 {
			t.Errorf("thumbnail: %v", err)
		}
	}()
	wg.Wait()
	if reads := objectGets(f, "a.jpg") - before; reads != 1 || !th.exists(p) {
		t.Fatalf("original read %d times", reads)
	}
}

type slowSource struct {
	files     []string
	data      []byte
	cur, peak atomic.Int32
}

func (s *slowSource) Walk(_ context.Context, visit func(sourceObject) error) error {
	for _, f := range s.files {
		if err := visit(sourceObject{Key: f, RelPath: f, Size: int64(len(s.data)), Mtime: time.Unix(1700000000, 0), Version: "v1", Backend: "s3"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *slowSource) Stat(ctx context.Context, rel string) (sourceObject, error) {
	return statTestSource(ctx, s, rel)
}

func (s *slowSource) Open(context.Context, string) (readSeekCloser, error) {
	n := s.cur.Add(1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	time.Sleep(60 * time.Millisecond)
	s.cur.Add(-1)
	return fixReader{bytes.NewReader(s.data)}, nil
}

func TestScanUsesConfiguredWorkers(t *testing.T) {
	for _, workers := range []int{1, 3} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			src := &slowSource{data: fixtureJPEG(color.RGBA{1, 2, 3, 255})}
			for i := 0; i < 9; i++ {
				src.files = append(src.files, fmt.Sprintf("%d.jpg", i))
			}
			th := newThumbCache(t.TempDir(), t.TempDir(), src, workers)
			sc := newScanner(config{MaxPixels: 64_000_000}, st, th, newSourceSet(src))
			sc.run(context.Background())
			if n, _ := st.countOK(); n != 9 || src.peak.Load() != int32(workers) || sc.snapshot().LastErr != "" {
				t.Fatalf("photos=%d peak=%d err=%q", n, src.peak.Load(), sc.snapshot().LastErr)
			}
		})
	}
}

func TestCleanupKeepsPhotosAddedDuringScan(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.upsert(photo{RelPath: "listed-gone.jpg", Width: 1, Height: 1, SourceVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.maxID()
	if _, err := st.upsert(photo{RelPath: "added-by-event.jpg", Width: 1, Height: 1, SourceVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	gone, err := st.missingPhotos(map[string]struct{}{}, nil, before)
	if err != nil || len(gone) != 1 || gone[0].SourceKey != "listed-gone.jpg" {
		t.Fatalf("gone=%+v err=%v", gone, err)
	}
	processor := newPhotoProcessor(st, newThumbCache(t.TempDir(), t.TempDir(), newMemSource(map[string][]byte{}), 1), newSourceSet(newMemSource(map[string][]byte{})), 64_000_000)
	for _, candidate := range gone {
		if removed, err := processor.RemoveIfUnchanged(context.Background(), candidate); err != nil || !removed {
			t.Fatalf("remove: %v %v", removed, err)
		}
	}
	if _, ok, _ := st.getBySourceKey("added-by-event.jpg"); !ok {
		t.Fatal("photo added during the scan was removed")
	}
}

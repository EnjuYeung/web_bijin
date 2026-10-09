package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A delayed listing must never attach fresh source bytes to an older version.
func TestListedPhotoDoesNotRestoreOlderSourceVersion(t *testing.T) {
	f, st, _, sc := s3Harness(t, 1, nil)
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	target := sc.sources.match("jan", "a.jpg")[0]
	old, err := target.src.Stat(context.Background(), target.rel)
	if err != nil {
		t.Fatal(err)
	}
	old.Key = target.key
	sc.run(context.Background())
	f.put("a.jpg", fixtureJPEG(color.RGBA{80, 20, 120, 255}))
	sc.run(context.Background())
	current, _, _ := st.getBySourceKey(target.key)
	result := sc.photos.ApplyListed(context.Background(), old)
	if !errors.Is(result.Err, errSourceChanged) || !result.Retryable {
		t.Fatalf("stale result: %+v", result)
	}
	got, _, _ := st.getBySourceKey(target.key)
	if got.SourceVersion != current.SourceVersion {
		t.Fatalf("delayed listing restored %q; current source is %q", got.SourceVersion, current.SourceVersion)
	}
}

func testThumbnailBytes(p *photoProcessor, ctx context.Context, id int64) ([]byte, error) {
	thumb, err := p.Thumbnail(ctx, id, "")
	return thumb.Bytes, err
}
func testThumbnailError(p *photoProcessor, ctx context.Context, id int64) error {
	_, err := p.Thumbnail(ctx, id, "")
	return err
}
func prepareTestThumbnail(th *thumbCache, ctx context.Context, p photo) error {
	if err := th.lock(ctx); err != nil {
		return err
	}
	defer th.unlock()
	d, err := th.prepareVersion(ctx, sourceObject{Key: p.sourceKey()}, 64_000_000)
	if err != nil {
		return err
	}
	return th.save(p, d.thumb)
}

func statTestSource(ctx context.Context, source photoSource, rel string) (sourceObject, error) {
	var object sourceObject
	found := false
	err := source.Walk(ctx, func(candidate sourceObject) error {
		if candidate.RelPath == rel {
			object, found = candidate, true
		}
		return nil
	})
	if err == nil && !found {
		err = os.ErrNotExist
	}
	if err == nil {
		err = ctx.Err()
	}
	return object, err
}

func TestPhotoProcessingReportsKnownInvalidVersion(t *testing.T) {
	_, src, _, sc := fixSetup(t)
	src.data = []byte("not an image")
	first := sc.photos.SyncKey(context.Background(), "a.jpg")
	if !first.Invalid || first.Err == nil || first.Retryable || first.AlbumUsable {
		t.Fatalf("first invalid attempt: %+v", first)
	}
	reads := src.opens.Load()
	known := sc.photos.SyncKey(context.Background(), "a.jpg")
	if !known.Invalid || known.Err != nil || known.Retryable || known.AlbumUsable || known.WallpapersReady || src.opens.Load() != reads {
		t.Fatalf("known bad version must not be done, reread or counted as a new failure: %+v", known)
	}
}

// Done signals that a caller has reached a cancellable wait at the public
// context boundary; test ordering never relies on scheduling sleeps.
type waitSignalContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *waitSignalContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
func awaitProcessingSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("processing boundary not reached")
	}
}
func awaitPhotoResult(t *testing.T, ch <-chan photoResult) photoResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("processing did not finish")
		return photoResult{}
	}
}

func TestPhotoProcessingWaitingVersionUpdateDeleteAndCancel(t *testing.T) {
	for _, action := range []string{"update", "delete", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f, st, th, sc := s3Harness(t, 1, nil)
			f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
			key := storageKeyPrefix(1) + "a.jpg"
			initial := sc.photos.SyncKey(context.Background(), key)
			if initial.Err != nil || !initial.WallpapersReady {
				t.Fatal(initial)
			}
			previous, _, _ := st.getBySourceKey(key)
			old, err := sc.sources.Stat(context.Background(), key)
			if err != nil || os.Remove(th.path(previous)) != nil {
				t.Fatal("setup", err)
			}
			reading, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var block atomic.Bool
			block.Store(true)
			f.mu.Lock()
			f.beforeServe = func(r *http.Request) {
				if r.Method == "GET" && block.Swap(false) {
					close(reading)
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}
			}
			f.mu.Unlock()
			active := make(chan photoResult, 1)
			go func() { active <- sc.photos.ApplyListed(context.Background(), old) }()
			awaitProcessingSignal(t, reading)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := &waitSignalContext{Context: ctx, waiting: make(chan struct{})}
			queued := make(chan photoResult, 1)
			go func() { queued <- sc.photos.SyncKey(waiting, key) }()
			awaitProcessingSignal(t, waiting.waiting)
			if action == "cancel" {
				cancel()
				result := awaitPhotoResult(t, queued)
				if !errors.Is(result.Err, context.Canceled) {
					t.Fatal(result)
				}
				// The active source is still blocked: cancellation must finish
				// before the holder releases the key or worker slot.
				once.Do(func() { close(release) })
				if result := awaitPhotoResult(t, active); result.Err != nil {
					t.Fatal(result)
				}
				return
			}
			if action == "update" {
				f.put("a.jpg", fixtureJPEG(color.RGBA{90, 70, 30, 255}))
			} else {
				f.del("a.jpg")
			}
			once.Do(func() { close(release) })
			result := awaitPhotoResult(t, active)
			if result.Err == nil || !result.Retryable {
				t.Fatalf("obsolete read published: %+v", result)
			}
			current := awaitPhotoResult(t, queued)
			if current.Err != nil {
				t.Fatal(current)
			}
			stale := sc.photos.ApplyListed(context.Background(), old)
			if stale.Err == nil {
				t.Fatalf("old listing accepted: %+v", stale)
			}
			thumb, err := sc.photos.Thumbnail(context.Background(), previous.ID, "")
			if action == "delete" {
				if !current.Missing || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("deleted photo resurrected: %+v %v", current, err)
				}
			} else {
				if err != nil || !current.AlbumUsable || !current.WallpapersReady || thumb.Photo.SourceVersion != current.Version || current.Version == old.Version {
					t.Fatalf("updated photo: %+v %+v %v", current, thumb.Photo, err)
				}
			}
		})
	}
}

func TestPhotoProcessingThumbnailDoesNotWaitForWallpapers(t *testing.T) {
	f, st, th, sc := s3Harness(t, 1, nil)
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	srv := httptest.NewServer(newRouter(st, sc, th, sc.sources, "Asia/Shanghai", testGate()))
	defer srv.Close()
	cookie := loginCookie(t, srv, "juen", "secret")
	listed := func() []photoItem {
		req, _ := http.NewRequest("GET", srv.URL+"/api/photos", nil)
		req.AddCookie(cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		defer res.Body.Close()
		var page struct {
			Photos []photoItem `json:"photos"`
		}
		_ = json.NewDecoder(res.Body).Decode(&page)
		return page.Photos
	}
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var block atomic.Bool
	block.Store(true)
	f.mu.Lock()
	f.beforeServe = func(r *http.Request) {
		// Once the public album exposes the photo, hold the next source
		// validation before the wallpapers are committed. The key and worker
		// remain held throughout this incomplete wallpaper processing stage.
		if r.Method == "HEAD" && len(listed()) == 1 && block.Swap(false) {
			close(blocked)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}
	f.mu.Unlock()
	done := make(chan photoResult, 1)
	go func() { done <- sc.photos.SyncKey(context.Background(), storageKeyPrefix(1)+"a.jpg") }()
	awaitProcessingSignal(t, blocked)
	photos := listed()
	if len(photos) != 1 {
		t.Fatal("photo was not visible before wallpaper completion")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+photos[0].Thumb, nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("browser waited for wallpaper work: %v", err)
	}
	defer res.Body.Close()
	cfg, _, err := image.DecodeConfig(res.Body)
	if res.StatusCode != 200 || err != nil || cfg.Width != 128 || cfg.Height != 96 {
		t.Fatalf("thumbnail unusable: %d %+v %v", res.StatusCode, cfg, err)
	}
	select {
	case result := <-done:
		t.Fatalf("wallpaper holder unexpectedly finished: %+v", result)
	default:
	}
	once.Do(func() { close(release) })
	if result := awaitPhotoResult(t, done); result.Err != nil || !result.WallpapersReady {
		t.Fatal(result)
	}
}

func TestPhotoProcessingWallpaperFailureAndGIFResults(t *testing.T) {
	_, src, th, sc := fixSetup(t)
	blocker := th.wallDir + "/blocked"
	if err := os.WriteFile(blocker, []byte("file instead of directory"), 0600); err != nil {
		t.Fatal(err)
	}
	th.wallDir = blocker
	result := sc.photos.SyncKey(context.Background(), "a.jpg")
	if result.Err == nil || !result.AlbumUsable || result.WallpapersReady || !result.Retryable || result.Invalid {
		t.Fatal(result)
	}
	thumb, err := sc.photos.Thumbnail(context.Background(), 1, "")
	if err != nil || len(thumb.Bytes) == 0 {
		t.Fatal(err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	result = sc.photos.SyncKey(context.Background(), "a.jpg")
	if result.Err != nil || !result.AlbumUsable || !result.WallpapersReady {
		t.Fatal(result)
	}
	src.data = tinyGIF()
	src.object.Key, src.object.RelPath, src.object.Version = "animation.gif", "animation.gif", "gif-v1"
	result = sc.photos.SyncKey(context.Background(), "animation.gif")
	if result.Err != nil || !result.AlbumUsable || !result.WallpapersReady {
		t.Fatal(result)
	}
}

func TestPhotoProcessingThumbnailRepairUsesCurrentVersion(t *testing.T) {
	f, _, th, sc := s3Harness(t, 1, nil)
	key := storageKeyPrefix(1) + "a.jpg"
	f.put("a.jpg", gradientJPEG(128, 96, color.RGBA{1, 2, 3, 255}))
	if result := sc.photos.SyncKey(context.Background(), key); result.Err != nil {
		t.Fatal(result)
	}
	old, err := sc.photos.Thumbnail(context.Background(), 1, "")
	if err != nil || os.Remove(th.path(old.Photo)) != nil {
		t.Fatal("setup", err)
	}
	f.put("a.jpg", gradientJPEG(96, 128, color.RGBA{90, 10, 60, 255}))
	source, err := sc.sources.Stat(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	before := objectGets(f, "a.jpg")
	current, err := sc.photos.Thumbnail(context.Background(), old.Photo.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(current.Bytes))
	if err != nil || cfg.Width != 96 || cfg.Height != 128 || current.Photo.SourceVersion != source.Version || current.Photo.SourceVersion == old.Photo.SourceVersion {
		t.Fatalf("fresh bytes under stale identity: %+v %+v %v", current.Photo, cfg, err)
	}
	if objectGets(f, "a.jpg")-before != 1 {
		t.Fatal("repair reread the original")
	}
	if _, err := os.Stat(th.path(old.Photo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repair restored old thumbnail")
	}
	if removed, err := sc.photos.RemoveIfUnchanged(context.Background(), old.Photo); err != nil || removed {
		t.Fatalf("old cleanup deleted replacement: %v %v", removed, err)
	}
	f.del("a.jpg")
	if removed, err := sc.photos.RemoveIfUnchanged(context.Background(), current.Photo); err != nil || !removed {
		t.Fatalf("current missing photo not removed: %v %v", removed, err)
	}
	if _, err := sc.photos.Thumbnail(context.Background(), current.Photo.ID, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted photo still browsable: %v", err)
	}
}

func TestPhotoProcessingUnchangedListingNeedsNoObjectRequests(t *testing.T) {
	f, _, _, sc := s3Harness(t, 1, nil)
	f.put("a.jpg", fixtureJPEG(color.RGBA{1, 2, 3, 255}))
	sc.run(context.Background())
	before := len(f.log())
	sc.run(context.Background())
	for _, request := range f.log()[before:] {
		if strings.Contains(request, "/jan/a.jpg") {
			t.Fatalf("unchanged scan made an object request: %s", request)
		}
	}
}

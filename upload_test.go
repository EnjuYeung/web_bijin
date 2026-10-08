package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type uploadTestApp struct {
	server  *httptest.Server
	store   *store
	scanner *scanner
	cfg     config
	cookie  *http.Cookie
}

func newUploadTestApp(t *testing.T, fake *fakeS3) *uploadTestApp {
	t.Helper()
	data, photos := t.TempDir(), t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{PhotosDir: photos, DataDir: data, TZ: "Asia/Shanghai", MaxPixels: 1_000_000, ThumbWorkers: 2}
	sources := newSourceSet(&localPhotoSource{root: photos})
	if fake != nil {
		c := fake.config()
		c.ID = 0
		id, err := st.saveStorage(c)
		if err != nil {
			t.Fatal(err)
		}
		c.ID = id
		sources.useStorages([]s3Config{c})
	}
	thumbs := newThumbCache(cfg.ThumbDir(), cfg.WallDir(), sources, cfg.ThumbWorkers)
	sc := newScanner(cfg, st, thumbs, sources)
	gate := newAuthGate("upload-test", "fixture-password", nil, bytes.Repeat([]byte{4}, 32))
	server := httptest.NewServer(newRouter(st, sc, thumbs, sources, cfg.TZ, gate))
	t.Cleanup(func() { server.Close(); st.Close() })
	return &uploadTestApp{server, st, sc, cfg, &http.Cookie{Name: sessionCookie, Value: gate.sign(time.Now().Add(time.Hour).Unix())}}
}

func (a *uploadTestApp) request(t *testing.T, method, endpoint string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, a.server.URL+endpoint, bytes.NewReader(body))
	req.AddCookie(a.cookie)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return res, data
}

func uploadTestJPEG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (a *uploadTestApp) prepare(t *testing.T, target, rel string, size int) struct {
	Task    uploadTask        `json:"task"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
} {
	t.Helper()
	body, _ := json.Marshal(uploadInput{Target: target, Path: rel, Size: int64(size)})
	res, data := a.request(t, http.MethodPost, "/api/uploads", body)
	if res.StatusCode >= 400 {
		t.Fatalf("prepare status=%d body=%s", res.StatusCode, data)
	}
	var prepared struct {
		Task    uploadTask        `json:"task"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &prepared); err != nil {
		t.Fatal(err)
	}
	return prepared
}

func (a *uploadTestApp) wait(t *testing.T, id string) uploadTask {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, data := a.request(t, http.MethodGet, "/api/uploads/"+id, nil)
		var result struct {
			Task uploadTask `json:"task"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Task.Phase != "processing" && result.Task.Phase != "uploading" && result.Task.Phase != "waiting" {
			return result.Task
		}
		if time.Now().After(deadline) {
			t.Fatal("upload processing did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUploadLocalFeedsExistingPhotoAndWallpaperWorkflow(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := uploadTestJPEG(t, color.RGBA{180, 80, 30, 255})
	prepared := a.prepare(t, "local", "旅行/海边 日落.jpg", len(data))
	res, body := a.request(t, http.MethodPut, prepared.URL, data)
	if res.StatusCode != 200 {
		t.Fatalf("local upload %d: %s", res.StatusCode, body)
	}
	task := a.wait(t, prepared.Task.ID)
	if task.Phase != "done" || !task.Saved {
		t.Fatalf("task: %+v", task)
	}
	saved, err := os.ReadFile(filepath.Join(a.cfg.PhotosDir, "旅行", "海边 日落.jpg"))
	if err != nil || !bytes.Equal(saved, data) {
		t.Fatalf("saved file mismatch: %v", err)
	}
	p, ok, err := a.store.getBySourceKey("旅行/海边 日落.jpg")
	if err != nil || !ok || p.Broken || p.Width != 64 {
		t.Fatalf("indexed photo: %+v %v", p, err)
	}
	if !a.scanner.thumbs.exists(p) {
		t.Fatal("thumbnail missing")
	}
	walls, err := a.store.wallpapersOf(p.ID)
	if err != nil || len(walls) == 0 {
		t.Fatalf("wallpapers missing: %v", err)
	}
	res, _ = a.request(t, http.MethodGet, "/original/"+fmtID(p.ID), nil)
	if res.StatusCode != 200 {
		t.Fatal("uploaded original cannot be opened")
	}
	res, _ = a.request(t, http.MethodGet, "/v1/backgrounds/random?format=json", nil)
	if res.StatusCode != 200 {
		t.Fatal("uploaded photo not available to wallpaper API")
	}
	files, _ := os.ReadDir(a.cfg.PhotosDir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".bijin-upload-") {
			t.Fatal("temporary upload file leaked")
		}
	}
}

func fmtID(id int64) string { return fmt.Sprintf("%d", id) }

func TestUploadRejectsChildFoldersAndInvalidPathsBeforeWriting(t *testing.T) {
	a := newUploadTestApp(t, nil)
	for _, rel := range []string{"旅行/杭州/a.jpg", "../escape.jpg", "/absolute.jpg", "旅行/.hidden.jpg", "旅行\\\\a.jpg", "notes.txt", "x/../a.jpg"} {
		body, _ := json.Marshal(uploadInput{Target: "local", Path: rel, Size: 10})
		res, _ := a.request(t, http.MethodPost, "/api/uploads", body)
		if res.StatusCode != 400 {
			t.Errorf("path=%q status=%d", rel, res.StatusCode)
		}
	}
	for _, size := range []int64{0, uploadMaxBytes + 1} {
		body, _ := json.Marshal(uploadInput{Target: "local", Path: "a.jpg", Size: size})
		res, _ := a.request(t, http.MethodPost, "/api/uploads", body)
		if res.StatusCode != 400 {
			t.Errorf("size=%d status=%d", size, res.StatusCode)
		}
	}
	if rows, _ := os.ReadDir(a.cfg.PhotosDir); len(rows) != 0 {
		t.Fatal("invalid request created files")
	}
}

func TestUploadAuthenticationAndOriginChecks(t *testing.T) {
	a := newUploadTestApp(t, nil)
	for _, endpoint := range []string{"/api/upload-targets", "/api/uploads/missing"} {
		res, err := http.Get(a.server.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("unauthenticated %s status=%d", endpoint, res.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, a.server.URL+"/api/uploads", strings.NewReader(`{"target":"local","path":"a.jpg","size":1}`))
	req.AddCookie(a.cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://other.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin write accepted")
	}
	req, _ = http.NewRequest(http.MethodPost, a.server.URL+"/api/uploads", strings.NewReader("target=local"))
	req.AddCookie(a.cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatal("simple form write accepted")
	}
}

func TestUploadLocalNeverOverwritesAndRejectsIncompleteData(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := uploadTestJPEG(t, color.RGBA{80, 110, 160, 255})
	p := a.prepare(t, "local", "同名.jpg", len(data))
	res, _ := a.request(t, http.MethodPut, p.URL, data)
	if res.StatusCode != 200 {
		t.Fatal("first upload failed")
	}
	if task := a.wait(t, p.Task.ID); task.Phase != "done" {
		t.Fatal(task.Message)
	}
	duplicate := a.prepare(t, "local", "同名.jpg", len(data))
	if duplicate.Task.Phase != "skipped" {
		t.Fatal("existing file not skipped")
	}
	p = a.prepare(t, "local", "broken.jpg", len(data))
	res, _ = a.request(t, http.MethodPut, p.URL, data[:len(data)/2])
	if res.StatusCode != 400 {
		t.Fatal("incomplete upload accepted")
	}
	if _, err := os.Stat(filepath.Join(a.cfg.PhotosDir, "broken.jpg")); !os.IsNotExist(err) {
		t.Fatal("partial image published")
	}
	saved, _ := os.ReadFile(filepath.Join(a.cfg.PhotosDir, "同名.jpg"))
	if !bytes.Equal(saved, data) {
		t.Fatal("existing image changed")
	}
}

func TestUploadLocalConcurrentCollisionHasOneWinner(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := uploadTestJPEG(t, color.RGBA{90, 140, 50, 255})
	first, second := a.prepare(t, "local", "并发.jpg", len(data)), a.prepare(t, "local", "并发.jpg", len(data))
	var wg sync.WaitGroup
	for _, prepared := range []struct {
		Task    uploadTask        `json:"task"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}{first, second} {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			res, _ := a.request(t, http.MethodPut, link, data)
			if res.StatusCode != 200 {
				t.Errorf("collision status %d", res.StatusCode)
			}
		}(prepared.URL)
	}
	wg.Wait()
	x, y := a.wait(t, first.Task.ID), a.wait(t, second.Task.ID)
	if !((x.Phase == "done" && y.Phase == "skipped") || (y.Phase == "done" && x.Phase == "skipped")) {
		t.Fatalf("phases: %s %s", x.Phase, y.Phase)
	}
	if count, _ := a.store.countOK(); count != 1 {
		t.Fatalf("indexed %d copies", count)
	}
}

func TestUploadLocalCannotFollowSymlinkOutsideRoot(t *testing.T) {
	a := newUploadTestApp(t, nil)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(a.cfg.PhotosDir, "outside")); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(uploadInput{Target: "local", Path: "outside/escape.jpg", Size: 10})
	res, data := a.request(t, http.MethodPost, "/api/uploads", body)
	if res.StatusCode < 400 {
		var p struct {
			URL string `json:"url"`
		}
		json.Unmarshal(data, &p)
		res, _ = a.request(t, http.MethodPut, p.URL, []byte("1234567890"))
		if res.StatusCode < 400 {
			t.Fatal("symlink upload accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.jpg")); !os.IsNotExist(err) {
		t.Fatal("write escaped root")
	}
}

func TestUploadS3ReusesConfiguredKeyAndIndexesSavedObject(t *testing.T) {
	fake := newFakeS3(t, "family")
	a := newUploadTestApp(t, fake)
	data := uploadTestJPEG(t, color.RGBA{180, 100, 100, 255})
	p := a.prepare(t, "s3-1", "家人/云端 日落.jpg", len(data))
	u, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.Query().Get("X-Amz-Credential"), "test-ak/") {
		t.Fatal("configured key not used")
	}
	for _, header := range []string{"if-none-match", "content-length", "content-type", "x-amz-meta-bijin-upload"} {
		if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), header) {
			t.Errorf("unsigned header %s", header)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, p.URL, bytes.NewReader(data))
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("S3 put status %d", res.StatusCode)
	}
	res, body := a.request(t, http.MethodPost, "/api/uploads/"+p.Task.ID+"/complete", []byte("{}"))
	if res.StatusCode != 200 {
		t.Fatalf("complete %d: %s", res.StatusCode, body)
	}
	task := a.wait(t, p.Task.ID)
	if task.Phase != "done" {
		t.Fatalf("S3 processing: %s", task.Message)
	}
	photo, ok, _ := a.store.getBySourceKey(storageKeyPrefix(1) + "家人/云端 日落.jpg")
	if !ok || photo.Broken || !a.scanner.thumbs.exists(photo) {
		t.Fatal("S3 upload not in existing gallery workflow")
	}
	walls, _ := a.store.wallpapersOf(photo.ID)
	if len(walls) == 0 {
		t.Fatal("S3 upload wallpaper missing")
	}
	if duplicate := a.prepare(t, "s3-1", "家人/云端 日落.jpg", len(data)); duplicate.Task.Phase != "skipped" {
		t.Fatal("S3 collision not skipped")
	}
}

func TestUploadS3DoesNotReportCachedBrokenImageAsDone(t *testing.T) {
	fake := newFakeS3(t, "family")
	a := newUploadTestApp(t, fake)
	data := []byte("this is not a jpeg")
	p := a.prepare(t, "s3-1", "坏图.jpg", len(data))
	req, _ := http.NewRequest(http.MethodPut, p.URL, bytes.NewReader(data))
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// Model a storage notification arriving before the browser's completion.
	src := a.scanner.sources.all()[1].Src.(*s3PhotoSource)
	object, err := src.Stat(context.Background(), "坏图.jpg")
	if err != nil {
		t.Fatal(err)
	}
	object.Key = storageKeyPrefix(1) + object.RelPath
	if err := a.scanner.ingest(context.Background(), object); err == nil {
		t.Fatal("broken image was accepted")
	}
	res, body := a.request(t, http.MethodPost, "/api/uploads/"+p.Task.ID+"/complete", []byte("{}"))
	if res.StatusCode != 200 {
		t.Fatalf("completion %d: %s", res.StatusCode, body)
	}
	task := a.wait(t, p.Task.ID)
	if task.Phase != "error" || !task.Saved || task.Retryable {
		t.Fatalf("cached broken image status: %+v", task)
	}
}

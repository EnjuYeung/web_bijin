package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func originalHarness(t *testing.T, edit func(*s3Config)) (*fakeS3, *store, *scanner, *httptest.Server, *http.Cookie) {
	t.Helper()
	f, st, th, sc := s3Harness(t, 1, edit)
	f.put("旅行/a b.jpg", fixtureJPEG(color.RGBA{10, 20, 30, 255}))
	sc.run(context.Background())
	srv := httptest.NewServer(newRouter(st, sc, th, sc.sources, "Asia/Shanghai", testGate()))
	t.Cleanup(srv.Close)
	return f, st, sc, srv, loginCookie(t, srv, "juen", "secret")
}

func getOriginal(t *testing.T, srv *httptest.Server, cookie *http.Cookie, id int64) (*http.Response, []byte) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/original/%d", srv.URL, id), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func TestOriginalRedirectsToPresignedURL(t *testing.T) {
	f, st, sc, srv, cookie := originalHarness(t, func(c *s3Config) {
		c.DirectOriginal = true
		c.PublicEndpoint = "https://s3.example.com"
	})
	key := storageKeyPrefix(1) + "旅行/a b.jpg"
	p, _, _ := st.getBySourceKey(key)
	res, _ := getOriginal(t, srv, cookie, p.ID)
	loc, err := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || err != nil || res.Header.Get("Cache-Control") != "private, max-age=3600" {
		t.Fatalf("status %d, Location %q, Cache-Control %q", res.StatusCode, res.Header.Get("Location"), res.Header.Get("Cache-Control"))
	}
	q := loc.Query()
	if loc.Scheme != "https" || loc.Host != "s3.example.com" || loc.Path != "/jan/旅行/a b.jpg" ||
		!strings.HasPrefix(q.Get("X-Amz-Credential"), "test-ak/") || q.Get("X-Amz-Expires") != "86400" || q.Get("X-Amz-Signature") == "" ||
		q.Get("response-cache-control") != "private, max-age=43200, immutable" || q.Get("response-content-type") != "image/jpeg" {
		t.Fatalf("presigned URL %s", loc)
	}
	// A SigV4 URL is fixed within one second, so ask again in the next second:
	// only the reuse cache can return the same URL.
	time.Sleep(1100 * time.Millisecond)
	if again, _ := getOriginal(t, srv, cookie, p.ID); again.Header.Get("Location") != res.Header.Get("Location") {
		t.Fatal("same version got a new URL, the browser cache would miss")
	}
	if unauth, _ := getOriginal(t, srv, nil, p.ID); unauth.StatusCode != http.StatusUnauthorized || unauth.Header.Get("Location") != "" {
		t.Fatalf("signed-out request got %d %q", unauth.StatusCode, unauth.Header.Get("Location"))
	}
	f.put("旅行/a b.jpg", append(fixtureJPEG(color.RGBA{200, 20, 30, 255}), 0, 0))
	sc.run(context.Background())
	if changed, _ := getOriginal(t, srv, cookie, p.ID); changed.StatusCode != http.StatusFound || changed.Header.Get("Location") == res.Header.Get("Location") {
		t.Fatal("new version reused the old URL")
	}
}

func TestPresignedURLReachesTheObject(t *testing.T) {
	f, st, _, srv, cookie := originalHarness(t, func(c *s3Config) { c.DirectOriginal = true })
	p, _, _ := st.getBySourceKey(storageKeyPrefix(1) + "旅行/a b.jpg")
	res, _ := getOriginal(t, srv, cookie, p.ID)
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), f.srv.URL+"/jan/") {
		t.Fatalf("empty browser address must default to the endpoint: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	direct, err := http.Get(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Body.Close()
	body, _ := io.ReadAll(direct.Body)
	f.mu.Lock()
	want := f.objects["旅行/a b.jpg"]
	f.mu.Unlock()
	if direct.StatusCode != 200 || !bytes.Equal(body, want) {
		t.Fatalf("storage answered %d with %d bytes", direct.StatusCode, len(body))
	}
}

func TestOriginalStreamsWhenDirectIsOff(t *testing.T) {
	f, st, _, srv, cookie := originalHarness(t, nil)
	p, _, _ := st.getBySourceKey(storageKeyPrefix(1) + "旅行/a b.jpg")
	res, body := getOriginal(t, srv, cookie, p.ID)
	f.mu.Lock()
	want := f.objects["旅行/a b.jpg"]
	f.mu.Unlock()
	if res.StatusCode != 200 || res.Header.Get("Location") != "" || res.Header.Get("Cache-Control") != "private, no-cache" || !bytes.Equal(body, want) {
		t.Fatalf("status %d Cache-Control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
}

func TestStorageDirectOriginalFields(t *testing.T) {
	c := s3Config{Endpoint: "http://1Panel-rustfs:9000", Bucket: "jan", AccessKey: "a", SecretKey: "s", PublicEndpoint: " s3.example.com/ ", DirectOriginal: true}
	n, err := c.normalize()
	if err != nil || n.PublicEndpoint != "https://s3.example.com" {
		t.Fatalf("%+v %v", n, err)
	}
	c.PublicEndpoint = "ftp://s3.example.com"
	if _, err := c.normalize(); err == nil || !strings.Contains(err.Error(), "浏览器访问地址") {
		t.Fatalf("bad browser address accepted: %v", err)
	}

	path := filepath.Join(t.TempDir(), "bijin.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The storages table as written by the previous release.
	if _, err := old.Exec(`CREATE TABLE storages (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, endpoint TEXT NOT NULL,
		region TEXT NOT NULL DEFAULT '', bucket TEXT NOT NULL, prefix TEXT NOT NULL DEFAULT '', access_key TEXT NOT NULL,
		secret_key TEXT NOT NULL, addressing TEXT NOT NULL DEFAULT 'auto', list_v1 INTEGER NOT NULL DEFAULT 0);
		INSERT INTO storages (name, endpoint, bucket, access_key, secret_key) VALUES ('RustFS', 'https://s3.example.com', 'jan', 'a', 's')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.getStorage(1)
	if err != nil || got.DirectOriginal || got.PublicEndpoint != "" || got.Bucket != "jan" {
		t.Fatalf("migrated %+v %v", got, err)
	}
	n.ID = 1
	if _, err := st.saveStorage(n); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.getStorage(1); !got.DirectOriginal || got.PublicEndpoint != "https://s3.example.com" || got.Endpoint != "http://1Panel-rustfs:9000" {
		t.Fatalf("saved %+v", got)
	}
}

func TestSettingsAPIDirectOriginalAndEvents(t *testing.T) {
	h := newSettingsHarness(t)
	f := newFakeS3(t, "photos")
	in := map[string]any{"name": "云端", "endpoint": f.srv.URL, "bucket": "photos", "accessKey": "test-ak", "secretKey": "test-sk",
		"addressing": "path", "directOriginal": true, "publicEndpoint": "https://s3.example.com"}
	if code, _, raw := h.json(t, http.MethodPost, "/api/storages", in); code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	code, out, raw := h.json(t, http.MethodGet, "/api/settings", nil)
	storages, _ := out["storages"].([]any)
	if code != 200 || len(storages) != 1 {
		t.Fatalf("settings %d %s", code, raw)
	}
	s := storages[0].(map[string]any)
	events, _ := out["events"].(map[string]any)
	if s["directOriginal"] != true || s["publicEndpoint"] != "https://s3.example.com" || events == nil || events["enabled"] != false {
		t.Fatalf("settings %s", raw)
	}
	if strings.Contains(raw, "test-sk") {
		t.Fatal("secret key returned")
	}
}

func TestLoadConfigWorkersAndEvents(t *testing.T) {
	t.Setenv("AUTH_USER", "juen")
	t.Setenv("AUTH_PASS", "secret")
	t.Setenv("PHOTOS_DIR", t.TempDir())
	t.Setenv("DATA_DIR", t.TempDir())
	for _, k := range []string{"THUMB_WORKERS", "EVENTS_LISTEN", "EVENTS_TOKEN", "SCAN_EVERY"} {
		t.Setenv(k, "")
	}
	cfg, err := loadConfig()
	if err != nil || cfg.ThumbWorkers < 1 || cfg.EventsListen != "" || cfg.ScanEvery != 2*time.Minute {
		t.Fatalf("defaults %+v %v", cfg, err)
	}
	t.Setenv("THUMB_WORKERS", "6")
	if cfg, err := loadConfig(); err != nil || cfg.ThumbWorkers != 6 {
		t.Fatalf("workers %+v %v", cfg, err)
	}
	for _, bad := range []string{"0", "65", "many"} {
		t.Setenv("THUMB_WORKERS", bad)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("THUMB_WORKERS=%s accepted", bad)
		}
	}
	t.Setenv("THUMB_WORKERS", "4")
	t.Setenv("EVENTS_LISTEN", "5002")
	t.Setenv("EVENTS_TOKEN", "short")
	if _, err := loadConfig(); err == nil {
		t.Fatal("short events token accepted")
	}
	t.Setenv("EVENTS_TOKEN", testEventToken)
	if cfg, err := loadConfig(); err != nil || cfg.EventsListen != ":5002" || cfg.ScanEvery != 30*time.Minute {
		t.Fatalf("events %+v %v", cfg, err)
	}
	t.Setenv("SCAN_EVERY", "5m")
	if cfg, err := loadConfig(); err != nil || cfg.ScanEvery != 5*time.Minute {
		t.Fatalf("explicit scan interval %+v %v", cfg, err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageNormalize(t *testing.T) {
	base := s3Config{Endpoint: "s3.example.com", Bucket: "photos", AccessKey: " ak ", SecretKey: " sk "}
	c, err := base.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://s3.example.com" || c.Name != "photos" || c.Addressing != addressingAuto || c.AccessKey != "ak" || c.SecretKey != "sk" || c.Region != "" {
		t.Fatalf("defaults: %+v", c)
	}
	ok := map[string]string{
		"http://192.168.1.2:9000":   "http://192.168.1.2:9000",
		"https://s3.amazonaws.com/": "https://s3.amazonaws.com",
		"minio.lan:9000":            "https://minio.lan:9000",
	}
	for in, want := range ok {
		b := base
		b.Endpoint = in
		c, err := b.normalize()
		if err != nil || c.Endpoint != want {
			t.Fatalf("%q -> %q, %v", in, c.Endpoint, err)
		}
	}
	b := base
	b.Prefix = "/albums/2026/"
	if c, err := b.normalize(); err != nil || c.Prefix != "albums/2026" {
		t.Fatalf("prefix %q %v", c.Prefix, err)
	}
	bad := []func(*s3Config){
		func(c *s3Config) { c.Endpoint = "" },
		func(c *s3Config) { c.Endpoint = "ftp://s3.example.com" },
		func(c *s3Config) { c.Endpoint = "https://s3.example.com/photos" },
		func(c *s3Config) { c.Endpoint = "https://s3.example.com?x=1" },
		func(c *s3Config) { c.Endpoint = "https://user:pw@s3.example.com" },
		func(c *s3Config) { c.Bucket = "" },
		func(c *s3Config) { c.Bucket = "a" },
		func(c *s3Config) { c.Prefix = "a/../b" },
		func(c *s3Config) { c.AccessKey = "" },
		func(c *s3Config) { c.SecretKey = "  " },
		func(c *s3Config) { c.Addressing = "dns" },
		func(c *s3Config) { c.Region = "us east" },
		func(c *s3Config) { c.Name = strings.Repeat("名", 41) },
	}
	for i, mutate := range bad {
		b := base
		mutate(&b)
		_, err := b.normalize()
		var ue *userError
		if !errors.As(err, &ue) || ue.msg == "" {
			t.Fatalf("case %d: expected user error, got %v", i, err)
		}
	}
}

func TestStorageStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bijin.db")
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode: %v %v", p, info.Mode(), err)
		}
	}
	a, _ := s3Config{Name: "A", Endpoint: "https://a.example.com", Bucket: "one", AccessKey: "ak", SecretKey: "sk", ListV1: true}.normalize()
	idA, err := st.saveStorage(a)
	if err != nil || idA != 1 {
		t.Fatalf("insert %d %v", idA, err)
	}
	b := a
	b.Name = "B"
	if _, err := st.saveStorage(b); err == nil || !strings.Contains(err.Error(), "已经添加过") {
		t.Fatalf("duplicate accepted: %v", err)
	}
	b.Prefix = "other"
	idB, err := st.saveStorage(b)
	if err != nil || idB != 2 {
		t.Fatalf("second insert %d %v", idB, err)
	}
	a.ID = idA
	a.Region = "eu-west-1"
	if _, err := st.saveStorage(a); err != nil {
		t.Fatal(err)
	}
	got, err := st.getStorage(idA)
	if err != nil || got.Region != "eu-west-1" || !got.ListV1 || got.SecretKey != "sk" {
		t.Fatalf("get %+v %v", got, err)
	}
	if err := st.deleteStorage(idB); err != nil {
		t.Fatal(err)
	}
	if err := st.deleteStorage(idB); !errors.Is(err, errStorageNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// IDs are never reused, so a new storage cannot inherit old index keys.
	c := b
	c.ID = 0
	idC, err := st.saveStorage(c)
	if err != nil || idC != 3 {
		t.Fatalf("reused id %d %v", idC, err)
	}
	list, err := st.listStorages()
	if err != nil || len(list) != 2 || list[0].ID != 1 || list[1].ID != 3 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := st.saveStorage(s3Config{ID: 99, Endpoint: "https://z", Bucket: "zzz"}); !errors.Is(err, errStorageNotFound) {
		t.Fatalf("update missing: %v", err)
	}
}

type settingsHarness struct {
	srv     *httptest.Server
	st      *store
	sc      *scanner
	sources *sourceSet
	cookie  *http.Cookie
}

func newSettingsHarness(t *testing.T) *settingsHarness {
	t.Helper()
	photos := t.TempDir()
	data := t.TempDir()
	writeJPEG(t, filepath.Join(photos, "local.jpg"), 300, 200, color.RGBA{200, 80, 40, 255})
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sources := newSourceSet(&localPhotoSource{root: photos})
	th := newThumbCache(filepath.Join(data, "thumbs"), sources)
	sc := newScanner(config{PhotosDir: "/photos", PhotosHostDir: "/mnt/user/photos", DataDir: data, ScanEvery: time.Hour, MaxPixels: 64_000_000}, st, th, sources)
	srv := httptest.NewServer(newRouter(st, sc, th, sources, "Asia/Shanghai", testGate()))
	t.Cleanup(srv.Close)
	return &settingsHarness{srv: srv, st: st, sc: sc, sources: sources, cookie: loginCookie(t, srv, "juen", "secret")}
}

func (h *settingsHarness) do(t *testing.T, method, path, contentType, body string, auth bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth {
		req.AddCookie(h.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (h *settingsHarness) json(t *testing.T, method, path string, v any) (int, map[string]any, string) {
	t.Helper()
	body := ""
	if v != nil {
		b, _ := json.Marshal(v)
		body = string(b)
	}
	code, raw := h.do(t, method, path, "application/json", body, true)
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return code, out, raw
}

func TestSettingsAPIRequiresLogin(t *testing.T) {
	h := newSettingsHarness(t)
	for _, r := range [][2]string{{"GET", "/api/settings"}, {"POST", "/api/storages"}, {"PUT", "/api/storages/1"}, {"DELETE", "/api/storages/1"}, {"POST", "/api/storages/test"}} {
		if code, _ := h.do(t, r[0], r[1], "application/json", "{}", false); code != http.StatusUnauthorized {
			t.Fatalf("%s %s without login = %d", r[0], r[1], code)
		}
	}
	res, err := http.Get(h.srv.URL + "/settings.js")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("settings.js: %v %d", err, res.StatusCode)
	}
	res.Body.Close()
}

func TestSettingsAPIStorageLifecycle(t *testing.T) {
	h := newSettingsHarness(t)
	f := newFakeS3(t, "photos")
	f.put("album/remote.jpg", fixtureJPEG(color.RGBA{30, 60, 90, 255}))
	f.put("album/remote2.jpg", fixtureJPEG(color.RGBA{90, 60, 30, 255}))
	if err := h.sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}

	code, got, _ := h.json(t, "GET", "/api/settings", nil)
	local := got["local"].(map[string]any)
	if code != 200 || local["hostDir"] != "/mnt/user/photos" || local["containerDir"] != "/photos" || local["photos"].(float64) != 1 || got["scanEvery"].(float64) != 3600 {
		t.Fatalf("settings: %d %v", code, got)
	}
	if len(got["storages"].([]any)) != 0 {
		t.Fatal("unexpected storages")
	}

	if code, _ := h.do(t, "POST", "/api/storages", "text/plain", `{}`, true); code != http.StatusUnsupportedMediaType {
		t.Fatalf("plain text accepted: %d", code)
	}
	if code, _ := h.do(t, "POST", "/api/storages", "application/json", `{`, true); code != http.StatusBadRequest {
		t.Fatalf("broken json: %d", code)
	}
	input := map[string]any{"name": "测试存储", "endpoint": f.srv.URL, "bucket": "photos", "prefix": "album", "accessKey": "test-ak", "secretKey": "test-sk", "addressing": "path"}
	missing := map[string]any{"endpoint": f.srv.URL, "accessKey": "a", "secretKey": "b"}
	if code, body, _ := h.json(t, "POST", "/api/storages", missing); code != 400 || !strings.Contains(body["error"].(string), "Bucket") {
		t.Fatalf("missing bucket: %d %v", code, body)
	}

	code, body, raw := h.json(t, "POST", "/api/storages", input)
	if code != http.StatusCreated || strings.Contains(raw, "test-sk") || strings.Contains(raw, "secretKey") {
		t.Fatalf("create: %d %s", code, raw)
	}
	storage := body["storage"].(map[string]any)
	id := int64(storage["id"].(float64))
	if storage["hasSecret"] != true || storage["prefix"] != "album" {
		t.Fatalf("create response: %v", storage)
	}
	if s := h.sc.snapshot(); !s.Queued {
		t.Fatal("saving did not queue a scan")
	}
	if len(h.sources.all()) != 2 {
		t.Fatal("storage not applied")
	}
	if code, body, _ := h.json(t, "POST", "/api/storages", input); code != 400 || !strings.Contains(body["error"].(string), "已经添加过") {
		t.Fatalf("duplicate: %d %v", code, body)
	}

	if err := h.sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, got, raw = h.json(t, "GET", "/api/settings", nil)
	storages := got["storages"].([]any)
	if code != 200 || len(storages) != 1 || strings.Contains(raw, "test-sk") {
		t.Fatalf("settings after create: %s", raw)
	}
	view := storages[0].(map[string]any)
	status := view["status"].(map[string]any)
	if view["photos"].(float64) != 2 || view["name"] != "测试存储" || status["seen"].(float64) != 2 || status["err"] != nil {
		t.Fatalf("storage view: %v", view)
	}

	// Photos from the storage are listed and their originals are streamed.
	// The storage prefix is not part of the display path, so its top-level
	// photos join the local root album.
	code, list, _ := h.json(t, "GET", "/api/photos?album=.", nil)
	if code != 200 || list["total"].(float64) != 3 {
		t.Fatalf("merged root album: %v", list)
	}
	if code, list, _ = h.json(t, "GET", "/api/photos?album=album", nil); code != 200 || list["total"].(float64) != 0 {
		t.Fatalf("prefix leaked into album path: %v", list)
	}
	code, list, _ = h.json(t, "GET", "/api/photos", nil)
	if code != 200 || list["total"].(float64) != 3 {
		t.Fatalf("photos: %v", list)
	}
	var remoteID float64
	for _, p := range list["photos"].([]any) {
		if p.(map[string]any)["name"] == "remote.jpg" {
			remoteID = p.(map[string]any)["id"].(float64)
		}
	}
	code, orig := h.do(t, "GET", "/original/"+jsonNumber(remoteID), "", "", true)
	if code != 200 || !bytes.Equal([]byte(orig), fixtureJPEG(color.RGBA{30, 60, 90, 255})) {
		t.Fatalf("original from storage: %d len=%d", code, len(orig))
	}

	// Updating with an empty secret keeps the saved one.
	edit := map[string]any{"name": "改名", "endpoint": f.srv.URL, "bucket": "photos", "prefix": "album", "accessKey": "test-ak", "secretKey": "", "addressing": "path"}
	if code, body, _ := h.json(t, "PUT", "/api/storages/"+jsonNumber(float64(id)), edit); code != 200 {
		t.Fatalf("update: %d %v", code, body)
	}
	saved, _ := h.st.getStorage(id)
	if saved.Name != "改名" || saved.SecretKey != "test-sk" {
		t.Fatalf("saved after update: %+v", saved)
	}
	edit["secretKey"] = "new-sk"
	h.json(t, "PUT", "/api/storages/"+jsonNumber(float64(id)), edit)
	if saved, _ := h.st.getStorage(id); saved.SecretKey != "new-sk" {
		t.Fatal("secret not replaced")
	}
	if code, _, _ := h.json(t, "PUT", "/api/storages/999", edit); code != 404 {
		t.Fatalf("update missing: %d", code)
	}

	// Connection test reuses the saved secret and reports hints on failure.
	test := map[string]any{"id": id, "endpoint": f.srv.URL, "bucket": "photos", "prefix": "album", "accessKey": "test-ak", "addressing": "path"}
	if code, body, _ := h.json(t, "POST", "/api/storages/test", test); code != 200 || body["images"].(float64) != 2 {
		t.Fatalf("test: %d %v", code, body)
	}
	test["accessKey"] = "wrong"
	if code, body, _ := h.json(t, "POST", "/api/storages/test", test); code != http.StatusBadGateway || !strings.Contains(body["error"].(string), "Access Key") {
		t.Fatalf("test wrong key: %d %v", code, body)
	}
	if code, _, _ := h.json(t, "POST", "/api/storages/test", map[string]any{"endpoint": "ftp://x", "bucket": "photos", "accessKey": "a", "secretKey": "b"}); code != 400 {
		t.Fatalf("test bad endpoint: %d", code)
	}

	if code, _, _ := h.json(t, "DELETE", "/api/storages/"+jsonNumber(float64(id)), nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := h.json(t, "DELETE", "/api/storages/"+jsonNumber(float64(id)), nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
	if code, _, _ := h.json(t, "DELETE", "/api/storages/abc", nil); code != 404 {
		t.Fatalf("delete bad id: %d", code)
	}
	if len(h.sources.all()) != 1 {
		t.Fatal("deleted storage still applied")
	}
	if err := h.sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.st.countOK(); n != 1 {
		t.Fatalf("photos after delete = %d", n)
	}
}

func jsonNumber(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

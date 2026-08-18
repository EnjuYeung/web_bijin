package main

import (
	"bytes"
	"encoding/json"
	"image"
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

func testGate() *authGate {
	return newAuthGate("juen", "secret", bytes.Repeat([]byte{7}, 32))
}

func testApp(t *testing.T) (*httptest.Server, *store, string) {
	t.Helper()
	photos := t.TempDir()
	data := t.TempDir()
	writeJPEG(t, filepath.Join(photos, "wide.jpg"), 400, 200, color.RGBA{200, 80, 40, 255})
	writeJPEG(t, filepath.Join(photos, "tall.jpg"), 200, 400, color.RGBA{40, 80, 160, 255})
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.upsert(photo{RelPath: "wide.jpg", Size: 111, MtimeUnix: time.Now().Unix(), Width: 400, Height: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.upsert(photo{RelPath: "tall.jpg", Size: 222, MtimeUnix: time.Now().Unix(), Width: 200, Height: 400}); err != nil {
		t.Fatal(err)
	}
	th := newThumbCache(filepath.Join(data, "thumbs"), photos)
	sc := newScanner(config{PhotosDir: photos, DataDir: data, ScanEvery: time.Hour, MaxPixels: 64_000_000, ThumbMaxEdge: 720}, st, th)
	h := newRouter(st, sc, th, photos, "Asia/Shanghai", testGate())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, st, photos
}

func loginCookie(t *testing.T, srv *httptest.Server, user, pass string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user": user, "pass": pass})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func TestUnauthHomeRedirectsToLogin(t *testing.T) {
	srv, _, _ := testApp(t)
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("home %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Fatalf("location %s", loc)
	}
}

func TestUnauthAPIAndImagesRejected(t *testing.T) {
	srv, _, _ := testApp(t)
	for _, path := range []string{"/api/photos", "/thumb/1", "/original/1"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s got %d", path, res.StatusCode)
		}
		res.Body.Close()
	}
}

func TestHealthAndLoginBgPublic(t *testing.T) {
	srv, _, _ := testApp(t)
	res, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health %d", res.StatusCode)
	}
	bg, err := http.Get(srv.URL + "/api/login-bg?orient=land")
	if err != nil {
		t.Fatal(err)
	}
	defer bg.Body.Close()
	if bg.StatusCode != http.StatusOK {
		t.Fatalf("login-bg %d", bg.StatusCode)
	}
	raw, _ := io.ReadAll(bg.Body)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width <= cfg.Height {
		t.Fatalf("land bg should be wide, got %dx%d", cfg.Width, cfg.Height)
	}
	port, err := http.Get(srv.URL + "/api/login-bg?orient=port")
	if err != nil {
		t.Fatal(err)
	}
	defer port.Body.Close()
	raw, _ = io.ReadAll(port.Body)
	cfg, _, err = image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Height <= cfg.Width {
		t.Fatalf("port bg should be tall, got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestWrongPasswordStaysOut(t *testing.T) {
	srv, _, _ := testApp(t)
	body, _ := json.Marshal(map[string]string{"user": "juen", "pass": "nope"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", res.StatusCode)
	}
	if len(res.Cookies()) != 0 {
		t.Fatal("should not set cookie")
	}
}

func TestLoginThenHomeAndList(t *testing.T) {
	srv, _, _ := testApp(t)
	c := loginCookie(t, srv, "juen", "secret")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("home %d", res.StatusCode)
	}
	page, _ := io.ReadAll(res.Body)
	if !bytes.Contains(page, []byte("Juen's")) || !bytes.Contains(page, []byte("theme-btn")) {
		t.Fatal("home missing title or theme button")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/photos?limit=10", nil)
	req.AddCookie(c)
	list, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list %d", list.StatusCode)
	}
	var data struct {
		Photos []struct {
			Title  string `json:"title"`
			Format string `json:"format"`
			Size   int64  `json:"size"`
			Date   string `json:"date"`
			W, H   int
		} `json:"photos"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(list.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data.Total != 2 || len(data.Photos) != 2 {
		t.Fatalf("photos %+v", data)
	}
	if data.Photos[0].Title == "" || data.Photos[0].Format != "JPEG" || data.Photos[0].Size == 0 || data.Photos[0].Date == "" {
		t.Fatalf("metadata %+v", data.Photos[0])
	}
}

func TestIndexHTMLNotServedPublic(t *testing.T) {
	srv, _, _ := testApp(t)
	res, err := http.Get(srv.URL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("index.html should not be public")
	}
}

func TestSafeNext(t *testing.T) {
	if safeNext("") != "/" || safeNext("//evil") != "/" || safeNext("https://x") != "/" {
		t.Fatal("rejected next should be /")
	}
	if safeNext("/login") != "/" {
		t.Fatal("login next loops")
	}
	if safeNext("/#p/1") != "/#p/1" {
		t.Fatal("hash next")
	}
}

func TestSessionRejectsTamper(t *testing.T) {
	g := testGate()
	raw := g.sign(time.Now().Add(time.Hour).Unix())
	if !g.validSession(raw) {
		t.Fatal("fresh session")
	}
	if g.validSession(raw+"x") || g.validSession("v1.1.dead") {
		t.Fatal("tamper accepted")
	}
	if g.validSession(g.sign(time.Now().Add(-time.Hour).Unix())) {
		t.Fatal("expired accepted")
	}
}

func TestFormLogin(t *testing.T) {
	srv, _, _ := testApp(t)
	form := url.Values{"user": {"juen"}, "pass": {"secret"}, "next": {"/"}}
	res, err := http.PostForm(srv.URL+"/api/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("form login follow %d", res.StatusCode)
	}
}

func TestLoadSessionKeyPersists(t *testing.T) {
	dir := t.TempDir()
	a, err := loadSessionKey(dir)
	if err != nil || len(a) != 32 {
		t.Fatalf("first key %v %d", err, len(a))
	}
	b, err := loadSessionKey(dir)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("key should persist")
	}
}

func TestLoadConfigRequiresAuth(t *testing.T) {
	t.Setenv("AUTH_USER", "")
	t.Setenv("AUTH_PASS", "")
	t.Setenv("PHOTOS_DIR", t.TempDir())
	t.Setenv("DATA_DIR", t.TempDir())
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected missing auth error")
	}
	t.Setenv("AUTH_USER", "juen")
	t.Setenv("AUTH_PASS", "secret")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthUser != "juen" || cfg.AuthPass != "secret" {
		t.Fatalf("%+v", cfg)
	}
}

func TestLoginBgPrefersOrient(t *testing.T) {
	photos := []photo{
		{ID: 1, Width: 400, Height: 200},
		{ID: 2, Width: 200, Height: 400},
		{ID: 3, Width: 100, Height: 100},
	}
	land, rest := splitByOrient(photos, false)
	if len(land) != 1 || land[0].ID != 1 {
		t.Fatalf("land %v", land)
	}
	port, rest2 := splitByOrient(photos, true)
	if len(port) != 1 || port[0].ID != 2 {
		t.Fatalf("port %v", port)
	}
	if len(rest) != 2 || len(rest2) != 2 {
		t.Fatalf("fallback pools %d %d", len(rest), len(rest2))
	}
}

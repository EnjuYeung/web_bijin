package main

import (
	"bytes"
	"encoding/json"
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
	return newAuthGate("juen", "secret", nil, bytes.Repeat([]byte{7}, 32))
}

func testApp(t *testing.T) (*httptest.Server, *store, string) {
	t.Helper()
	return testAppWithGate(t, testGate())
}

func testAppWithGate(t *testing.T, gate *authGate) (*httptest.Server, *store, string) {
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
	sources := newSourceSet(&localPhotoSource{root: photos})
	th := newThumbCache(filepath.Join(data, "thumbs"), filepath.Join(data, "wallpapers"), sources, 1)
	sc := newScanner(config{PhotosDir: photos, DataDir: data, ScanEvery: time.Hour, MaxPixels: 64_000_000}, st, th, sources)
	h := newRouter(st, sc, th, sources, "Asia/Shanghai", gate)
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
	for _, path := range []string{"/api/photos", "/api/albums", "/thumb/1", "/original/1"} {
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

func TestAlbumsAndAlbumPhotoFilter(t *testing.T) {
	srv, st, _ := testApp(t)
	now := time.Now().Unix()
	for _, p := range []photo{
		{RelPath: "2025/旅行/a.jpg", Size: 333, MtimeUnix: now - 20, Width: 300, Height: 200},
		{RelPath: "2026/旅行/b.jpg", Size: 444, MtimeUnix: now - 10, Width: 200, Height: 300},
		{RelPath: "2026/旅行/c.jpg", Size: 555, MtimeUnix: now, Width: 500, Height: 300},
	} {
		if _, err := st.upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	c := loginCookie(t, srv, "juen", "secret")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/albums", nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("albums %d", res.StatusCode)
	}
	var got struct {
		Albums []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Count int    `json:"count"`
			Cover struct {
				ID int64 `json:"id"`
			} `json:"cover"`
		} `json:"albums"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Albums) != 3 {
		t.Fatalf("albums %+v", got)
	}
	var album2026 struct {
		ID    string
		Name  string
		Count int
		Cover struct{ ID int64 }
	}
	for _, a := range got.Albums {
		if a.ID == "2026/旅行" {
			album2026.ID, album2026.Name, album2026.Count, album2026.Cover.ID = a.ID, a.Name, a.Count, a.Cover.ID
		}
	}
	if album2026.Name != "旅行" || album2026.Count != 2 || album2026.Cover.ID == 0 {
		t.Fatalf("2026 album %+v", album2026)
	}

	filterURL := srv.URL + "/api/photos?limit=10&album=" + url.QueryEscape("2026/旅行")
	req, _ = http.NewRequest(http.MethodGet, filterURL, nil)
	req.AddCookie(c)
	filtered, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Body.Close()
	var list struct {
		Photos []struct {
			Name string `json:"name"`
		} `json:"photos"`
		Total int      `json:"total"`
		Album albumRef `json:"album"`
	}
	if err := json.NewDecoder(filtered.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || len(list.Photos) != 2 || list.Album.ID != "2026/旅行" || list.Album.Name != "旅行" {
		t.Fatalf("filtered %+v", list)
	}
	for _, p := range list.Photos {
		if !strings.HasPrefix(p.Name, "2026/旅行/") {
			t.Fatalf("wrong album photo %q", p.Name)
		}
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/photos?album=../etc", nil)
	req.AddCookie(c)
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid album got %d", bad.StatusCode)
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
	// Nothing was scanned, so there is no wallpaper yet: the background is
	// missing rather than sent to the login page. TestLoginBackgroundUsesWallpapers
	// covers the rest.
	bg, err := noRedirect.Get(srv.URL + "/api/login-bg?orient=land")
	if err != nil {
		t.Fatal(err)
	}
	defer bg.Body.Close()
	if bg.StatusCode != http.StatusNotFound {
		t.Fatalf("login-bg without wallpapers %d", bg.StatusCode)
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
		Total int    `json:"total"`
		Seed  string `json:"seed"`
	}
	if err := json.NewDecoder(list.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data.Total != 2 || len(data.Photos) != 2 {
		t.Fatalf("photos %+v", data)
	}
	if data.Seed == "" {
		t.Fatal("seed must be a JSON string so browsers preserve all int64 digits")
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

func logout(t *testing.T, srv *httptest.Server, method string, c *http.Cookie, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func photosStatus(t *testing.T, srv *httptest.Server, c *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/photos?limit=1", nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestLogoutSignsOnlyThisDeviceOut(t *testing.T) {
	srv, _, _ := testApp(t)
	phone := loginCookie(t, srv, "juen", "secret")
	laptop := loginCookie(t, srv, "juen", "secret")

	res := logout(t, srv, http.MethodPost, phone, map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?out=1" {
		t.Fatalf("logout %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if got := res.Header.Get("Clear-Site-Data"); got != `"cache"` {
		t.Fatalf("Clear-Site-Data %q, want only the cache cleared", got)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", res.Header.Get("Cache-Control"))
	}
	var cleared *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cleared = c
		}
	}
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 || cleared.Path != "/" || !cleared.HttpOnly {
		t.Fatalf("session cookie not removed: %+v", cleared)
	}
	if photosStatus(t, srv, laptop) != http.StatusOK {
		t.Fatal("logging out the phone signed the laptop out too")
	}
}

func TestLogoutWhenAlreadySignedOut(t *testing.T) {
	srv, _, _ := testApp(t)
	res := logout(t, srv, http.MethodPost, nil, map[string]string{"Origin": srv.URL})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?out=1" {
		t.Fatalf("logout without a session %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	expired := &http.Cookie{Name: sessionCookie, Value: testGate().sign(time.Now().Add(-time.Hour).Unix())}
	res = logout(t, srv, http.MethodPost, expired, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout with an expired session %d", res.StatusCode)
	}
}

func TestLogoutRefusesOtherSitesAndGet(t *testing.T) {
	srv, _, _ := testApp(t)
	c := loginCookie(t, srv, "juen", "secret")
	for name, headers := range map[string]map[string]string{
		"cross-site fetch":   {"Sec-Fetch-Site": "cross-site"},
		"foreign origin":     {"Origin": "https://evil.example"},
		"non-http(s) origin": {"Origin": "null"},
	} {
		res := logout(t, srv, http.MethodPost, c, headers)
		if res.StatusCode != http.StatusForbidden || len(res.Cookies()) != 0 || res.Header.Get("Clear-Site-Data") != "" {
			t.Fatalf("%s: %d, cookies %v, Clear-Site-Data %q", name, res.StatusCode, res.Cookies(), res.Header.Get("Clear-Site-Data"))
		}
	}
	// A link or image tag cannot sign anyone out.
	res := logout(t, srv, http.MethodGet, c, nil)
	if res.StatusCode != http.StatusMethodNotAllowed || len(res.Cookies()) != 0 {
		t.Fatalf("GET /logout %d, cookies %v", res.StatusCode, res.Cookies())
	}
	if photosStatus(t, srv, c) != http.StatusOK {
		t.Fatal("refused logouts must leave the session working")
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

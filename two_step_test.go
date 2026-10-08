package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rsc.io/qr"
)

// testSecret is RFC 6238's SHA-1 test key "12345678901234567890".
const testSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func twoStepGate(t *testing.T, clock *testClock) *authGate {
	t.Helper()
	key, err := parseTwoStepSecret(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	g := newAuthGate("juen", "secret", key, bytes.Repeat([]byte{7}, 32))
	g.now = clock.now
	return g
}

func codeAt(t *testing.T, at time.Time) string {
	t.Helper()
	key, _ := parseTwoStepSecret(testSecret)
	return stepCode(key, at.Unix()/codeStep)
}

func postLogin(t *testing.T, srv *httptest.Server, ip string, body map[string]string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if ip != "" {
		req.Header.Set("X-Real-IP", ip)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestStepCodeMatchesRFC6238(t *testing.T) {
	key, err := parseTwoStepSecret(testSecret)
	if err != nil || string(key) != "12345678901234567890" {
		t.Fatalf("key %q %v", key, err)
	}
	// RFC 6238 appendix B, SHA-1; the app shows the last six digits.
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := stepCode(key, unix/codeStep); got != want {
			t.Fatalf("time %d: got %s want %s", unix, got, want)
		}
	}
}

func TestCodeStepAllowsOneStepOfClockDrift(t *testing.T) {
	key, _ := parseTwoStepSecret(testSecret)
	now := time.Unix(1_800_000_015, 0)
	t0 := now.Unix() / codeStep
	for d := int64(-1); d <= 1; d++ {
		step, ok := codeStepAt(key, stepCode(key, t0+d), now)
		if !ok || step != t0+d {
			t.Fatalf("drift %d: step %d ok %v", d, step, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := codeStepAt(key, stepCode(key, t0+d), now); ok {
			t.Fatalf("drift %d accepted", d)
		}
	}
	spaced := stepCode(key, t0)[:3] + " " + stepCode(key, t0)[3:]
	if _, ok := codeStepAt(key, spaced, now); !ok {
		t.Fatal("code typed with a space rejected")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := codeStepAt(key, bad, now); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestParseTwoStepSecret(t *testing.T) {
	if _, err := parseTwoStepSecret("gezd gnbv gy3t qojq gezd gnbv gy3t qojq"); err != nil {
		t.Fatalf("app-style text rejected: %v", err)
	}
	if _, err := parseTwoStepSecret(testSecret + "===="); err != nil {
		t.Fatalf("padding rejected: %v", err)
	}
	for _, bad := range []string{"GEZDGNBVGY3TQOJQ", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJ1", "not base32 at all!!!!!!!!!!!!!"} {
		if _, err := parseTwoStepSecret(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	s, err := newTwoStepSecret()
	if err != nil || len(s) != 32 {
		t.Fatalf("new secret %q %v", s, err)
	}
	if k, err := parseTwoStepSecret(s); err != nil || len(k) != 20 {
		t.Fatalf("new secret does not parse: %v", err)
	}
}

func TestLoadConfigTwoStepSecret(t *testing.T) {
	t.Setenv("AUTH_USER", "juen")
	t.Setenv("AUTH_PASS", "secret")
	t.Setenv("PHOTOS_DIR", t.TempDir())
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("AUTH_TWO_STEP_SECRET", "")
	cfg, err := loadConfig()
	if err != nil || len(cfg.TwoStepKey) != 0 {
		t.Fatalf("unset secret: %v %v", cfg.TwoStepKey, err)
	}
	t.Setenv("AUTH_TWO_STEP_SECRET", testSecret)
	if cfg, err = loadConfig(); err != nil || string(cfg.TwoStepKey) != "12345678901234567890" {
		t.Fatalf("valid secret: %q %v", cfg.TwoStepKey, err)
	}
	t.Setenv("AUTH_TWO_STEP_SECRET", testSecret[:20])
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "AUTH_TWO_STEP_SECRET") {
		t.Fatalf("broken secret must stop startup, got %v", err)
	}
}

func TestLoginOptions(t *testing.T) {
	for _, tc := range []struct {
		gate *authGate
		want bool
	}{{testGate(), false}, {twoStepGate(t, &testClock{t: time.Now()}), true}} {
		srv, _, _ := testAppWithGate(t, tc.gate)
		res, err := http.Get(srv.URL + "/api/login-options")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			TwoStep bool `json:"twoStep"`
		}
		_ = json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || got.TwoStep != tc.want || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("options %d %+v %q", res.StatusCode, got, res.Header.Get("Cache-Control"))
		}
	}
}

func TestTwoStepLoginNeedsFreshCode(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	srv, _, _ := testAppWithGate(t, twoStepGate(t, clock))
	for _, body := range []map[string]string{
		{"user": "juen", "pass": "secret"},
		{"user": "juen", "pass": "secret", "code": "000000"},
		{"user": "juen", "pass": "wrong", "code": codeAt(t, clock.now())},
	} {
		if res := postLogin(t, srv, "198.51.100.1", body); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%v got %d", body, res.StatusCode)
		}
	}
	code := codeAt(t, clock.now())
	res := postLogin(t, srv, "198.51.100.2", map[string]string{"user": "juen", "pass": "secret", "code": code})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("right code %d", res.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/photos", nil)
	req.AddCookie(cookie)
	photos, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	photos.Body.Close()
	if photos.StatusCode != http.StatusOK {
		t.Fatalf("photos with session %d", photos.StatusCode)
	}
	// A code works once, even 20 seconds later from another device.
	clock.add(20 * time.Second)
	if res := postLogin(t, srv, "198.51.100.3", map[string]string{"user": "juen", "pass": "secret", "code": code}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused code got %d", res.StatusCode)
	}
	clock.add(30 * time.Second)
	if res := postLogin(t, srv, "198.51.100.3", map[string]string{"user": "juen", "pass": "secret", "code": codeAt(t, clock.now())}); res.StatusCode != http.StatusOK {
		t.Fatalf("next code got %d", res.StatusCode)
	}
}

func TestLoginLockoutPerIP(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	srv, _, _ := testAppWithGate(t, twoStepGate(t, clock))
	wrong := map[string]string{"user": "juen", "pass": "secret", "code": "000000"}
	right := func() map[string]string {
		return map[string]string{"user": "juen", "pass": "secret", "code": codeAt(t, clock.now())}
	}
	for i := 0; i < maxLoginFails; i++ {
		if res := postLogin(t, srv, "203.0.113.9", wrong); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("miss %d got %d", i+1, res.StatusCode)
		}
	}
	res := postLogin(t, srv, "203.0.113.9", right())
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked IP with right code got %d", res.StatusCode)
	}
	if ra, _ := strconv.Atoi(res.Header.Get("Retry-After")); ra < 890 || ra > 901 {
		t.Fatalf("Retry-After %q", res.Header.Get("Retry-After"))
	}
	// Someone else's misses never lock the owner's own IP.
	if res := postLogin(t, srv, "198.51.100.7", right()); res.StatusCode != http.StatusOK {
		t.Fatalf("other IP got %d", res.StatusCode)
	}
	// Attempts while locked do not extend the lock.
	clock.add(14 * time.Minute)
	if res := postLogin(t, srv, "203.0.113.9", wrong); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("still locked got %d", res.StatusCode)
	}
	clock.add(time.Minute + 30*time.Second)
	if res := postLogin(t, srv, "203.0.113.9", right()); res.StatusCode != http.StatusOK {
		t.Fatalf("after lockout got %d", res.StatusCode)
	}
}

func TestLoginFailCountResets(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	g := twoStepGate(t, clock)
	miss := func(n int) {
		for i := 0; i < n; i++ {
			if r := g.attempt("192.0.2.4", "juen", "nope", ""); r != loginWrong {
				t.Fatalf("miss got %v", r)
			}
		}
	}
	miss(maxLoginFails - 1)
	clock.add(30 * time.Second)
	if r := g.attempt("192.0.2.4", "juen", "secret", codeAt(t, clock.now())); r != loginOK {
		t.Fatalf("success after misses got %v", r)
	}
	miss(maxLoginFails - 1)
	clock.add(loginLockout)
	miss(maxLoginFails - 1)
	if r := g.attempt("192.0.2.4", "juen", "secret", codeAt(t, clock.now())); r != loginOK {
		t.Fatalf("old misses should have expired, got %v", r)
	}
	if len(g.fails) != 0 {
		t.Fatalf("fail records left: %v", g.fails)
	}
}

func TestPasswordOnlyLoginIgnoresCode(t *testing.T) {
	g := testGate()
	if r := g.attempt("192.0.2.5", "juen", "secret", ""); r != loginOK {
		t.Fatalf("password only got %v", r)
	}
	for i := 0; i < maxLoginFails; i++ {
		g.attempt("192.0.2.5", "juen", "wrong", "")
	}
	if r := g.attempt("192.0.2.5", "juen", "secret", ""); r != loginLocked {
		t.Fatalf("password-only lockout got %v", r)
	}
}

func TestFormLoginLockedRedirect(t *testing.T) {
	srv, _, _ := testApp(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var res *http.Response
	for i := 0; i <= maxLoginFails; i++ {
		var err error
		res, err = client.PostForm(srv.URL+"/api/login", url.Values{"user": {"juen"}, "pass": {"wrong"}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?err=locked" {
		t.Fatalf("locked form login %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestTwoStepSessionsSignOut(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plain := newAuthGate("juen", "secret", nil, key)
	a, _ := parseTwoStepSecret(testSecret)
	b, _ := parseTwoStepSecret("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	withA := newAuthGate("juen", "secret", a, key)
	withB := newAuthGate("juen", "secret", b, key)
	exp := time.Now().Add(time.Hour).Unix()
	// Deploying without a secret keeps today's sessions.
	if !newAuthGate("juen", "secret", nil, key).validSession(plain.sign(exp)) {
		t.Fatal("password-only session lost")
	}
	if withA.validSession(plain.sign(exp)) {
		t.Fatal("turning two-step on kept old sessions")
	}
	if withB.validSession(withA.sign(exp)) {
		t.Fatal("changing the secret kept old sessions")
	}
	if !newAuthGate("juen", "secret", a, key).validSession(withA.sign(exp)) {
		t.Fatal("restart with the same secret lost sessions")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.RemoteAddr = "172.18.0.1:40000"
	if got := clientIP(r); got != "172.18.0.1" {
		t.Fatalf("no header: %s", got)
	}
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("header: %s", got)
	}
	r.Header.Set("X-Real-IP", "not-an-ip")
	if got := clientIP(r); got != "172.18.0.1" {
		t.Fatalf("bad header: %s", got)
	}
}

func TestTwoStepSetupConfirmsBeforePrinting(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	code := codeAt(t, now)

	var out, msg bytes.Buffer
	if rc := runTwoStepSetup(strings.NewReader("000000\n"+code+"\n"), &out, &msg, testSecret, "juen", clock); rc != 0 {
		t.Fatalf("rc %d: %s", rc, msg.String())
	}
	if out.String() != testSecret+"\n" {
		t.Fatalf("stdout must hold only the secret, got %q", out.String())
	}
	if !strings.Contains(msg.String(), "GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ") || !strings.Contains(msg.String(), "▀") || !strings.Contains(msg.String(), "动态码不对") {
		t.Fatalf("messages: %s", msg.String())
	}

	for _, input := range []string{"111111\n222222\n333333\n" + code + "\n", ""} {
		out.Reset()
		msg.Reset()
		if rc := runTwoStepSetup(strings.NewReader(input), &out, &msg, testSecret, "juen", clock); rc != 1 || out.Len() != 0 {
			t.Fatalf("input %q: rc %d stdout %q", input, rc, out.String())
		}
		if !strings.Contains(msg.String(), "没有任何改动") {
			t.Fatalf("missing no-change note: %s", msg.String())
		}
	}
}

func TestOtpauthURI(t *testing.T) {
	want := "otpauth://totp/bijin:juen?algorithm=SHA1&digits=6&issuer=bijin&period=30&secret=" + testSecret
	if got := otpauthURI(testSecret, "juen"); got != want {
		t.Fatalf("got %s", got)
	}
}

// The half-block drawing must reproduce every QR module, quiet zone white.
func TestTerminalQRDrawsEveryModule(t *testing.T) {
	c, err := qr.Encode(otpauthURI(testSecret, "juen"), qr.M)
	if err != nil {
		t.Fatal(err)
	}
	cell := regexp.MustCompile(`\x1b\[(\d+);(\d+)m▀`)
	lines := strings.Split(strings.TrimSuffix(terminalQR(c), "\n"), "\n")
	const quiet = 2
	if len(lines) != (c.Size+2*quiet+1)/2 {
		t.Fatalf("%d lines for size %d", len(lines), c.Size)
	}
	for row, line := range lines {
		cells := cell.FindAllStringSubmatch(line, -1)
		if len(cells) != c.Size+2*quiet {
			t.Fatalf("row %d has %d cells", row, len(cells))
		}
		for col, m := range cells {
			x, y := col-quiet, row*2-quiet
			for i, dark := range []bool{m[1] == "30", m[2] == "40"} {
				yy := y + i
				want := x >= 0 && yy >= 0 && x < c.Size && yy < c.Size && c.Black(x, yy)
				if dark != want {
					t.Fatalf("module %d,%d dark %v want %v", x, yy, dark, want)
				}
			}
		}
	}
}

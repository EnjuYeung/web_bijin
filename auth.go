package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "bijin"
	sessionTTL    = 30 * 24 * time.Hour

	// maxLoginFails wrong logins in a row from one IP lock it out for
	// loginLockout; the count also resets after loginLockout without a miss.
	maxLoginFails = 5
	loginLockout  = 15 * time.Minute
)

type authGate struct {
	user    string
	pass    string
	twoStep []byte // empty: password only
	key     []byte
	now     func() time.Time

	mu       sync.Mutex
	lastStep int64 // newest accepted code step; each code works once
	fails    map[string]loginFails
}

type loginFails struct {
	n    int
	last time.Time
}

type loginResult int

const (
	loginOK loginResult = iota
	loginWrong
	loginLocked
)

func newAuthGate(user, pass string, twoStep, key []byte) *authGate {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("bijin-session-v2\x00" + user + "\x00" + pass))
	// Turning two-step on or changing its secret signs every device out.
	if len(twoStep) > 0 {
		mac.Write([]byte("\x00two-step\x00"))
		mac.Write(twoStep)
	}
	return &authGate{user: user, pass: pass, twoStep: twoStep, key: mac.Sum(nil), now: time.Now, fails: map[string]loginFails{}}
}

func loadSessionKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "session.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b[:32], nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func (g *authGate) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return g.validSession(c.Value)
}

func (g *authGate) sign(exp int64) string {
	msg := g.user + "|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(msg))
	return "v2." + strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (g *authGate) validSession(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "v2" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(raw), []byte(g.sign(exp)))
}

func (g *authGate) check(user, pass string) bool {
	uh := sha256.Sum256([]byte(user))
	ph := sha256.Sum256([]byte(pass))
	wantU := sha256.Sum256([]byte(g.user))
	wantP := sha256.Sum256([]byte(g.pass))
	okU := subtle.ConstantTimeCompare(uh[:], wantU[:]) == 1
	okP := subtle.ConstantTimeCompare(ph[:], wantP[:]) == 1
	return okU && okP
}

// attempt checks one login from ip. A locked IP is refused before its
// password is looked at, and a wrong password, code or reused code all count
// as one miss.
func (g *authGate) attempt(ip, user, pass, code string) loginResult {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.fails[ip]
	if now.Sub(f.last) >= loginLockout {
		f = loginFails{}
	}
	if f.n >= maxLoginFails {
		return loginLocked
	}
	ok := g.check(user, pass)
	step := int64(0)
	if len(g.twoStep) > 0 {
		var codeOK bool
		step, codeOK = codeStepAt(g.twoStep, code, now)
		ok = ok && codeOK && step > g.lastStep
	}
	if !ok {
		g.fails[ip] = loginFails{n: f.n + 1, last: now}
		for k, v := range g.fails {
			if now.Sub(v.last) >= loginLockout {
				delete(g.fails, k)
			}
		}
		return loginWrong
	}
	delete(g.fails, ip)
	if step > 0 {
		g.lastStep = step
	}
	return loginOK
}

// lockedFor is how long ip must still wait, for the Retry-After header.
func (g *authGate) lockedFor(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return max(0, loginLockout-g.now().Sub(g.fails[ip].last))
}

// clientIP is the visitor's address as OpenResty puts it in X-Real-IP. The
// port is published on 127.0.0.1 only and OpenResty overwrites the header,
// so visitors cannot choose it; direct requests use the connection address.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(ip) != nil {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (g *authGate) setSession(w http.ResponseWriter) {
	exp := time.Now().Add(sessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    g.sign(exp),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (g *authGate) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.signedIn(r) {
			next.ServeHTTP(w, r)
			return
		}
		if wantsAPIError(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

func wantsAPIError(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/thumb/") || strings.HasPrefix(p, "/original/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func safeNext(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	if strings.HasPrefix(v, "/login") {
		return "/"
	}
	return v
}

func readLogin(r *http.Request) (user, pass, code string) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body struct {
			User string `json:"user"`
			Pass string `json:"pass"`
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body.User, body.Pass, body.Code
	}
	if strings.HasPrefix(ct, "multipart/form-data") {
		_ = r.ParseMultipartForm(1 << 20)
	} else {
		_ = r.ParseForm()
	}
	return r.FormValue("user"), r.FormValue("pass"), r.FormValue("code")
}

// handleLoginOptions tells the login page whether to ask for a code.
func (g *authGate) handleLoginOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"twoStep": len(g.twoStep) > 0})
}

func (g *authGate) handleLogin(w http.ResponseWriter, r *http.Request) {
	asJSON := strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || strings.Contains(r.Header.Get("Accept"), "application/json")
	user, pass, code := readLogin(r)
	ip := clientIP(r)
	switch g.attempt(ip, user, pass, code) {
	case loginLocked:
		w.Header().Set("Retry-After", strconv.Itoa(int(g.lockedFor(ip).Seconds())+1))
		if asJSON {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "locked"})
			return
		}
		http.Redirect(w, r, "/login?err=locked", http.StatusFound)
		return
	case loginWrong:
		if asJSON {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		http.Redirect(w, r, "/login?err=1", http.StatusFound)
		return
	}
	g.setSession(w)
	_ = r.ParseForm()
	next := safeNext(r.FormValue("next"))
	if q := r.URL.Query().Get("next"); q != "" {
		next = safeNext(q)
	}
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": next})
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

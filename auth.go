package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "bijin"
	sessionTTL    = 30 * 24 * time.Hour
)

type authGate struct {
	user string
	pass string
	key  []byte
}

func newAuthGate(user, pass string, key []byte) *authGate {
	return &authGate{user: user, pass: pass, key: key}
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
	return "v1." + strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (g *authGate) validSession(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "v1" {
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

func readLogin(r *http.Request) (user, pass string) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body.User, body.Pass
	}
	if strings.HasPrefix(ct, "multipart/form-data") {
		_ = r.ParseMultipartForm(1 << 20)
	} else {
		_ = r.ParseForm()
	}
	return r.FormValue("user"), r.FormValue("pass")
}

func (g *authGate) handleLogin(w http.ResponseWriter, r *http.Request) {
	user, pass := readLogin(r)
	if !g.check(user, pass) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || strings.Contains(r.Header.Get("Accept"), "application/json") {
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
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": next})
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

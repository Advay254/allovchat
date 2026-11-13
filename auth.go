package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randSlug(n int) string {
	const al = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = al[int(b[i])%len(al)]
	}
	return string(b)
}

func hashPass(p string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	k, _ := pbkdf2.Key(sha256.New, p, salt, 200000, 32)
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(k)
}

func checkPass(p, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 2 {
		return false
	}
	salt, e1 := hex.DecodeString(parts[0])
	want, e2 := hex.DecodeString(parts[1])
	if e1 != nil || e2 != nil {
		return false
	}
	k, err := pbkdf2.Key(sha256.New, p, salt, 200000, 32)
	return err == nil && subtle.ConstantTimeCompare(k, want) == 1
}

func (a *App) sign(v string) string {
	m := hmac.New(sha256.New, []byte(a.cfg.Secret))
	m.Write([]byte(v))
	return v + "." + hex.EncodeToString(m.Sum(nil))
}

func (a *App) unsign(s string) (string, bool) {
	i := strings.LastIndex(s, ".")
	if i < 0 || !hmac.Equal([]byte(a.sign(s[:i])), []byte(s)) {
		return "", false
	}
	return s[:i], true
}

func (a *App) setCookie(w http.ResponseWriter, name, val string, maxAge int, strict bool) {
	ss := http.SameSiteLaxMode
	if strict {
		ss = http.SameSiteStrictMode
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: a.cfg.Secure, SameSite: ss})
}

func (a *App) startOwner(w http.ResponseWriter, id int64) {
	exp := time.Now().Add(30 * 24 * time.Hour).Unix()
	a.setCookie(w, "os", a.sign("o:"+strconv.FormatInt(id, 10)+":"+strconv.FormatInt(exp, 10)), 30*86400, false)
}

// ownerID returns the signed-in owner's id, or 0.
func (a *App) ownerID(r *http.Request) int64 {
	c, err := r.Cookie("os")
	if err != nil {
		return 0
	}
	v, ok := a.unsign(c.Value)
	if !ok {
		return 0
	}
	p := strings.Split(v, ":")
	if len(p) != 3 || p[0] != "o" {
		return 0
	}
	id, _ := strconv.ParseInt(p[1], 10, 64)
	exp, _ := strconv.ParseInt(p[2], 10, 64)
	var n int
	if time.Now().Unix() > exp || a.db.QueryRow(r.Context(), `SELECT 1 FROM owners WHERE id=$1 AND NOT disabled`, id).Scan(&n) != nil {
		return 0
	}
	return id
}

func (a *App) adminOK(r *http.Request) bool {
	c, err := r.Cookie("as")
	if err != nil {
		return false
	}
	v, ok := a.unsign(c.Value)
	if !ok || !strings.HasPrefix(v, "a:") {
		return false
	}
	exp, _ := strconv.ParseInt(v[2:], 10, 64)
	return time.Now().Unix() < exp
}

func same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(sha256hex(a)), []byte(sha256hex(b))) == 1
}

// Limiter is a small in-memory fixed-window rate limiter.
type Limiter struct {
	mu sync.Mutex
	m  map[string]*win
}

type win struct {
	n     int
	until time.Time
}

func newLimiter() *Limiter { return &Limiter{m: map[string]*win{}} }

func (l *Limiter) allow(key string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.m) > 20000 {
		for k, w := range l.m {
			if now.After(w.until) {
				delete(l.m, k)
			}
		}
	}
	w := l.m[key]
	if w == nil || now.After(w.until) {
		l.m[key] = &win{1, now.Add(per)}
		return true
	}
	w.n++
	return w.n <= max
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

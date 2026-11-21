package main

import (
	"context"
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

// proxyHops is how many reverse proxies sit in front of the app (TRUSTED_PROXY_HOPS).
// The client IP is taken from that many entries from the right of X-Forwarded-For.
var proxyHops = 1

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

const pbkdf2Iter = 310000

func hashPass(p string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	k, _ := pbkdf2.Key(sha256.New, p, salt, pbkdf2Iter, 32)
	return "v2$" + strconv.Itoa(pbkdf2Iter) + "$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(k)
}

// dummyHash is checked when a username doesn't exist, so response time doesn't reveal which usernames are real.
var dummyHash = hashPass("not-a-real-password")

func checkPass(p, stored string) bool {
	parts := strings.Split(stored, "$")
	iter := 200000 // hashes created before v2 had no iteration count
	if len(parts) == 4 && parts[0] == "v2" {
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return false
		}
		iter, parts = n, parts[2:]
	}
	if len(parts) != 2 || iter < 1000 || iter > 5000000 {
		return false
	}
	salt, e1 := hex.DecodeString(parts[0])
	want, e2 := hex.DecodeString(parts[1])
	if e1 != nil || e2 != nil {
		return false
	}
	k, err := pbkdf2.Key(sha256.New, p, salt, iter, 32)
	return err == nil && subtle.ConstantTimeCompare(k, want) == 1
}

func same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(sha256hex(a)), []byte(sha256hex(b))) == 1
}

func (a *App) setCookie(w http.ResponseWriter, name, val string, maxAge int, strict bool) {
	ss := http.SameSiteLaxMode
	if strict {
		ss = http.SameSiteStrictMode
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: a.cfg.Secure, SameSite: ss})
}

// ---------- server-side sessions: revocable, and only a hash is stored ----------

func (a *App) newSession(ctx context.Context, kind string, owner int64, ttl time.Duration) (string, error) {
	tok := randHex(24)
	var oid any
	if owner > 0 {
		oid = owner
	}
	_, err := a.db.Exec(ctx, `INSERT INTO sessions(id,kind,owner_id,expires_at) VALUES($1,$2,$3,now()+make_interval(secs => $4::float8))`, sha256hex(tok), kind, oid, ttl.Seconds())
	return tok, err
}

func (a *App) startOwner(w http.ResponseWriter, r *http.Request, id int64) error {
	tok, err := a.newSession(r.Context(), "owner", id, 30*24*time.Hour)
	if err != nil {
		return err
	}
	a.setCookie(w, "os", tok, 30*86400, false)
	return nil
}

func (a *App) endSession(r *http.Request, cookie string) {
	if c, err := r.Cookie(cookie); err == nil && len(c.Value) == 48 {
		h := sha256hex(c.Value)
		a.cache.del("s:" + h)
		a.exec(r.Context(), "end session", `DELETE FROM sessions WHERE id=$1`, h)
	}
}

// ownerID returns the signed-in account's id, or 0. This runs on almost every request, so a successful
// lookup is kept for 20 seconds. Logging out, disabling or deleting an account clears the cache straight away.
func (a *App) ownerID(r *http.Request) int64 {
	// A browser sends its cookie (session kind "owner"); the Android app sends a bearer token (kind "app").
	tok, kind := "", "owner"
	if c, err := r.Cookie("os"); err == nil && len(c.Value) == 48 {
		tok = c.Value
	} else if t := bearerToken(r); t != "" {
		tok, kind = t, "app"
	}
	if tok == "" {
		return 0
	}
	key := "s:" + sha256hex(tok)
	if v, ok := a.cache.get(key); ok {
		return v.(int64)
	}
	var id int64
	if a.db.QueryRow(r.Context(), `SELECT s.owner_id FROM sessions s JOIN owners o ON o.id=s.owner_id WHERE s.id=$1 AND s.kind=$2 AND s.expires_at>now() AND NOT o.disabled`, sha256hex(tok), kind).Scan(&id) != nil {
		return 0
	}
	a.cache.set(key, id, 20*time.Second)
	return id
}

// adminToken returns the raw admin session token if the request has a valid admin session, else "".
func (a *App) adminToken(r *http.Request) string {
	c, err := r.Cookie("as")
	if err != nil || len(c.Value) != 48 {
		return ""
	}
	var n int
	if a.db.QueryRow(r.Context(), `SELECT 1 FROM sessions WHERE id=$1 AND kind='admin' AND expires_at>now()`, sha256hex(c.Value)).Scan(&n) != nil {
		return ""
	}
	return c.Value
}

// csrf derives the anti-forgery token for an admin session.
func (a *App) csrf(token string) string {
	m := hmac.New(sha256.New, []byte(a.cfg.Secret))
	m.Write([]byte("csrf|" + token))
	return hex.EncodeToString(m.Sum(nil))
}

// ---------- rate limiting ----------

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

// FailLimiter counts failed logins per key over a 15 minute window and reports how long a locked key must wait.
type FailLimiter struct {
	mu sync.Mutex
	m  map[string]*failRec
}

type failRec struct {
	n     int
	start time.Time
}

const failWindow = 15 * time.Minute

func newFails() *FailLimiter { return &FailLimiter{m: map[string]*failRec{}} }

// wait returns how long until key may try again, or 0 if it isn't locked.
func (f *FailLimiter) wait(key string, limit int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.m[key]
	if r == nil {
		return 0
	}
	left := failWindow - time.Since(r.start)
	if left <= 0 {
		delete(f.m, key)
		return 0
	}
	if r.n >= limit {
		return left
	}
	return 0
}

// count returns the failures recorded for key in the current window.
func (f *FailLimiter) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.m[key]; r != nil && time.Since(r.start) <= failWindow {
		return r.n
	}
	return 0
}

func (f *FailLimiter) fail(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if len(f.m) > 50000 {
		for k, r := range f.m {
			if now.Sub(r.start) > failWindow {
				delete(f.m, k)
			}
		}
	}
	for _, k := range keys {
		if r := f.m[k]; r == nil || now.Sub(r.start) > failWindow {
			f.m[k] = &failRec{1, now}
		} else {
			r.n++
		}
	}
}

func (f *FailLimiter) reset(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.m, k)
	}
}

// loginWait locks a sign-in after 5 failures from the same account and IP, or 20 from one IP.
// The per-account counter (acct) never locks anyone out on its own, so a stranger can't lock a real person out
// from their own devices. Callers use it to ask for a human check instead.
func (a *App) loginWait(user, ip string) (pair, acct, byIP string, wait time.Duration) {
	pair, acct, byIP = "p:"+user+"|"+ip, "a:"+user, "i:"+ip
	wait = max(a.fails.wait(pair, 5), a.fails.wait(byIP, 20))
	return
}

func clientIP(r *http.Request) string {
	if proxyHops > 0 {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			parts := strings.Split(f, ",")
			i := len(parts) - proxyHops
			if i < 0 {
				i = 0
			}
			if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil {
				return ip.String()
			}
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Support for the Android app.
//
// The app signs in with a username and password and gets a bearer token (a revocable server-side session of kind "app").
// It then keeps one event stream open to the server, like Gotify does, and shows a notification when something
// arrives. Events are as generic as web pushes: a kind and an opaque tag, never names or message text.

const (
	appTTL           = 180 * 24 * time.Hour
	appMaxStreams    = 5
	defaultAppOrigin = "https://appassets.androidplatform.net"
)

// Origins allowed to call the API with a bearer token from inside the app's WebView. APP_ORIGINS adds more, comma separated.
var appOrigins = func() map[string]bool {
	m := map[string]bool{defaultAppOrigin: true}
	for _, o := range strings.Split(os.Getenv("APP_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); strings.HasPrefix(o, "https://") {
			m[o] = true
		}
	}
	return m
}()

func appOriginOK(o string) bool { return appOrigins[o] }

func hasBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// appStreams fans events out to the apps that are connected for an account.
type appStreams struct {
	mu   sync.Mutex
	subs map[int64]map[chan string]bool
}

var appHub = &appStreams{subs: map[int64]map[chan string]bool{}}

func (h *appStreams) sub(owner int64) (chan string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs[owner]) >= appMaxStreams {
		return nil, false
	}
	c := make(chan string, 8)
	if h.subs[owner] == nil {
		h.subs[owner] = map[chan string]bool{}
	}
	h.subs[owner][c] = true
	return c, true
}

func (h *appStreams) unsub(owner int64, c chan string) {
	h.mu.Lock()
	delete(h.subs[owner], c)
	if len(h.subs[owner]) == 0 {
		delete(h.subs, owner)
	}
	h.mu.Unlock()
}

func (h *appStreams) pub(owner int64, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs[owner] {
		select {
		case c <- msg:
		default:
		}
	}
}

// capture lets the app login reuse the website's login handler, including its lockout and human-check rules,
// then swap the browser cookie it produces for an app token.
type capture struct {
	h    http.Header
	code int
	body bytes.Buffer
}

func (c *capture) Header() http.Header         { return c.h }
func (c *capture) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *capture) WriteHeader(code int)        { c.code = code }

func (a *App) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/app/login", a.appLogin)
	mux.HandleFunc("POST /api/app/logout", a.appLogout)
	mux.HandleFunc("GET /api/app/events", a.appEvents)
}

func (a *App) appLogin(w http.ResponseWriter, r *http.Request) {
	cw := &capture{h: http.Header{}, code: http.StatusOK}
	a.login(cw, r)
	if cw.code != http.StatusOK {
		// Wrong password, lockout (429 with Retry-After), human check: pass it on unchanged.
		for _, k := range []string{"Content-Type", "Cache-Control", "Retry-After"} {
			if v := cw.h.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(cw.code)
		w.Write(cw.body.Bytes())
		return
	}
	var cookie string
	for _, c := range (&http.Response{Header: cw.h}).Cookies() {
		if c.Name == "os" {
			cookie = c.Value
		}
	}
	var oid int64
	if len(cookie) != 48 || a.db.QueryRow(r.Context(), `DELETE FROM sessions WHERE id=$1 AND kind='owner' RETURNING owner_id`, sha256hex(cookie)).Scan(&oid) != nil {
		a.fail500(w, "app login session", fmt.Errorf("no session to convert"))
		return
	}
	tok, err := a.newSession(r.Context(), "app", oid, appTTL)
	if err != nil {
		a.fail500(w, "app login token", err)
		return
	}
	jsonOut(w, 200, map[string]any{"token": tok})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") && len(h) == 7+48 {
		return h[7:]
	}
	return ""
}

func (a *App) appLogout(w http.ResponseWriter, r *http.Request) {
	if t := bearerToken(r); t != "" {
		h := sha256hex(t)
		a.cache.del("s:" + h)
		a.exec(r.Context(), "app logout", `DELETE FROM sessions WHERE id=$1 AND kind='app'`, h)
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// appEvents is the app's live connection. It sends a generic event whenever something needs attention.
func (a *App) appEvents(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "Live updates aren't available here")
		return
	}
	ch, ok := appHub.sub(oid)
	if !ok {
		fail(w, 429, "Too many connected devices")
		return
	}
	defer appHub.unsub(oid, ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 5000\n\n")
	fl.Flush()
	tk := time.NewTicker(25 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", m)
			fl.Flush()
		case <-tk.C:
			if a.ownerID(r) != oid { // signed out or disabled: end the stream so the app notices
				return
			}
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// appNotify tells connected apps about an event. It follows the same rules as web push: the owner's quiet hours are
// respected, and the caller has already skipped people who have this chat open.
func (a *App) appNotify(ctx context.Context, tid int64, to, kind, tag string) {
	var owner *int64
	var qs, qe, tz int
	err := a.db.QueryRow(ctx, `SELECT CASE WHEN $2::text='o' THEN l.owner_id ELSE t.account_id END, l.quiet_start, l.quiet_end, l.tz
		FROM threads t JOIN links l ON l.id=t.link_id JOIN owners ow ON ow.id=l.owner_id AND NOT ow.disabled
		WHERE t.id=$1 AND t.expires_at>now()`, tid, to).Scan(&owner, &qs, &qe, &tz)
	if err != nil || owner == nil {
		return
	}
	if to == "o" && pushQuiet(qs, qe, tz) {
		return
	}
	appHub.pub(*owner, fmt.Sprintf(`{"k":%q,"g":%q}`, kind, tag))
}

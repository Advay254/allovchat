package main

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
)

// Web Push.
//
// What leaves the server is always generic: a kind ("request", "message", ...), a page to open and an opaque tag.
// No message text, names or link labels are ever put in a push, and the browser's push service only ever sees ciphertext.

const (
	maxSubsPerOwner  = 10 // devices per account
	maxSubsPerThread = 3  // devices per guest chat
	pushTTL          = 12 * 3600
	pushMaxFails     = 5
)

var (
	// A burst of events for one chat becomes one push, sent pushDelay after the first event.
	// After a push, the next one for the same chat waits until pushGap has passed.
	pushDelay = 4 * time.Second
	pushGap   = 20 * time.Second

	pushKinds = map[string]bool{"request": true, "message": true, "accepted": true, "reveal": true}
)

// Only the push services of the major browsers are accepted by default. Extra hosts (for example a self-hosted
// UnifiedPush server) can be added with PUSH_ALLOWED_HOSTS: exact names, or a leading dot for a whole domain.
var defaultPushHosts = []string{"fcm.googleapis.com", ".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com"}

type slot struct {
	pending bool
	last    time.Time
}

type Pusher struct {
	on       bool
	pub      string
	priv     string
	subject  string
	hosts    []string
	client   *http.Client
	allow    func(host string) bool // replaced in tests
	run      func(tid int64, to, kind string)
	sem      chan struct{}
	mu       sync.Mutex
	slots    map[string]*slot
	tagKey   []byte
	attempts int // test hook: sends attempted
}

type pushSub struct {
	ID                int64
	Endpoint, P, Auth string
	Guest             bool
	Slug              string
}

func (p *Pusher) hostOK(host string) bool {
	if p.allow != nil {
		return p.allow(host)
	}
	host = strings.ToLower(host)
	for _, h := range p.hosts {
		if h == host || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

// endpointOK checks a push endpoint the browser gave us. The server will POST to it, so it must be a known push
// service over https, never an address on our own network.
func (p *Pusher) endpointOK(ep string) bool {
	if len(ep) > 700 {
		return false
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if port := u.Port(); port != "" && port != "443" && p.allow == nil {
		return false
	}
	return p.hostOK(u.Hostname())
}

// publicOnly runs after DNS, right before connecting, so a push endpoint can't be pointed at a private address.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return errors.New("push: destination not allowed")
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64 { // 100.64.0.0/10, carrier-grade NAT
		return errors.New("push: destination not allowed")
	}
	return nil
}

func newPushClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, Control: publicOnly}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       60 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// initPush loads or creates the VAPID key pair. Keys from VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY win; otherwise a pair is
// generated on first boot and kept in the database. If anything is wrong, push is switched off and the rest of the app runs.
func (a *App) initPush(ctx context.Context) *Pusher {
	p := &Pusher{hosts: append([]string{}, defaultPushHosts...), client: newPushClient(), sem: make(chan struct{}, 8), slots: map[string]*slot{}}
	p.run = a.deliver
	for _, h := range strings.Split(os.Getenv("PUSH_ALLOWED_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			p.hosts = append(p.hosts, h)
		}
	}
	p.subject = "https://github.com/Advay254/allovchat"
	if strings.HasPrefix(a.cfg.BaseURL, "https://") {
		p.subject = a.cfg.BaseURL
	}
	if v := strings.TrimSpace(os.Getenv("VAPID_SUBJECT")); strings.HasPrefix(v, "mailto:") || strings.HasPrefix(v, "https://") {
		p.subject = v
	}
	p.tagKey = []byte("push-tag|" + a.cfg.Secret)

	pub, priv := strings.TrimSpace(os.Getenv("VAPID_PUBLIC_KEY")), strings.TrimSpace(os.Getenv("VAPID_PRIVATE_KEY"))
	switch {
	case (pub == "") != (priv == ""):
		log.Printf("push is off: set both VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY, or neither")
		return p
	case pub == "":
		var err error
		if pub, priv, err = a.vapidFromDB(ctx); err != nil {
			logErr("push keys", err)
			return p
		}
	}
	if !vapidPair(pub, priv) {
		log.Printf("push is off: the VAPID public and private keys are not a matching pair")
		return p
	}
	p.pub, p.priv, p.on = pub, priv, true
	return p
}

func (a *App) vapidFromDB(ctx context.Context) (pub, priv string, err error) {
	err = a.db.QueryRow(ctx, `SELECT pub, priv FROM push_keys WHERE id=1`).Scan(&pub, &priv)
	if errors.Is(err, pgx.ErrNoRows) {
		// Two instances starting together both try this; ON CONFLICT keeps the first pair, and both read it back.
		if priv, pub, err = webpush.GenerateVAPIDKeys(); err != nil {
			return
		}
		if _, err = a.db.Exec(ctx, `INSERT INTO push_keys(id,pub,priv) VALUES(1,$1,$2) ON CONFLICT (id) DO NOTHING`, pub, priv); err != nil {
			return
		}
		err = a.db.QueryRow(ctx, `SELECT pub, priv FROM push_keys WHERE id=1`).Scan(&pub, &priv)
	}
	return
}

// vapidPair reports whether pub is the public half of priv (both base64url without padding).
func vapidPair(pub, priv string) bool {
	pb, e1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(pub, "="))
	kb, e2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(priv, "="))
	if e1 != nil || e2 != nil || len(pb) != 65 || len(kb) != 32 {
		return false
	}
	k, err := ecdh.P256().NewPrivateKey(kb)
	return err == nil && hmac.Equal(k.PublicKey().Bytes(), pb)
}

// ---------- scheduling ----------

// notify queues a push for the person on side `to` ("o" owner or "v" guest) of chat tid. It never blocks the request.
func (a *App) notify(tid int64, to, kind string) {
	if a.push == nil || !pushKinds[kind] || (to != "o" && to != "v") {
		return
	}
	a.push.schedule(tid, to, kind)
}

func (p *Pusher) schedule(tid int64, to, kind string) {
	key := strconv.FormatInt(tid, 10) + "|" + to + "|" + kind
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.slots[key]
	if s == nil {
		if len(p.slots) > 20000 {
			for k, v := range p.slots {
				if !v.pending && time.Since(v.last) > 10*time.Minute {
					delete(p.slots, k)
				}
			}
		}
		s = &slot{}
		p.slots[key] = s
	}
	if s.pending {
		return // already queued: this event rides along with it
	}
	delay := pushDelay
	if w := pushGap - time.Since(s.last); w > delay {
		delay = w
	}
	s.pending = true
	time.AfterFunc(delay, func() {
		p.mu.Lock()
		s.pending, s.last = false, time.Now()
		p.mu.Unlock()
		p.run(tid, to, kind)
	})
}

// tag is an opaque label for one chat and one recipient. It is used as the push Topic and the notification tag,
// so a newer alert replaces an older one. It is a keyed hash, so it reveals nothing about the chat.
func (p *Pusher) tag(tid int64, to string) string {
	m := hmac.New(sha256.New, p.tagKey)
	fmt.Fprintf(m, "%d|%s", tid, to)
	return hex.EncodeToString(m.Sum(nil))[:22]
}

// pushQuiet reports whether the owner's quiet hours cover this moment. Unlike the auto-reply check,
// "off" (-1) means never quiet.
func pushQuiet(start, end, tz int) bool {
	if start < 0 || end < 0 {
		return false
	}
	return inQuiet(start, end, tz)
}

// ---------- delivery ----------

func (a *App) deliver(tid int64, to, kind string) {
	p := a.push
	p.sem <- struct{}{}
	defer func() { <-p.sem }()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC push deliver: %v", r)
		}
	}()
	// Someone with this chat open already sees the update live, so no alert is needed.
	if a.hub.conns(tid, to) > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Apps that are connected to the live event stream hear about it first, and this works even when web push is off.
	a.appNotify(ctx, tid, to, kind, p.tag(tid, to))
	if !p.on {
		return
	}
	rows, err := a.db.Query(ctx, `SELECT s.id, s.endpoint, s.p256dh, s.auth, (s.thread_id IS NOT NULL), l.slug, l.quiet_start, l.quiet_end, l.tz
		FROM threads t
		JOIN links l ON l.id=t.link_id
		JOIN owners ow ON ow.id=l.owner_id AND NOT ow.disabled
		JOIN push_subs s ON (($2::text='o' AND s.owner_id=l.owner_id) OR ($2::text='v' AND (s.thread_id=t.id OR (t.account_id IS NOT NULL AND s.owner_id=t.account_id))))
		WHERE t.id=$1 AND t.expires_at>now()`, tid, to)
	if err != nil {
		logErr("push recipients", err)
		return
	}
	var subs []pushSub
	quiet := false
	for rows.Next() {
		var s pushSub
		var qs, qe, tz int
		if rows.Scan(&s.ID, &s.Endpoint, &s.P, &s.Auth, &s.Guest, &s.Slug, &qs, &qe, &tz) == nil {
			subs = append(subs, s)
			quiet = to == "o" && pushQuiet(qs, qe, tz)
		}
	}
	rows.Close()
	if len(subs) == 0 || quiet {
		return
	}
	tag := p.tag(tid, to)
	for _, s := range subs {
		u := "/app"
		if s.Guest {
			u = "/l/" + s.Slug
		}
		body, _ := json.Marshal(map[string]string{"k": kind, "u": u, "g": tag})
		a.sendOne(ctx, s, body, tag)
	}
}

func (a *App) sendOne(ctx context.Context, s pushSub, body []byte, topic string) {
	p := a.push
	p.mu.Lock()
	p.attempts++
	p.mu.Unlock()
	if !p.endpointOK(s.Endpoint) {
		a.exec(ctx, "push drop host", `DELETE FROM push_subs WHERE id=$1`, s.ID)
		return
	}
	resp, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{Endpoint: s.Endpoint, Keys: webpush.Keys{Auth: s.Auth, P256dh: s.P}}, &webpush.Options{
		HTTPClient: p.client, Subscriber: p.subject, VAPIDPublicKey: p.pub, VAPIDPrivateKey: p.priv,
		TTL: pushTTL, Urgency: webpush.UrgencyNormal, Topic: topic,
	})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		resp.Body.Close()
	}
	switch {
	case err == nil && code >= 200 && code < 300:
		a.exec(ctx, "push ok", `UPDATE push_subs SET last_ok=now(), fails=0 WHERE id=$1`, s.ID)
	case code == http.StatusNotFound || code == http.StatusGone:
		// The browser removed this subscription. It is gone for good, for every chat that used it.
		a.exec(ctx, "push gone", `DELETE FROM push_subs WHERE endpoint=$1`, s.Endpoint)
	default:
		if err != nil {
			log.Printf("push failed id=%d: %v", s.ID, err)
		} else {
			log.Printf("push failed id=%d: status %d", s.ID, code)
		}
		a.exec(ctx, "push fail", `UPDATE push_subs SET fails=fails+1 WHERE id=$1`, s.ID)
		a.exec(ctx, "push prune", `DELETE FROM push_subs WHERE id=$1 AND fails>=$2`, s.ID, pushMaxFails)
	}
}

// ---------- HTTP ----------

func (a *App) pushRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sw.js", a.serviceWorker)
	mux.HandleFunc("GET /offline", a.page("offline.html"))
	mux.HandleFunc("GET /api/push/key", a.pushKey)
	mux.HandleFunc("POST /api/push/subscribe", a.pushSubscribeOwner)
	mux.HandleFunc("POST /api/push/unsubscribe", a.pushUnsubscribeOwner)
	mux.HandleFunc("POST /api/l/{slug}/push/subscribe", a.pushSubscribeGuest)
	mux.HandleFunc("POST /api/l/{slug}/push/unsubscribe", a.pushUnsubscribeGuest)
	a.appRoutes(mux)
}

// serviceWorker serves /sw.js from the root, so it covers the whole site. It is never cached, and it carries the build
// fingerprint, so every deploy gives browsers a new worker file and they update it.
func (a *App) serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	site, _ := json.Marshal(a.set.get("site_name"))
	out := strings.NewReplacer("__BUILD__", buildID, "__SITE__", string(site)).Replace(string(b))
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Service-Worker-Allowed", "/")
	w.Write([]byte(out))
}

// pushKey returns the public VAPID key. It is public by design.
func (a *App) pushKey(w http.ResponseWriter, r *http.Request) {
	if a.push == nil || !a.push.on {
		fail(w, http.StatusServiceUnavailable, "Notifications aren't available on this server")
		return
	}
	jsonCached(w, map[string]string{"key": a.push.pub}, 300)
}

type subIn struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// readSub decodes and checks a subscription. The endpoint must belong to a known push service, and the keys must be
// real keys, so nobody can store junk or point the server at an arbitrary address.
func (a *App) readSub(w http.ResponseWriter, r *http.Request) (subIn, bool) {
	var in subIn
	if !readJSON(w, r, &in) {
		return in, false
	}
	if a.push == nil || !a.push.on {
		fail(w, http.StatusServiceUnavailable, "Notifications aren't available on this server")
		return in, false
	}
	auth, err := base64.RawURLEncoding.DecodeString(in.Auth)
	if !a.push.endpointOK(in.Endpoint) || !validPub(in.P256dh) || err != nil || len(auth) != 16 {
		fail(w, http.StatusBadRequest, "That notification setup didn't look right")
		return in, false
	}
	return in, true
}

func (a *App) pushSubscribeOwner(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	if !a.rl.allow("psub:"+strconv.FormatInt(oid, 10), 30, time.Hour) || !a.rl.allow("psub-ip:"+clientIP(r), 60, time.Hour) {
		fail(w, http.StatusTooManyRequests, "Too many attempts. Try again later")
		return
	}
	in, ok := a.readSub(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "push sub begin", err)
		return
	}
	defer tx.Rollback(ctx)
	// A browser belongs to whoever is signed in on it now, so a shared device never alerts the previous person's account.
	if _, err = tx.Exec(ctx, `DELETE FROM push_subs WHERE endpoint=$1 AND owner_id IS NOT NULL AND owner_id<>$2`, in.Endpoint, oid); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO push_subs(endpoint,p256dh,auth,owner_id) VALUES($1,$2,$3,$4)
			ON CONFLICT (endpoint, owner_id) WHERE owner_id IS NOT NULL DO UPDATE SET p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth, fails=0`, in.Endpoint, in.P256dh, in.Auth, oid)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM push_subs WHERE id IN (SELECT id FROM push_subs WHERE owner_id=$1 ORDER BY id DESC OFFSET $2)`, oid, maxSubsPerOwner)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail500(w, "push subscribe", err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) pushUnsubscribeOwner(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a.exec(r.Context(), "push unsubscribe", `DELETE FROM push_subs WHERE endpoint=$1 AND owner_id=$2`, in.Endpoint, oid)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// Guests are identified by their chat token (the same cookie that opens the chat), not by an account.
func (a *App) pushSubscribeGuest(w http.ResponseWriter, r *http.Request) {
	tid, _, ok := a.visitorThread(w, r)
	if !ok {
		return
	}
	if !a.rl.allow("psub-t:"+strconv.FormatInt(tid, 10), 20, time.Hour) || !a.rl.allow("psub-ip:"+clientIP(r), 60, time.Hour) {
		fail(w, http.StatusTooManyRequests, "Too many attempts. Try again later")
		return
	}
	in, ok := a.readSub(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "push guest begin", err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO push_subs(endpoint,p256dh,auth,thread_id) VALUES($1,$2,$3,$4)
		ON CONFLICT (endpoint, thread_id) WHERE thread_id IS NOT NULL DO UPDATE SET p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth, fails=0`, in.Endpoint, in.P256dh, in.Auth, tid)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM push_subs WHERE id IN (SELECT id FROM push_subs WHERE thread_id=$1 ORDER BY id DESC OFFSET $2)`, tid, maxSubsPerThread)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail500(w, "push guest subscribe", err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) pushUnsubscribeGuest(w http.ResponseWriter, r *http.Request) {
	tid, _, ok := a.visitorThread(w, r)
	if !ok {
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a.exec(r.Context(), "push guest unsubscribe", `DELETE FROM push_subs WHERE endpoint=$1 AND thread_id=$2`, in.Endpoint, tid)
	jsonOut(w, 200, map[string]any{"ok": true})
}

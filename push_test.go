package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- pure functions ----------

func TestEndpointRules(t *testing.T) {
	p := &Pusher{hosts: defaultPushHosts}
	good := []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc",
		"https://wns2-par02p.notify.windows.com/w/?token=abc",
	}
	bad := []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://127.0.0.1/x",
		"https://localhost/x",
		"https://evil.example.com/x",
		"https://fcm.googleapis.com.evil.com/x",
		"https://evilfcm.googleapis.com.example/x",
		"https://user:pw@fcm.googleapis.com/x",
		"https://fcm.googleapis.com:8443/x",
		"https://fcm.googleapis.com/x#frag",
		"https://169.254.169.254/latest/meta-data",
		"ftp://fcm.googleapis.com/x",
		"https://fcm.googleapis.com/" + strings.Repeat("a", 800),
		"",
	}
	for _, e := range good {
		if !p.endpointOK(e) {
			t.Errorf("should accept %q", e)
		}
	}
	for _, e := range bad {
		if p.endpointOK(e) {
			t.Errorf("should refuse %q", e)
		}
	}
	p.hosts = append(p.hosts, "push.example.org")
	if !p.endpointOK("https://push.example.org/x") {
		t.Error("PUSH_ALLOWED_HOSTS entry should be accepted")
	}
}

func TestPublicOnly(t *testing.T) {
	for _, a := range []string{"127.0.0.1:443", "10.1.2.3:443", "192.168.0.9:443", "172.16.0.1:443", "169.254.169.254:80", "[::1]:443", "[fc00::1]:443", "100.64.0.1:443", "0.0.0.0:443", "224.0.0.1:443", "not-an-ip:443"} {
		if publicOnly("tcp", a, nil) == nil {
			t.Errorf("should refuse %s", a)
		}
	}
	for _, a := range []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443"} {
		if err := publicOnly("tcp", a, nil); err != nil {
			t.Errorf("should allow %s: %v", a, err)
		}
	}
}

func TestPushQuiet(t *testing.T) {
	if pushQuiet(-1, -1, 0) || pushQuiet(5, -1, 0) {
		t.Error("quiet hours that are off must never be quiet")
	}
	h := time.Now().UTC().Hour()
	if !pushQuiet(h, (h+2)%24, 0) {
		t.Error("current hour should be inside the window")
	}
	if pushQuiet((h+3)%24, (h+5)%24, 0) {
		t.Error("a window later today should not be quiet now")
	}
}

func TestVapidPair(t *testing.T) {
	a := &App{}
	_ = a
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv, pub := base64.RawURLEncoding.EncodeToString(k.Bytes()), base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	if !vapidPair(pub, priv) {
		t.Error("matching pair refused")
	}
	other, _ := ecdh.P256().GenerateKey(rand.Reader)
	if vapidPair(base64.RawURLEncoding.EncodeToString(other.PublicKey().Bytes()), priv) {
		t.Error("mismatched pair accepted")
	}
	if vapidPair("abc", "def") || vapidPair("", "") {
		t.Error("junk accepted")
	}
}

func TestScheduleCoalesces(t *testing.T) {
	oldD, oldG := pushDelay, pushGap
	pushDelay, pushGap = 40*time.Millisecond, 250*time.Millisecond
	defer func() { pushDelay, pushGap = oldD, oldG }()
	var mu sync.Mutex
	var runs []time.Time
	p := &Pusher{slots: map[string]*slot{}}
	p.run = func(int64, string, string) { mu.Lock(); runs = append(runs, time.Now()); mu.Unlock() }
	start := time.Now()
	for i := 0; i < 20; i++ { // a burst of 20 messages in one chat
		p.schedule(7, "o", "message")
	}
	p.schedule(8, "o", "message") // a different chat is independent
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	if len(runs) != 2 {
		t.Fatalf("burst should give one push per chat, got %d total", len(runs))
	}
	mu.Unlock()
	p.schedule(7, "o", "message") // right after a push: must wait for the gap
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := len(runs)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("second push for the same chat came too early (%d)", n)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(runs) != 3 {
		t.Fatalf("the pending event was lost, got %d", len(runs))
	}
	if runs[2].Sub(start) < pushGap {
		t.Errorf("gap not honoured: %v", runs[2].Sub(start))
	}
}

// ---------- fake push service that decrypts what it receives (RFC 8291) ----------

type fakeSub struct {
	priv *ecdh.PrivateKey
	auth []byte
}

type fakePush struct {
	*httptest.Server
	mu     sync.Mutex
	subs   map[string]fakeSub // by path
	status map[string]int     // forced response status by path
	got    map[string]chan map[string]string
	hdr    map[string]http.Header
}

func newFakePush(t *testing.T) *fakePush {
	f := &fakePush{subs: map[string]fakeSub{}, status: map[string]int{}, got: map[string]chan map[string]string{}, hdr: map[string]http.Header{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		s, known := f.subs[r.URL.Path]
		code := f.status[r.URL.Path]
		ch := f.got[r.URL.Path]
		f.hdr[r.URL.Path] = r.Header.Clone()
		f.mu.Unlock()
		if !known {
			w.WriteHeader(404)
			return
		}
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		pt, err := decryptPush(body, s)
		if err != nil {
			t.Errorf("fake push service could not decrypt: %v", err)
			w.WriteHeader(400)
			return
		}
		var m map[string]string
		if err := json.Unmarshal(pt, &m); err != nil {
			t.Errorf("payload is not the expected JSON object: %q", pt)
		}
		ch <- m
		w.WriteHeader(201)
	}))
	t.Cleanup(f.Close)
	return f
}

// add makes a browser-style subscription and returns the JSON the browser would send to the server.
func (f *fakePush) add(t *testing.T, name string) (endpoint, p256dh, auth string) {
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	a := make([]byte, 16)
	rand.Read(a)
	f.mu.Lock()
	f.subs["/push/"+name] = fakeSub{k, a}
	f.got["/push/"+name] = make(chan map[string]string, 16)
	f.mu.Unlock()
	return f.URL + "/push/" + name, base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(a)
}

func (f *fakePush) wait(t *testing.T, name string) map[string]string {
	t.Helper()
	select {
	case m := <-f.got["/push/"+name]:
		return m
	case <-time.After(4 * time.Second):
		t.Fatalf("no push arrived for %s", name)
	}
	return nil
}

func (f *fakePush) none(t *testing.T, name string, d time.Duration) {
	t.Helper()
	select {
	case m := <-f.got["/push/"+name]:
		t.Fatalf("unexpected push for %s: %v", name, m)
	case <-time.After(d):
	}
}

func decryptPush(body []byte, s fakeSub) ([]byte, error) {
	if len(body) < 16+4+1+65 {
		return nil, fmt.Errorf("short body")
	}
	salt, idlen := body[:16], int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	pk, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		return nil, err
	}
	secret, err := s.priv.ECDH(pk)
	if err != nil {
		return nil, err
	}
	info := "WebPush: info\x00" + string(s.priv.PublicKey().Bytes()) + string(asPub)
	ikm, _ := hkdf.Key(sha256.New, secret, s.auth, info, 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	g, _ := cipher.NewGCM(blk)
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	pt = bytes.TrimRight(pt, "\x00")
	if len(pt) == 0 || pt[len(pt)-1] != 2 {
		return nil, fmt.Errorf("bad padding")
	}
	return pt[:len(pt)-1], nil
}

// ---------- database-backed flow ----------

func testApp(t *testing.T) *App {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the database tests")
	}
	for _, k := range []string{"VAPID_PUBLIC_KEY", "VAPID_PRIVATE_KEY", "VAPID_SUBJECT", "PUSH_ALLOWED_HOSTS"} {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: Config{Secret: "test-secret", AdminPath: "adm1n", DBMaxConns: 4}, db: db, hub: newHub(), set: &Settings{m: map[string]string{}}, rl: newLimiter(), fails: newFails(), cache: newCache()}
	if err := a.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a.push = a.initPush(ctx)
	if !a.push.on {
		t.Fatal("push should be on after first boot")
	}
	return a
}

type cl struct {
	t *testing.T
	c *http.Client
	u string
}

func newCl(t *testing.T, u string) *cl {
	j, _ := cookiejar.New(nil)
	return &cl{t, &http.Client{Jar: j}, u}
}

func (c *cl) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.u+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (c *cl) must(method, path string, body any) map[string]any {
	c.t.Helper()
	code, out := c.do(method, path, body)
	if code != 200 {
		c.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func fast(t *testing.T) {
	oldD, oldG := pushDelay, pushGap
	pushDelay, pushGap = 30*time.Millisecond, 0
	t.Cleanup(func() { pushDelay, pushGap = oldD, oldG })
}

func TestPushFlow(t *testing.T) {
	fast(t)
	a := testApp(t)
	fp := newFakePush(t)
	a.push.allow = func(string) bool { return true }
	a.push.client = fp.Client()
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	ctx := context.Background()

	// public pieces
	code, out := newCl(t, srv.URL).do("GET", "/api/push/key", nil)
	if code != 200 || out["key"] != a.push.pub {
		t.Fatalf("key endpoint: %d %v", code, out)
	}
	resp, err := http.Get(srv.URL + "/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	swb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Service-Worker-Allowed") != "/" || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Errorf("sw.js headers: %v", resp.Header)
	}
	if !strings.Contains(string(swb), "const BUILD = '"+buildID+"'") || !strings.Contains(string(swb), `const SITE = "allovchat"`) || strings.Contains(string(swb), "__") {
		t.Errorf("sw.js placeholders not filled:\n%.300s", swb)
	}
	resp, _ = http.Get(srv.URL + "/offline")
	ob, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(ob), "offline") || !strings.Contains(string(ob), "style.css?v="+buildID) {
		t.Errorf("offline page: %d", resp.StatusCode)
	}

	// owner signs up, makes a link, turns notifications on
	owner := newCl(t, srv.URL)
	owner.must("POST", "/api/register", map[string]string{"username": "alice", "password": "correct horse"})
	slug := owner.must("POST", "/api/links", map[string]string{"label": "Secret bike label", "cid": ""})["slug"].(string)
	ep, p256, auth := fp.add(t, "owner1")
	if code, _ := newCl(t, srv.URL).do("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth}); code != 401 {
		t.Errorf("subscribing without a session should be 401, got %d", code)
	}
	owner.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth})
	owner.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth}) // twice is fine
	var n int
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1`, ep).Scan(&n)
	if n != 1 {
		t.Fatalf("subscribing twice should keep one row, got %d", n)
	}

	// validation: nobody can make the server call an arbitrary address or store junk keys
	for name, body := range map[string]map[string]string{
		"bad host": {"endpoint": "https://evil.example.com/x", "p256dh": p256, "auth": auth},
		"http":     {"endpoint": strings.Replace(ep, "https://", "http://", 1), "p256dh": p256, "auth": auth},
		"bad key":  {"endpoint": ep, "p256dh": "AAAA", "auth": auth},
		"bad auth": {"endpoint": ep, "p256dh": p256, "auth": "AAAA"},
	} {
		a.push.allow = nil // use the real allowlist for this check
		a.push.hosts = defaultPushHosts
		if code, _ := owner.do("POST", "/api/push/subscribe", body); code != 400 {
			t.Errorf("%s should be refused with 400, got %d", name, code)
		}
		a.push.allow = func(h string) bool { return h == "127.0.0.1" }
	}
	a.push.allow = func(string) bool { return true }
	if code, _ := owner.do("POST", "/api/push/subscribe", map[string]any{"endpoint": ep, "p256dh": p256, "auth": auth, "extra": 1}); code != 400 {
		t.Errorf("unknown fields should be refused, got %d", code)
	}

	// a guest writes: the owner gets a generic "request" push with nothing sensitive in it
	guest := newCl(t, srv.URL)
	tok := randHex(16)
	guest.must("POST", "/api/l/"+slug+"/start", map[string]string{"message": "hello from the guest, my name is Bob", "token": tok, "turnstile": "", "pub": ""})
	m := fp.wait(t, "owner1")
	if len(m) != 3 || m["k"] != "request" || m["u"] != "/app" || len(m["g"]) != 22 {
		t.Fatalf("request payload: %v", m)
	}
	raw, _ := json.Marshal(m)
	for _, bad := range []string{"Bob", "hello", "Secret", "bike", "alice", slug} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("payload leaks %q: %s", bad, raw)
		}
	}
	fp.hdr["/push/owner1"].Get("Topic")
	if h := fp.hdr["/push/owner1"]; h.Get("Topic") != m["g"] || h.Get("TTL") != "43200" || !strings.HasPrefix(h.Get("Authorization"), "vapid ") || h.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("push headers: %v", h)
	}
	fp.none(t, "owner1", 150*time.Millisecond)

	// the guest turns alerts on for this chat only
	gep, gp, ga := fp.add(t, "guest1")
	guest.must("POST", "/api/l/"+slug+"/push/subscribe", map[string]string{"endpoint": gep, "p256dh": gp, "auth": ga})
	if code, _ := newCl(t, srv.URL).do("POST", "/api/l/"+slug+"/push/subscribe", map[string]string{"endpoint": gep, "p256dh": gp, "auth": ga}); code != 404 {
		t.Errorf("a stranger without the chat cookie must not subscribe, got %d", code)
	}

	// the owner accepts: the guest hears about it
	chats := owner.must("GET", "/api/chats", nil)
	_ = chats
	var tid int64
	a.db.QueryRow(ctx, `SELECT id FROM threads LIMIT 1`).Scan(&tid)
	owner.must("POST", fmt.Sprintf("/api/t/%d/accept", tid), map[string]any{})
	gm := fp.wait(t, "guest1")
	if gm["k"] != "accepted" || gm["u"] != "/l/"+slug {
		t.Errorf("accepted payload: %v", gm)
	}

	// messages both ways
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "second message", "cid": randHex(8)})
	if m := fp.wait(t, "owner1"); m["k"] != "message" {
		t.Errorf("message payload: %v", m)
	}
	owner.must("POST", fmt.Sprintf("/api/t/%d/send", tid), map[string]string{"body": "reply", "cid": randHex(8)})
	if m := fp.wait(t, "guest1"); m["k"] != "message" || m["u"] != "/l/"+slug {
		t.Errorf("reply payload: %v", m)
	}

	// a burst becomes one push
	for i := 0; i < 6; i++ {
		guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": fmt.Sprint("burst ", i), "cid": randHex(8)})
	}
	fp.wait(t, "owner1")
	fp.none(t, "owner1", 200*time.Millisecond)

	// someone with the chat open already sees it live: no push
	a.hub.join(tid, "o")
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "live", "cid": randHex(8)})
	fp.none(t, "owner1", 300*time.Millisecond)
	a.hub.part(tid, "o")

	// quiet hours silence the owner (and only the owner)
	h := time.Now().UTC().Hour()
	a.db.Exec(ctx, `UPDATE links SET quiet_start=$1, quiet_end=$2, tz=0`, h, (h+2)%24)
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "quiet", "cid": randHex(8)})
	fp.none(t, "owner1", 300*time.Millisecond)
	owner.must("POST", fmt.Sprintf("/api/t/%d/send", tid), map[string]string{"body": "still reaches the guest", "cid": randHex(8)})
	fp.wait(t, "guest1")
	a.db.Exec(ctx, `UPDATE links SET quiet_start=-1, quiet_end=-1`)

	// a completed reveal tells the person who agreed first
	owner.must("POST", fmt.Sprintf("/api/t/%d/reveal", tid), map[string]any{"agree": true, "contact": "+254700000000"})
	fp.none(t, "guest1", 200*time.Millisecond) // not complete yet: nothing to announce
	guest.must("POST", "/api/l/"+slug+"/reveal", map[string]any{"agree": true, "contact": "@bob"})
	if m := fp.wait(t, "owner1"); m["k"] != "reveal" {
		t.Errorf("reveal payload: %v", m)
	}

	// a shared browser: the next person to sign in takes over the device
	bob := newCl(t, srv.URL)
	bob.must("POST", "/api/register", map[string]string{"username": "bobby", "password": "correct horse"})
	bob.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth})
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1 AND owner_id IS NOT NULL`, ep).Scan(&n)
	if n != 1 {
		t.Errorf("endpoint should belong to one account, rows: %d", n)
	}
	var who string
	a.db.QueryRow(ctx, `SELECT o.username FROM push_subs s JOIN owners o ON o.id=s.owner_id WHERE s.endpoint=$1`, ep).Scan(&who)
	if who != "bobby" {
		t.Errorf("device should now belong to bobby, got %q", who)
	}
	bob.must("POST", "/api/push/unsubscribe", map[string]string{"endpoint": ep})
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1 AND owner_id IS NOT NULL`, ep).Scan(&n)
	if n != 0 {
		t.Errorf("unsubscribe should remove the row, got %d", n)
	}
	// someone else can't remove alice's device
	owner.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth})
	bob.must("POST", "/api/push/unsubscribe", map[string]string{"endpoint": ep})
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1`, ep).Scan(&n)
	if n != 1 {
		t.Errorf("another account must not be able to remove alice's subscription")
	}

	// a push service that says "gone" (410) removes the subscription
	fp.mu.Lock()
	fp.status["/push/owner1"] = 410
	fp.mu.Unlock()
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "to a dead device", "cid": randHex(8)})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1`, ep).Scan(&n)
		if n == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n != 0 {
		t.Errorf("410 should delete the subscription, rows left: %d", n)
	}

	// repeated failures prune a subscription; one success resets the count
	owner.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth})
	fp.mu.Lock()
	fp.status["/push/owner1"] = 500
	fp.mu.Unlock()
	for i := 0; i < pushMaxFails; i++ {
		guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": fmt.Sprint("fail ", i), "cid": randHex(8)})
		time.Sleep(150 * time.Millisecond)
	}
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE endpoint=$1`, ep).Scan(&n)
	if n != 0 {
		t.Errorf("a subscription failing %d times in a row should be removed, rows: %d", pushMaxFails, n)
	}

	// ending the chat removes the guest's subscription; deleting the account removes the owner's
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE thread_id=$1`, tid).Scan(&n)
	if n != 1 {
		t.Fatalf("guest subscription missing before the chat ends")
	}
	guest.must("POST", "/api/l/"+slug+"/leave", map[string]any{})
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE thread_id IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("leaving a chat should delete its subscriptions, rows: %d", n)
	}
	owner.must("POST", "/api/push/subscribe", map[string]string{"endpoint": ep, "p256dh": p256, "auth": auth})
	owner.must("POST", "/api/account/delete", map[string]string{"password": "correct horse", "confirm": "DELETE"})
	a.db.QueryRow(ctx, `SELECT count(*) FROM push_subs WHERE owner_id IS NOT NULL AND endpoint=$1`, ep).Scan(&n)
	if n != 0 {
		t.Errorf("deleting the account should delete its subscriptions, rows: %d", n)
	}
}

func TestPushCaps(t *testing.T) {
	a := testApp(t)
	a.push.allow = func(string) bool { return true }
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	o := newCl(t, srv.URL)
	o.must("POST", "/api/register", map[string]string{"username": "capper", "password": "correct horse"})
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	p := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	au := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	for i := 0; i < maxSubsPerOwner+4; i++ {
		o.must("POST", "/api/push/subscribe", map[string]string{"endpoint": fmt.Sprintf("https://127.0.0.1/p/%d", i), "p256dh": p, "auth": au})
	}
	var n int
	a.db.QueryRow(context.Background(), `SELECT count(*) FROM push_subs`).Scan(&n)
	if n != maxSubsPerOwner {
		t.Errorf("an account keeps at most %d devices, got %d", maxSubsPerOwner, n)
	}
}

func TestPushOffWithHalfConfiguredKeys(t *testing.T) {
	a := testApp(t)
	t.Setenv("VAPID_PUBLIC_KEY", "only-public")
	p := a.initPush(context.Background())
	if p.on {
		t.Error("push must stay off when only one VAPID key is set")
	}
	t.Setenv("VAPID_PUBLIC_KEY", "")
	// keys generated once stay the same on the next boot
	p2 := a.initPush(context.Background())
	if !p2.on || p2.pub != a.push.pub {
		t.Error("the stored VAPID pair should be reused")
	}
	_ = net.IPv4len
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// app builds a request the way the Android app's WebView does: from its own origin, with a bearer token.
func appReq(t *testing.T, method, url, tok, origin string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestAppLoginBearerAndCORS(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	web := newCl(t, srv.URL)
	web.must("POST", "/api/register", map[string]string{"username": "alice", "password": "correct horse"})

	// wrong password is relayed like on the website
	resp, out := appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "nope nope"})
	if resp.StatusCode != 401 || out["error"] == nil {
		t.Fatalf("wrong password: %d %v", resp.StatusCode, out)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != defaultAppOrigin {
		t.Errorf("login from the app origin needs CORS headers: %v", resp.Header)
	}

	resp, out = appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "correct horse"})
	tok, _ := out["token"].(string)
	if resp.StatusCode != 200 || len(tok) != 48 {
		t.Fatalf("login: %d %v", resp.StatusCode, out)
	}
	if len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Errorf("an app login must not set a browser cookie: %v", resp.Header["Set-Cookie"])
	}
	var n int
	a.db.QueryRow(testCtx(), `SELECT count(*) FROM sessions WHERE kind='owner'`).Scan(&n)
	if n != 1 { // only the website session from register; the temporary one from the app login is gone
		t.Errorf("owner sessions left over: %d", n)
	}

	// the token works for normal calls
	if resp, out := appReq(t, "GET", srv.URL+"/api/me", tok, defaultAppOrigin, nil); resp.StatusCode != 200 || out["username"] != "alice" {
		t.Fatalf("me with token: %d %v", resp.StatusCode, out)
	}
	if resp, _ := appReq(t, "POST", srv.URL+"/api/links", tok, defaultAppOrigin, map[string]string{"label": "From the app", "cid": ""}); resp.StatusCode != 200 {
		t.Errorf("creating a link with the token: %d", resp.StatusCode)
	}
	// a made-up or truncated token does nothing
	for _, bad := range []string{strings.Repeat("a", 48), "short", tok[:47] + "x"} {
		if resp, _ := appReq(t, "GET", srv.URL+"/api/me", bad, defaultAppOrigin, nil); resp.StatusCode != 401 {
			t.Errorf("bad token %q accepted: %d", bad, resp.StatusCode)
		}
	}
	// the website's cookie value is not a bearer token, and the other way round
	var cookieTok string
	for _, c := range web.c.Jar.Cookies(mustURL(srv.URL)) {
		if c.Name == "os" {
			cookieTok = c.Value
		}
	}
	if resp, _ := appReq(t, "GET", srv.URL+"/api/me", cookieTok, "", nil); resp.StatusCode != 401 {
		t.Errorf("a browser session token must not work as a bearer token: %d", resp.StatusCode)
	}

	// cross-site rules
	if resp, _ := appReq(t, "POST", srv.URL+"/api/links", tok, "https://evil.example", map[string]string{"label": "x", "cid": ""}); resp.StatusCode != 403 {
		t.Errorf("a foreign origin must be blocked even with a token: %d", resp.StatusCode)
	}
	if resp, _ := appReq(t, "POST", srv.URL+"/api/links", "", defaultAppOrigin, map[string]string{"label": "x", "cid": ""}); resp.StatusCode != 403 {
		t.Errorf("the app origin without a token must not get past the origin check: %d", resp.StatusCode)
	}
	resp, _ = appReq(t, "OPTIONS", srv.URL+"/api/links", "", defaultAppOrigin, nil)
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != defaultAppOrigin || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = appReq(t, "OPTIONS", srv.URL+"/api/links", "", "https://evil.example", nil)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("a foreign origin must not receive CORS headers")
	}
	resp, _ = appReq(t, "GET", srv.URL+"/", "", defaultAppOrigin, nil)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("CORS must be limited to /api/")
	}

	// logging out ends the token straight away
	appReq(t, "POST", srv.URL+"/api/app/logout", tok, defaultAppOrigin, map[string]any{})
	if resp, _ := appReq(t, "GET", srv.URL+"/api/me", tok, defaultAppOrigin, nil); resp.StatusCode != 401 {
		t.Errorf("token still valid after logout: %d", resp.StatusCode)
	}
}

func TestAppLoginLockoutIsRelayed(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	newCl(t, srv.URL).must("POST", "/api/register", map[string]string{"username": "alice", "password": "correct horse"})
	var last *http.Response
	for i := 0; i < 8; i++ {
		last, _ = appReq(t, "POST", srv.URL+"/api/app/login", "", "", map[string]string{"username": "alice", "password": "wrong wrong"})
	}
	if last.StatusCode != 429 || last.Header.Get("Retry-After") == "" {
		t.Errorf("repeated failures should lock out with Retry-After, got %d %v", last.StatusCode, last.Header)
	}
	// even the right password waits
	if resp, _ := appReq(t, "POST", srv.URL+"/api/app/login", "", "", map[string]string{"username": "alice", "password": "correct horse"}); resp.StatusCode != 429 {
		t.Errorf("lockout applies to the app too: %d", resp.StatusCode)
	}
}

// readEvents reads server-sent events from a stream into a channel.
func readEvents(t *testing.T, url, tok string) (chan map[string]string, func(), int) {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/app/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan map[string]string, 16)
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return ch, func() {}, resp.StatusCode
	}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") {
				var m map[string]string
				json.Unmarshal([]byte(l[6:]), &m)
				ch <- m
			}
		}
	}()
	return ch, func() { resp.Body.Close() }, 200
}

func TestAppEventsStream(t *testing.T) {
	fast(t)
	a := testApp(t)
	a.push.on = false // app events must not depend on web push being set up
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	owner := newCl(t, srv.URL)
	owner.must("POST", "/api/register", map[string]string{"username": "alice", "password": "correct horse"})
	slug := owner.must("POST", "/api/links", map[string]string{"label": "Secret bike label", "cid": ""})["slug"].(string)
	_, out := appReq(t, "POST", srv.URL+"/api/app/login", "", "", map[string]string{"username": "alice", "password": "correct horse"})
	tok := out["token"].(string)

	if _, _, code := readEvents(t, srv.URL, "nope"); code != 401 {
		t.Errorf("stream without a valid token: %d", code)
	}
	ev, stop, code := readEvents(t, srv.URL, tok)
	if code != 200 {
		t.Fatalf("stream: %d", code)
	}
	defer stop()
	wait := func(d time.Duration) map[string]string {
		select {
		case m := <-ev:
			return m
		case <-time.After(d):
			return nil
		}
	}

	guest := newCl(t, srv.URL)
	guest.must("POST", "/api/l/"+slug+"/start", map[string]string{"message": "hello, my name is Bob", "token": randHex(16), "turnstile": "", "pub": ""})
	m := wait(3 * time.Second)
	if m == nil || m["k"] != "request" || len(m) != 2 || len(m["g"]) != 22 {
		t.Fatalf("request event: %v", m)
	}
	raw, _ := json.Marshal(m)
	for _, bad := range []string{"Bob", "hello", "Secret", "bike", slug} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("event leaks %q", bad)
		}
	}
	var tid int64
	a.db.QueryRow(testCtx(), `SELECT id FROM threads LIMIT 1`).Scan(&tid)
	owner.must("POST", fmt.Sprintf("/api/t/%d/accept", tid), map[string]any{})
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "second", "cid": randHex(8)})
	if m := wait(3 * time.Second); m == nil || m["k"] != "message" {
		t.Errorf("message event: %v", m)
	}

	// quiet hours silence it
	h := time.Now().UTC().Hour()
	a.db.Exec(testCtx(), `UPDATE links SET quiet_start=$1, quiet_end=$2, tz=0`, h, (h+2)%24)
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "quiet", "cid": randHex(8)})
	if m := wait(400 * time.Millisecond); m != nil {
		t.Errorf("quiet hours ignored: %v", m)
	}
	a.db.Exec(testCtx(), `UPDATE links SET quiet_start=-1, quiet_end=-1`)

	// someone with the chat open already sees it, so no event
	a.hub.join(tid, "o")
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "live", "cid": randHex(8)})
	if m := wait(400 * time.Millisecond); m != nil {
		t.Errorf("event sent although the chat is open: %v", m)
	}
	a.hub.part(tid, "o")

	// another account's stream never sees this account's events
	other := newCl(t, srv.URL)
	other.must("POST", "/api/register", map[string]string{"username": "carol", "password": "correct horse"})
	_, o2 := appReq(t, "POST", srv.URL+"/api/app/login", "", "", map[string]string{"username": "carol", "password": "correct horse"})
	ev2, stop2, _ := readEvents(t, srv.URL, o2["token"].(string))
	defer stop2()
	guest.must("POST", "/api/l/"+slug+"/send", map[string]string{"body": "for alice only", "cid": randHex(8)})
	if m := wait(3 * time.Second); m == nil {
		t.Errorf("alice should still get her event")
	}
	select {
	case m := <-ev2:
		t.Errorf("carol received alice's event: %v", m)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAppStreamLimit(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	newCl(t, srv.URL).must("POST", "/api/register", map[string]string{"username": "alice", "password": "correct horse"})
	_, out := appReq(t, "POST", srv.URL+"/api/app/login", "", "", map[string]string{"username": "alice", "password": "correct horse"})
	tok := out["token"].(string)
	for i := 0; i < appMaxStreams; i++ {
		_, stop, code := readEvents(t, srv.URL, tok)
		if code != 200 {
			t.Fatalf("stream %d refused: %d", i, code)
		}
		defer stop()
	}
	if _, _, code := readEvents(t, srv.URL, tok); code != 429 {
		t.Errorf("a sixth stream should be refused, got %d", code)
	}
}

func testCtx() context.Context { return context.Background() }

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// chatPair sets up alice (owns a link), bob (a guest who made an account and saved the chat) and their chat.
func chatPair(t *testing.T) (a *App, srv *httptest.Server, alice, bob *cl, tid int64, slug string, lid int64) {
	a, srv = vaultServer(t)
	alice = signUp(t, srv, "alice", "correct horse")
	lid, slug = newLinkID(t, a, alice, "alice link")
	bob = newCl(t, srv.URL)
	bob.must("POST", "/api/l/"+slug+"/start", map[string]string{"message": "hello there", "token": randHex(16), "turnstile": "", "pub": ""})
	bob.must("POST", "/api/register", map[string]string{"username": "bobby", "password": "battery staple"})
	bob.must("POST", "/api/l/"+slug+"/claim", map[string]string{})
	if err := a.db.QueryRow(testCtx(), `SELECT id FROM threads WHERE link_id=$1`, lid).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	alice.must("POST", "/api/t/"+itoa(tid)+"/accept", map[string]any{})
	return
}

func syncOnce(t *testing.T, c *cl, cursor int64, limit int) map[string]any {
	t.Helper()
	return c.must("GET", "/api/sync?cursor="+itoa(cursor)+"&limit="+strconv.Itoa(limit), nil)
}

func list(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

func msgByID(out map[string]any) map[int64]map[string]any {
	res := map[int64]map[string]any{}
	for _, x := range list(out, "messages") {
		m := x.(map[string]any)
		res[int64(m["id"].(float64))] = m
	}
	return res
}

func cursorOf(out map[string]any) int64 { return int64(out["cursor"].(float64)) }

func TestSyncFirstSyncAndIsolation(t *testing.T) {
	_, srv, alice, bob, tid, _, _ := chatPair(t)
	carol := signUp(t, srv, "carol", "third passphrase")
	for name, c := range map[string]*cl{"alice": alice, "bob": bob} {
		out := syncOnce(t, c, 0, 100)
		th, ms := list(out, "threads"), list(out, "messages")
		if len(th) != 1 || len(ms) != 1 || cursorOf(out) == 0 || out["more"] != false || out["reset"] != false {
			t.Fatalf("%s first sync: %v", name, out)
		}
		row := th[0].(map[string]any)
		want := map[string]string{"alice": "owner", "bob": "guest"}[name]
		if int64(row["id"].(float64)) != tid || row["role"] != want || row["status"] != "open" {
			t.Errorf("%s thread: %v", name, row)
		}
		if ms[0].(map[string]any)["body"] != "hello there" || ms[0].(map[string]any)["side"] != "v" {
			t.Errorf("%s message: %v", name, ms[0])
		}
	}
	out := syncOnce(t, carol, 0, 100)
	if len(list(out, "threads"))+len(list(out, "messages"))+len(list(out, "gone")) != 0 {
		t.Errorf("another account must see nothing: %v", out)
	}
}

func TestSyncNewMessagesAndDeliveryState(t *testing.T) {
	_, _, alice, bob, tid, _, _ := chatPair(t)
	cur := cursorOf(syncOnce(t, alice, 0, 100))
	cid := randHex(16)
	alice.must("POST", "/api/t/"+itoa(tid)+"/send", map[string]string{"body": "hi bob", "cid": cid})

	out := syncOnce(t, alice, cur, 100)
	var sent map[string]any
	for _, m := range msgByID(out) {
		if m["cid"] == cid {
			sent = m
		}
	}
	if sent == nil || sent["read"] != false || sent["side"] != "o" {
		t.Fatalf("sent message should be in the next sync, unread: %v", out)
	}
	cur = cursorOf(out)

	bobOut := syncOnce(t, bob, 0, 100)
	if th := list(bobOut, "threads")[0].(map[string]any); th["unread"].(float64) != 1 {
		t.Errorf("bob's unread count: %v", th)
	}
	bob.must("GET", "/api/m/"+itoa(tid)+"/state", nil) // bob opens the chat, which marks alice's message read
	for _, m := range msgByID(syncOnce(t, alice, cur, 100)) {
		if m["cid"] == cid && m["read"] != true {
			t.Errorf("read state should reach the sender: %v", m)
		}
	}
	if th := list(syncOnce(t, bob, 0, 100), "threads")[0].(map[string]any); th["unread"].(float64) != 0 {
		t.Errorf("bob's unread after reading: %v", th)
	}

	// privacy mode hides read state from the sender, and turning it off shows it again
	cur = cursorOf(syncOnce(t, alice, 0, 100))
	bob.must("POST", "/api/m/"+itoa(tid)+"/privacy", map[string]any{"on": true})
	for _, m := range msgByID(syncOnce(t, alice, cur, 100)) {
		if m["cid"] == cid && m["read"] != false {
			t.Errorf("privacy mode must hide the read flag: %v", m)
		}
	}
	cur = cursorOf(syncOnce(t, alice, cur, 100))
	bob.must("POST", "/api/m/"+itoa(tid)+"/privacy", map[string]any{"on": false})
	seen := false
	for _, m := range msgByID(syncOnce(t, alice, cur, 100)) {
		if m["cid"] == cid {
			seen = true
			if m["read"] != true {
				t.Errorf("read flag should come back when privacy is off: %v", m)
			}
		}
	}
	if !seen {
		t.Error("turning privacy off must re-send the messages whose read flag changed")
	}
}

func TestSyncRemovals(t *testing.T) {
	a, _, alice, bob, tid, slug, lid := chatPair(t)
	aCur, bCur := cursorOf(syncOnce(t, alice, 0, 100)), cursorOf(syncOnce(t, bob, 0, 100))

	// bob removes the chat from his account: his other devices drop it, alice keeps it
	bob.must("POST", "/api/m/"+itoa(tid)+"/unclaim", map[string]any{})
	if g := list(syncOnce(t, bob, bCur, 100), "gone"); len(g) != 1 || int64(g[0].(float64)) != tid {
		t.Errorf("unclaim should be a removal for bob: %v", g)
	}
	if g := list(syncOnce(t, alice, aCur, 100), "gone"); len(g) != 0 {
		t.Errorf("alice must not lose the chat when bob unclaims: %v", g)
	}
	bob.must("POST", "/api/l/"+slug+"/claim", map[string]string{})

	// "Leave" deletes the chat for both
	aCur, bCur = cursorOf(syncOnce(t, alice, 0, 100)), cursorOf(syncOnce(t, bob, 0, 100))
	alice.must("POST", "/api/t/"+itoa(tid)+"/leave", map[string]any{})
	for name, c := range map[string]*cl{"alice": alice, "bob": bob} {
		cur := map[string]int64{"alice": aCur, "bob": bCur}[name]
		g := list(syncOnce(t, c, cur, 100), "gone")
		if len(g) == 0 || int64(g[len(g)-1].(float64)) != tid {
			t.Errorf("%s should be told the chat is gone: %v", name, g)
		}
		for _, x := range g {
			if int64(x.(float64)) != tid {
				t.Errorf("%s: unexpected removal %v", name, x)
			}
		}
	}
	// a fresh start never returns it
	if th := list(syncOnce(t, alice, 0, 100), "threads"); len(th) != 0 {
		t.Errorf("deleted chat in a first sync: %v", th)
	}

	// deleting a link removes it for its owner
	aCur = cursorOf(syncOnce(t, alice, 0, 100))
	alice.must("POST", "/api/links/"+itoa(lid)+"/delete", map[string]any{})
	if g := list(syncOnce(t, alice, aCur, 100), "gone_links"); len(g) != 1 || int64(g[0].(float64)) != lid {
		t.Errorf("deleted link: %v", g)
	}
	_ = a
}

func TestSyncPagesWithoutGapsOrLoops(t *testing.T) {
	_, _, alice, bob, tid, _, _ := chatPair(t)
	want := map[int64]bool{}
	for i := 0; i < 6; i++ {
		alice.must("POST", "/api/t/"+itoa(tid)+"/send", map[string]string{"body": "m" + strconv.Itoa(i), "cid": randHex(16)})
	}
	for _, c := range []*cl{alice, bob} {
		got, cur := map[int64]bool{}, int64(0)
		for pages := 0; ; pages++ {
			if pages > 20 {
				t.Fatal("paging does not finish")
			}
			out := syncOnce(t, c, cur, 2)
			if n := cursorOf(out); n < cur {
				t.Fatalf("cursor went backwards: %d -> %d", cur, n)
			} else {
				cur = n
			}
			for id := range msgByID(out) {
				got[id] = true
			}
			if out["more"] != true {
				break
			}
		}
		if len(got) != 7 {
			t.Errorf("paged sync found %d of 7 messages", len(got))
		}
		for id := range got {
			want[id] = true
		}
	}
	if len(want) != 7 {
		t.Errorf("both accounts should see the same 7 messages: %d", len(want))
	}
}

func TestSyncSendIsExactlyOnce(t *testing.T) {
	a, _, alice, bob, tid, _, _ := chatPair(t)
	cid := randHex(16)
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = alice.do("POST", "/api/t/"+itoa(tid)+"/send", map[string]string{"body": "retried", "cid": cid})
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("retry %d: %d", i, c)
		}
	}
	if n := count(t, a, `SELECT count(*) FROM messages WHERE thread_id=$1 AND cid=$2`, tid, cid); n != 1 {
		t.Fatalf("a retried send must store one message, stored %d", n)
	}
	for _, c := range []*cl{alice, bob} {
		n := 0
		for _, m := range msgByID(syncOnce(t, c, 0, 100)) {
			if m["cid"] == cid {
				n++
			}
		}
		if n != 1 {
			t.Errorf("sync shows the retried message %d times", n)
		}
	}
}

func TestSyncLateCommitIsNotMissed(t *testing.T) {
	a, srv, alice, _, _, _, _ := chatPair(t)
	// a second chat on another link, written by a transaction that stays open
	lid2, slug2 := newLinkID(t, a, alice, "second link")
	g2 := newCl(t, srv.URL)
	g2.must("POST", "/api/l/"+slug2+"/start", map[string]string{"message": "first on two", "token": randHex(16), "turnstile": "", "pub": ""})
	var t2 int64
	a.db.QueryRow(testCtx(), `SELECT id FROM threads WHERE link_id=$1`, lid2).Scan(&t2)
	alice.must("POST", "/api/t/"+itoa(t2)+"/accept", map[string]any{})
	t1 := int64(0)
	a.db.QueryRow(testCtx(), `SELECT id FROM threads WHERE link_id<>$1 ORDER BY id LIMIT 1`, lid2).Scan(&t1)

	cur := cursorOf(syncOnce(t, alice, 0, 100))
	tx, err := a.db.Begin(testCtx())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(testCtx())
	if _, err := tx.Exec(testCtx(), `INSERT INTO messages(thread_id, side, body, cid) VALUES($1,'o','late one',$2)`, t2, "late"+randHex(8)); err != nil {
		t.Fatal(err)
	}
	// a later write on the other chat commits first and takes a higher number
	alice.must("POST", "/api/t/"+itoa(t1)+"/send", map[string]string{"body": "quick one", "cid": randHex(16)})
	mid := syncOnce(t, alice, cur, 100)
	found := false
	for _, m := range msgByID(mid) {
		found = found || m["body"] == "quick one"
	}
	if !found {
		t.Fatal("the committed message should be in this page")
	}
	if err := tx.Commit(testCtx()); err != nil {
		t.Fatal(err)
	}
	late := false
	for _, m := range msgByID(syncOnce(t, alice, cursorOf(mid), 100)) {
		late = late || m["body"] == "late one"
	}
	if !late {
		t.Error("a message that committed after a later one must still arrive")
	}
}

func TestSyncValidationAndAccess(t *testing.T) {
	_, srv, alice, _, _, _, _ := chatPair(t)
	anon := newCl(t, srv.URL)
	if code, _ := anon.do("GET", "/api/sync", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	for _, q := range []string{"?cursor=-1", "?cursor=abc", "?limit=0", "?limit=100000", "?limit=x"} {
		if code, _ := alice.do("GET", "/api/sync"+q, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	out := alice.must("GET", "/api/sync?cursor=999999999", nil)
	if out["reset"] != true || cursorOf(out) != 0 {
		t.Errorf("a cursor from the future means the database was reset: %v", out)
	}
	if out := alice.must("GET", "/api/sync", nil); len(list(out, "threads")) != 1 {
		t.Errorf("no cursor means from the start: %v", out)
	}
	// from the app, with a bearer token
	_, login := appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "correct horse"})
	resp, got := appReq(t, "GET", srv.URL+"/api/sync", login["token"].(string), defaultAppOrigin, nil)
	if resp.StatusCode != 200 || len(list(got, "threads")) != 1 {
		t.Errorf("sync from the app: %d %v", resp.StatusCode, got)
	}
}

// streamLines reads the raw event stream, so the test can see ids as well as data.
func streamLines(t *testing.T, url, tok, query string, header map[string]string) (chan string, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/app/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v", err)
	}
	ch := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			ch <- sc.Text()
		}
	}()
	return ch, func() { resp.Body.Close() }
}

func waitLine(ch chan string, prefix string, d time.Duration) (string, bool) {
	end := time.After(d)
	for {
		select {
		case l := <-ch:
			if strings.HasPrefix(l, prefix) {
				return l, true
			}
		case <-end:
			return "", false
		}
	}
}

func TestSyncEventStreamResumes(t *testing.T) {
	fast(t)
	a, srv, alice, bob, tid, _, _ := chatPair(t)
	a.push.on = false
	_, login := appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "correct horse"})
	tok := login["token"].(string)
	cur := cursorOf(syncOnce(t, alice, 0, 100))

	// connected with a cursor that is up to date: nothing is replayed
	ch, stop := streamLines(t, srv.URL, tok, "?cursor="+itoa(a.syncHead(testCtx())+0), nil)
	if l, ok := waitLine(ch, "data: ", 700*time.Millisecond); ok {
		t.Errorf("an up to date device should not get a catch-up event: %s", l)
	}
	stop()

	// something happens while the device is away
	bob.must("POST", "/api/m/"+itoa(tid)+"/send", map[string]string{"body": "while you were gone", "cid": randHex(16)})
	ch, stop = streamLines(t, srv.URL, tok, "", map[string]string{"Last-Event-ID": itoa(cur)})
	defer stop()
	l, ok := waitLine(ch, "data: ", 3*time.Second)
	if !ok || !strings.Contains(l, `"k":"sync"`) {
		t.Fatalf("a device that missed a change must be told right away: %q", l)
	}
	if out := syncOnce(t, alice, cur, 100); len(msgByID(out)) == 0 {
		t.Error("the catch-up event should be backed by a change in the next sync")
	}

	// live events carry an id the device can store and resume from
	ch2, stop2 := streamLines(t, srv.URL, tok, "", nil)
	defer stop2()
	time.Sleep(200 * time.Millisecond)
	bob.must("POST", "/api/m/"+itoa(tid)+"/send", map[string]string{"body": "live one", "cid": randHex(16)})
	if l, ok := waitLine(ch2, "id: ", 3*time.Second); !ok {
		t.Error("live events should carry an id")
	} else if n, err := strconv.ParseInt(strings.TrimPrefix(l, "id: "), 10, 64); err != nil || n < cur {
		t.Errorf("event id %q", l)
	}
}

package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func sendMsg(t *testing.T, c *cl, prefix string, tid int64, body string, extra map[string]any) (int, map[string]any) {
	m := map[string]any{"body": body, "cid": randHex(16)}
	for k, v := range extra {
		m[k] = v
	}
	return c.do("POST", prefix+itoa(tid)+"/send", m)
}

func lastMessageID(t *testing.T, a *App, tid int64) int64 {
	var id int64
	if err := a.db.QueryRow(testCtx(), `SELECT max(id) FROM messages WHERE thread_id=$1`, tid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func find(out map[string]any, id int64) map[string]any { return msgByID(out)[id] }

func TestReplies(t *testing.T) {
	a, _, alice, bob, tid, _, _ := chatPair(t)
	first := lastMessageID(t, a, tid)
	if code, _ := sendMsg(t, alice, "/api/t/", tid, "answering you", map[string]any{"reply": first}); code != 200 {
		t.Fatalf("reply: %d", code)
	}
	id := lastMessageID(t, a, tid)
	for _, c := range []*cl{alice, bob} {
		if m := find(syncOnce(t, c, 0, 100), id); m == nil || int64(m["reply_to"].(float64)) != first {
			t.Errorf("reply should be in sync: %v", m)
		}
	}
	st := bob.must("GET", "/api/m/"+itoa(tid)+"/state", nil)
	if m := find(st, id); m == nil || int64(m["reply_to"].(float64)) != first {
		t.Errorf("reply should be in the chat screen: %v", m)
	}
	for name, v := range map[string]int64{"missing": 999999, "negative": -5} {
		if code, _ := sendMsg(t, alice, "/api/t/", tid, "x", map[string]any{"reply": v}); code != 400 {
			t.Errorf("%s reply: %d", name, code)
		}
	}
	// a message in another chat can't be quoted
	_, slug2 := newLinkID(t, a, alice, "other link")
	g := newCl(t, bob.u)
	g.must("POST", "/api/l/"+slug2+"/start", map[string]string{"message": "elsewhere", "token": randHex(16), "turnstile": "", "pub": ""})
	var t2 int64
	a.db.QueryRow(testCtx(), `SELECT id FROM threads WHERE id<>$1 ORDER BY id DESC LIMIT 1`, tid).Scan(&t2)
	alice.must("POST", "/api/t/"+itoa(t2)+"/accept", map[string]any{})
	if code, _ := sendMsg(t, alice, "/api/t/", t2, "x", map[string]any{"reply": first}); code != 400 {
		t.Errorf("quoting another chat's message: %d", code)
	}
}

func TestReactions(t *testing.T) {
	a, _, alice, bob, tid, _, _ := chatPair(t)
	mid := lastMessageID(t, a, tid)
	cur := cursorOf(syncOnce(t, alice, 0, 100))
	react := func(c *cl, prefix string, body string) (int, map[string]any) {
		return c.do("POST", prefix+itoa(tid)+"/react", map[string]any{"mid": mid, "body": body})
	}
	if code, _ := react(alice, "/api/t/", "👍"); code != 200 {
		t.Fatalf("react: %d", code)
	}
	react(alice, "/api/t/", "👍") // exact state: again changes nothing
	if code, _ := react(bob, "/api/m/", "❤️"); code != 200 {
		t.Fatalf("react as guest: %d", code)
	}
	out := syncOnce(t, bob, 0, 100)
	rx, _ := find(out, mid)["reactions"].([]any)
	if len(rx) != 2 {
		t.Fatalf("both reactions should be on the message: %v", rx)
	}
	if m := find(syncOnce(t, alice, cur, 100), mid); m == nil {
		t.Error("a reaction must re-send the message to the other person")
	}
	st := alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil)
	if rx, _ := find(st, mid)["reactions"].([]any); len(rx) != 2 {
		t.Errorf("reactions on the chat screen: %v", rx)
	}
	cur = cursorOf(syncOnce(t, alice, 0, 100))
	react(alice, "/api/t/", "") // remove
	if rx, _ := find(syncOnce(t, bob, cur, 100), mid)["reactions"].([]any); len(rx) != 1 {
		t.Errorf("removed reaction: %v", rx)
	}
	for name, body := range map[string]string{"too long": strings.Repeat("a", 40), "control": "\x00\x01"} {
		if code, _ := react(alice, "/api/t/", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := alice.do("POST", "/api/t/"+itoa(tid)+"/react", map[string]any{"mid": 999999, "body": "👍"}); code != 404 {
		t.Errorf("unknown message: %d", code)
	}
}

func TestUnsend(t *testing.T) {
	a, _, alice, bob, tid, _, _ := chatPair(t)
	sendMsg(t, alice, "/api/t/", tid, "oops wrong chat", nil)
	mid := lastMessageID(t, a, tid)
	bob.must("POST", "/api/m/"+itoa(tid)+"/react", map[string]any{"mid": mid, "body": "👍"})
	cur := cursorOf(syncOnce(t, bob, 0, 100))

	if code, _ := bob.do("POST", "/api/m/"+itoa(tid)+"/unsend", map[string]any{"mid": mid}); code != 404 {
		t.Errorf("deleting someone else's message: %d", code)
	}
	alice.must("POST", "/api/t/"+itoa(tid)+"/unsend", map[string]any{"mid": mid})
	alice.must("POST", "/api/t/"+itoa(tid)+"/unsend", map[string]any{"mid": mid}) // again is fine
	m := find(syncOnce(t, bob, cur, 100), mid)
	if m == nil || m["deleted"] != true || m["body"] != "" {
		t.Fatalf("deleted message in sync: %v", m)
	}
	if rx, _ := m["reactions"].([]any); len(rx) != 0 {
		t.Errorf("reactions should go with the message: %v", rx)
	}
	var stored string
	a.db.QueryRow(testCtx(), `SELECT body FROM messages WHERE id=$1`, mid).Scan(&stored)
	if stored != "" {
		t.Error("the text must be erased on the server")
	}
	if m := find(alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil), mid); m == nil || m["deleted"] != true {
		t.Errorf("deleted message on the chat screen: %v", m)
	}
	if code, _ := bob.do("POST", "/api/m/"+itoa(tid)+"/react", map[string]any{"mid": mid, "body": "👍"}); code != 404 {
		t.Errorf("reacting to a deleted message: %d", code)
	}
}

func TestDisappearingMessages(t *testing.T) {
	a, _, alice, bob, tid, _, _ := chatPair(t)
	if code, _ := bob.do("POST", "/api/m/"+itoa(tid)+"/disappear", map[string]any{"secs": 7}); code != 400 {
		t.Errorf("odd timer: %d", code)
	}
	sendMsg(t, alice, "/api/t/", tid, "stays", nil)
	stays := lastMessageID(t, a, tid)
	bob.must("POST", "/api/m/"+itoa(tid)+"/disappear", map[string]any{"secs": 300}) // either side can set it
	if th := list(syncOnce(t, alice, 0, 100), "threads")[0].(map[string]any); th["disappear_secs"].(float64) != 300 {
		t.Errorf("timer in sync: %v", th)
	}
	if st := alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil); st["disappear_secs"].(float64) != 300 {
		t.Errorf("timer on the chat screen: %v", st["disappear_secs"])
	}
	sendMsg(t, alice, "/api/t/", tid, "vanishes", nil)
	gone := lastMessageID(t, a, tid)
	m := find(syncOnce(t, bob, 0, 100), gone)
	if m == nil || m["expires_at"] == nil {
		t.Fatalf("a message sent under a timer should carry its expiry: %v", m)
	}
	if find(syncOnce(t, bob, 0, 100), stays)["expires_at"] != nil {
		t.Error("a message sent before the timer must not expire")
	}
	// time passes
	a.db.Exec(testCtx(), `UPDATE messages SET expires_at=now() - interval '1 minute' WHERE id=$1`, gone)
	if find(syncOnce(t, bob, 0, 100), gone) != nil || find(alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil), gone) != nil {
		t.Error("an expired message must not be returned, even before the cleanup runs")
	}
	a.purgeExpiredMessages(testCtx())
	if n := count(t, a, `SELECT count(*) FROM messages WHERE id=$1`, gone); n != 0 {
		t.Error("cleanup should remove it")
	}
	if n := count(t, a, `SELECT count(*) FROM messages WHERE id=$1`, stays); n != 1 {
		t.Error("cleanup must leave other messages alone")
	}
	bob.must("POST", "/api/m/"+itoa(tid)+"/disappear", map[string]any{"secs": 0})
	sendMsg(t, alice, "/api/t/", tid, "normal again", nil)
	if find(syncOnce(t, bob, 0, 100), lastMessageID(t, a, tid))["expires_at"] != nil {
		t.Error("timer off means no expiry")
	}
}

func TestPersonalPrivacySettings(t *testing.T) {
	a, srv, alice, bob, tid, _, _ := chatPair(t)
	p := bob.must("GET", "/api/prefs", nil)
	if p["typing"] != true || p["receipts"] != true || p["online"] != true || p["notif_private"] != true {
		t.Fatalf("defaults: %v", p)
	}
	sendMsg(t, alice, "/api/t/", tid, "did you see this", nil)
	mid := lastMessageID(t, a, tid)
	cur := cursorOf(syncOnce(t, alice, 0, 100))
	bob.must("GET", "/api/m/"+itoa(tid)+"/state", nil)
	if m := find(syncOnce(t, alice, 0, 100), mid); m["read"] != true {
		t.Fatalf("read receipt by default: %v", m)
	}
	// bob turns receipts off: alice's copy of the flag goes back to unread, on every device
	p = bob.must("POST", "/api/prefs", map[string]any{"receipts": false})
	if p["receipts"] != false || p["typing"] != true {
		t.Errorf("only the named setting changes: %v", p)
	}
	if m := find(syncOnce(t, alice, cur, 100), mid); m == nil || m["read"] != false {
		t.Errorf("receipts off must hide the read flag from the sender: %v", m)
	}
	if st := alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil); find(st, mid)["is_read"] != false {
		t.Errorf("and on the chat screen: %v", find(st, mid))
	}
	// bob's second device sees the setting
	dev2 := newCl(t, srv.URL)
	dev2.must("POST", "/api/login", map[string]string{"username": "bobby", "password": "battery staple"})
	if pr, _ := syncOnce(t, dev2, 0, 100)["prefs"].(map[string]any); pr["receipts"] != false {
		t.Errorf("settings should sync: %v", pr)
	}
	bob.must("POST", "/api/prefs", map[string]any{"receipts": true})
	if m := find(syncOnce(t, alice, cursorOf(syncOnce(t, alice, 0, 100))-1, 100), mid); m != nil && m["read"] != true {
		t.Errorf("turning receipts back on shows the flag again: %v", m)
	}

	// typing
	ch := a.hub.sub(tid)
	defer a.hub.unsub(tid, ch)
	sawTyping := func() bool {
		select {
		case m := <-ch:
			return strings.Contains(m, `"typing"`)
		case <-time.After(400 * time.Millisecond):
			return false
		}
	}
	bob.must("POST", "/api/m/"+itoa(tid)+"/typing", map[string]any{})
	if !sawTyping() {
		t.Error("typing is shared by default")
	}
	bob.must("POST", "/api/prefs", map[string]any{"typing": false})
	bob.must("POST", "/api/m/"+itoa(tid)+"/typing", map[string]any{})
	if sawTyping() {
		t.Error("typing off means nothing is sent")
	}
	alice.must("POST", "/api/t/"+itoa(tid)+"/typing", map[string]any{})
	if !sawTyping() {
		t.Error("one person's setting must not change the other's")
	}
	if st := alice.must("GET", "/api/t/"+itoa(tid)+"/state", nil); st["online"] != false {
		t.Logf("online: %v", st["online"])
	}
	bob.must("POST", "/api/prefs", map[string]any{"notif_private": false})
	if bob.must("GET", "/api/prefs", nil)["notif_private"] != false {
		t.Error("notification privacy setting should be saved")
	}
	if code, _ := newCl(t, srv.URL).do("GET", "/api/prefs", nil); code != 401 {
		t.Errorf("settings need sign-in: %d", code)
	}
}

func TestPinAndMute(t *testing.T) {
	fast(t)
	a, srv, alice, bob, tid, _, _ := chatPair(t)
	a.push.on = false
	carol := signUp(t, srv, "carol", "third passphrase")
	if code, _ := carol.do("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"pinned": true}); code != 404 {
		t.Errorf("a stranger pinning someone's chat: %d", code)
	}
	if code, _ := bob.do("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{}); code != 400 {
		t.Errorf("nothing to change: %d", code)
	}
	bob.must("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"pinned": true})
	bob.must("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"pinned": true}) // again changes nothing
	bt := list(syncOnce(t, bob, 0, 100), "threads")[0].(map[string]any)
	at := list(syncOnce(t, alice, 0, 100), "threads")[0].(map[string]any)
	if bt["pinned"] != true || bt["muted"] != false || at["pinned"] != false {
		t.Errorf("pin is private to the person who set it: bob %v alice %v", bt, at)
	}

	// mute: no live event for the muted person
	_, login := appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "correct horse"})
	ch, stop := streamLines(t, srv.URL, login["token"].(string), "", nil)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	sendMsg(t, bob, "/api/m/", tid, "private text should never appear in events", nil)
	l, ok := waitLine(ch, "data: ", 3*time.Second)
	if !ok || !strings.Contains(l, `"k":"message"`) {
		t.Fatalf("unmuted chats notify: %q", l)
	}
	if strings.Contains(l, "private text") {
		t.Errorf("events must never carry message text: %s", l)
	}
	alice.must("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"muted": true})
	time.Sleep(300 * time.Millisecond)
	sendMsg(t, bob, "/api/m/", tid, "second", nil)
	if l, ok := waitLine(ch, "data: ", 1200*time.Millisecond); ok {
		t.Errorf("a muted chat must not notify: %s", l)
	}
	if th := list(syncOnce(t, alice, 0, 100), "threads")[0].(map[string]any); th["muted"] != true {
		t.Errorf("mute in sync: %v", th)
	}
	alice.must("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"muted": false})
	time.Sleep(300 * time.Millisecond)
	sendMsg(t, bob, "/api/m/", tid, "third", nil)
	if _, ok := waitLine(ch, "data: ", 3*time.Second); !ok {
		t.Error("unmuting brings notifications back")
	}
	// the other person's mute never affects you
	bob.must("POST", "/api/chats/"+itoa(tid)+"/prefs", map[string]any{"muted": true})
	if th := list(syncOnce(t, alice, 0, 100), "threads")[0].(map[string]any); th["muted"] != false {
		t.Errorf("bob's mute leaked to alice: %v", th)
	}
	_ = strconv.Itoa
}

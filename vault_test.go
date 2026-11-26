package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// blob makes a string with the shape of a sealed key. The server only checks the shape, never the contents.
func blob(prefix string, c byte) string {
	return prefix + ".AAAAAAAAAAAAAAAA." + strings.Repeat(string(rune('A'+c%26)), 44)
}

func vaultBodyFor(seed byte) map[string]any {
	return map[string]any{"iter": 210000, "salt": strings.Repeat("s", 22), "wrapped_pw": blob("k1", seed), "wrapped_rc": blob("k1", seed+1),
		"rc_auth": strings.Repeat(string(rune('a'+seed%26)), 43), "rc_display": blob("k1", seed+2)}
}

func testPub(t *testing.T) string {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func vaultServer(t *testing.T) (*App, *httptest.Server) {
	a := testApp(t)
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return a, srv
}

func signUp(t *testing.T, srv *httptest.Server, user, pass string) *cl {
	c := newCl(t, srv.URL)
	c.must("POST", "/api/register", map[string]string{"username": user, "password": pass})
	return c
}

func newLinkID(t *testing.T, a *App, c *cl, label string) (int64, string) {
	c.must("POST", "/api/links", map[string]string{"label": label, "cid": ""})
	var id int64
	var slug string
	if err := a.db.QueryRow(testCtx(), `SELECT id, slug FROM links WHERE label=$1`, label).Scan(&id, &slug); err != nil {
		t.Fatal(err)
	}
	return id, slug
}

func count(t *testing.T, a *App, sql string, args ...any) int {
	var n int
	if err := a.db.QueryRow(testCtx(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVaultNeedsSignIn(t *testing.T) {
	_, srv := vaultServer(t)
	anon := newCl(t, srv.URL)
	for _, p := range []string{"/api/vault", "/api/vault/status", "/api/vault/keys"} {
		if code, _ := anon.do("GET", p, nil); code != 401 {
			t.Errorf("GET %s signed out: %d", p, code)
		}
	}
	for _, p := range []string{"/api/vault/create", "/api/vault/password", "/api/vault/keys"} {
		if code, _ := anon.do("POST", p, map[string]any{}); code != 401 {
			t.Errorf("POST %s signed out: %d", p, code)
		}
	}
}

func TestVaultCreateAndFetch(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	out := c.must("GET", "/api/vault", nil)
	if out["exists"] != false || out["recommended_iter"] == nil || out["id"] == nil {
		t.Fatalf("empty vault: %v", out)
	}
	body := vaultBodyFor(1)
	c.must("POST", "/api/vault/create", body)
	c.must("POST", "/api/vault/create", body) // the same request again is fine
	if code, _ := c.do("POST", "/api/vault/create", vaultBodyFor(7)); code != 409 {
		t.Errorf("a different vault over an existing one: %d", code)
	}
	out = c.must("GET", "/api/vault", nil)
	kdf, _ := out["kdf"].(map[string]any)
	if out["exists"] != true || out["wrapped_pw"] != body["wrapped_pw"] || out["rc_display"] != body["rc_display"] || kdf["iter"].(float64) != 210000 {
		t.Fatalf("fetched vault: %v", out)
	}
	if _, has := out["wrapped_rc"]; has {
		t.Error("the recovery copy must not come back from a normal fetch")
	}
	if _, has := out["rc_auth_hash"]; has {
		t.Error("the recovery proof must never be returned")
	}
}

func TestVaultRejectsBadInput(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	for name, mut := range map[string]func(map[string]any){
		"low iterations":  func(m map[string]any) { m["iter"] = 1000 },
		"huge iterations": func(m map[string]any) { m["iter"] = 99999999 },
		"short salt":      func(m map[string]any) { m["salt"] = "abc" },
		"plain wrap":      func(m map[string]any) { m["wrapped_pw"] = "just some text" },
		"chat message":    func(m map[string]any) { m["wrapped_rc"] = strings.Replace(m["wrapped_rc"].(string), "k1", "e1", 1) },
		"short proof":     func(m map[string]any) { m["rc_auth"] = "abc" },
		"huge blob":       func(m map[string]any) { m["rc_display"] = "k1.AAAAAAAAAAAAAAAA." + strings.Repeat("A", 3000) },
		"unknown field":   func(m map[string]any) { m["extra"] = 1 },
	} {
		b := vaultBodyFor(1)
		mut(b)
		if code, _ := c.do("POST", "/api/vault/create", b); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if out := c.must("GET", "/api/vault", nil); out["exists"] != false {
		t.Error("a refused request must not leave a vault behind")
	}
}

func TestVaultKeysOwnership(t *testing.T) {
	a, srv := vaultServer(t)
	alice := signUp(t, srv, "alice", "correct horse")
	bob := signUp(t, srv, "bobby", "battery staple")
	aid, _ := newLinkID(t, a, alice, "alice link")
	bid, _ := newLinkID(t, a, bob, "bob link")
	key := func(kind string, ref int64, c byte) map[string]any {
		return map[string]any{"keys": []map[string]any{{"kind": kind, "ref": ref, "blob": blob("k1", c)}}}
	}

	if code, _ := alice.do("POST", "/api/vault/keys", key("link", aid, 1)); code != 409 {
		t.Errorf("keys before a vault exists: %d", code)
	}
	alice.must("POST", "/api/vault/create", vaultBodyFor(1))
	bob.must("POST", "/api/vault/create", vaultBodyFor(9))

	alice.must("POST", "/api/vault/keys", key("link", aid, 3))
	alice.must("POST", "/api/vault/keys", key("link", aid, 4)) // storing again replaces it
	if n := count(t, a, `SELECT count(*) FROM vault_keys WHERE owner_id=(SELECT id FROM owners WHERE username='alice')`); n != 1 {
		t.Fatalf("one link, one key: %d", n)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", key("link", bid, 5)); code != 404 {
		t.Errorf("someone else's link: %d", code)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", key("link", 999999, 5)); code != 404 {
		t.Errorf("a link that doesn't exist must look the same: %d", code)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", key("chat", 1, 5)); code != 404 {
		t.Errorf("a chat that isn't saved to the account: %d", code)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", key("other", 1, 5)); code != 400 {
		t.Errorf("unknown kind: %d", code)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", map[string]any{"keys": []map[string]any{{"kind": "link", "ref": aid, "blob": "plain text"}}}); code != 400 {
		t.Errorf("unsealed key: %d", code)
	}
	if code, _ := alice.do("POST", "/api/vault/keys", map[string]any{"keys": []any{}}); code != 400 {
		t.Errorf("empty batch: %d", code)
	}
	// one bad key in a batch means nothing is stored
	bad := map[string]any{"keys": []map[string]any{{"kind": "link", "ref": aid, "blob": blob("k1", 20)}, {"kind": "link", "ref": bid, "blob": blob("k1", 21)}}}
	if code, _ := alice.do("POST", "/api/vault/keys", bad); code != 404 {
		t.Errorf("mixed batch: %d", code)
	}
	var got string
	a.db.QueryRow(testCtx(), `SELECT blob FROM vault_keys WHERE link_id=$1`, aid).Scan(&got)
	if got != blob("k1", 4) {
		t.Error("a refused batch must not change anything")
	}

	// each account sees only its own
	st := alice.must("GET", "/api/vault/status", nil)
	if l, _ := st["links"].([]any); len(l) != 1 || int64(l[0].(float64)) != aid {
		t.Errorf("alice status: %v", st)
	}
	st = bob.must("GET", "/api/vault/status", nil)
	if l, _ := st["links"].([]any); len(l) != 0 {
		t.Errorf("bob must not see alice's keys: %v", st)
	}
	if st["exists"] != true {
		t.Errorf("bob's vault: %v", st)
	}
	ks := alice.must("GET", "/api/vault/keys", nil)
	if l, _ := ks["keys"].([]any); len(l) != 1 {
		t.Fatalf("alice keys: %v", ks)
	} else if k := l[0].(map[string]any); k["kind"] != "link" || k["blob"] != blob("k1", 4) || int64(k["ref"].(float64)) != aid {
		t.Errorf("alice key: %v", k)
	}
	if ks := bob.must("GET", "/api/vault/keys", nil); len(ks["keys"].([]any)) != 0 {
		t.Errorf("bob keys: %v", ks)
	}

	// deleting the link takes its sealed key with it
	alice.must("POST", "/api/links/"+itoa(aid)+"/delete", map[string]any{})
	if n := count(t, a, `SELECT count(*) FROM vault_keys WHERE link_id=$1`, aid); n != 0 {
		t.Errorf("sealed key left after the link was deleted: %d", n)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestVaultSavedChat(t *testing.T) {
	a, srv := vaultServer(t)
	alice := signUp(t, srv, "alice", "correct horse")
	lid, slug := newLinkID(t, a, alice, "alice link")
	alice.must("POST", "/api/links/"+itoa(lid)+"/key", map[string]string{"pub": testPub(t)})

	guest := newCl(t, srv.URL)
	guest.must("POST", "/api/l/"+slug+"/start", map[string]string{"message": "e1.AAAAAAAAAAAAAAAA." + strings.Repeat("B", 30), "token": randHex(16), "turnstile": "", "pub": testPub(t)})
	var tid int64
	if err := a.db.QueryRow(testCtx(), `SELECT id FROM threads WHERE link_id=$1`, lid).Scan(&tid); err != nil {
		t.Fatal(err)
	}

	bob := guest // the same browser, which now makes an account
	bob.must("POST", "/api/register", map[string]string{"username": "bobby", "password": "battery staple"})

	// no vault yet: the chat must not be saved without its key
	if code, _ := bob.do("POST", "/api/l/"+slug+"/claim", map[string]string{"key": blob("k1", 5)}); code != 409 {
		t.Fatalf("claim with a key but no vault: %d", code)
	}
	if n := count(t, a, `SELECT count(*) FROM threads WHERE id=$1 AND account_id IS NOT NULL`, tid); n != 0 {
		t.Fatal("a refused claim must not attach the chat")
	}
	if code, _ := bob.do("POST", "/api/l/"+slug+"/claim", map[string]string{"key": "plain"}); code != 400 {
		t.Errorf("claim with an unsealed key: %d", code)
	}

	bob.must("POST", "/api/vault/create", vaultBodyFor(2))
	out := bob.must("POST", "/api/l/"+slug+"/claim", map[string]string{"key": blob("k1", 5)})
	if int64(out["id"].(float64)) != tid {
		t.Fatalf("claim should return the chat id: %v", out)
	}
	bob.must("POST", "/api/l/"+slug+"/claim", map[string]string{"key": blob("k1", 6)}) // repeating is harmless
	var got string
	a.db.QueryRow(testCtx(), `SELECT blob FROM vault_keys WHERE thread_id=$1`, tid).Scan(&got)
	if got != blob("k1", 6) || count(t, a, `SELECT count(*) FROM vault_keys WHERE thread_id=$1`, tid) != 1 {
		t.Errorf("sealed chat key: %q", got)
	}
	// a second device of the same account sees the chat is restorable
	dev2 := newCl(t, srv.URL)
	dev2.must("POST", "/api/login", map[string]string{"username": "bobby", "password": "battery staple"})
	st := dev2.must("GET", "/api/vault/status", nil)
	if c, _ := st["chats"].([]any); len(c) != 1 || int64(c[0].(float64)) != tid {
		t.Errorf("second device status: %v", st)
	}

	// the owner of the link has no access to the guest's sealed key
	alice.must("POST", "/api/vault/create", vaultBodyFor(3))
	if code, _ := alice.do("POST", "/api/vault/keys", map[string]any{"keys": []map[string]any{{"kind": "chat", "ref": tid, "blob": blob("k1", 1)}}}); code != 404 {
		t.Errorf("link owner writing a guest's chat key: %d", code)
	}

	// removing the chat from the account removes its sealed key
	bob.must("POST", "/api/m/"+itoa(tid)+"/unclaim", map[string]any{})
	if n := count(t, a, `SELECT count(*) FROM vault_keys WHERE thread_id=$1`, tid); n != 0 {
		t.Errorf("sealed key left after unclaim: %d", n)
	}
}

func TestClaimStillWorksWithoutAKey(t *testing.T) {
	a, srv := vaultServer(t)
	alice := signUp(t, srv, "alice", "correct horse")
	lid, slug := newLinkID(t, a, alice, "alice link")
	guest := newCl(t, srv.URL)
	guest.must("POST", "/api/l/"+slug+"/start", map[string]string{"message": "hello there", "token": randHex(16), "turnstile": "", "pub": ""})
	guest.must("POST", "/api/register", map[string]string{"username": "bobby", "password": "battery staple"})
	guest.must("POST", "/api/l/"+slug+"/claim", map[string]string{})
	if n := count(t, a, `SELECT count(*) FROM threads WHERE link_id=$1 AND account_id IS NOT NULL`, lid); n != 1 {
		t.Errorf("claim without a key: %d", n)
	}
}

func TestVaultPasswordChange(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	c.must("POST", "/api/vault/create", vaultBodyFor(1))
	other := newCl(t, srv.URL)
	other.must("POST", "/api/login", map[string]string{"username": "alice", "password": "correct horse"})

	change := func(cur, next string, ver int, seed byte) (int, map[string]any) {
		return c.do("POST", "/api/vault/password", map[string]any{"current_password": cur, "new_password": next, "version": ver, "iter": 250000, "salt": strings.Repeat("n", 22), "wrapped_pw": blob("k1", seed)})
	}
	if code, _ := change("wrong wrong", "new password 1", 1, 8); code != 401 {
		t.Errorf("wrong current password: %d", code)
	}
	if code, _ := change("correct horse", "short", 1, 8); code != 400 {
		t.Errorf("short new password: %d", code)
	}
	if code, _ := change("correct horse", "new password 1", 5, 8); code != 409 {
		t.Errorf("stale version: %d", code)
	}
	if code, out := change("correct horse", "new password 1", 1, 8); code != 200 {
		t.Fatalf("change: %d %v", code, out)
	}
	if code, _ := change("new password 1", "new password 2", 1, 9); code != 409 {
		t.Errorf("version moved on after a change: %d", code)
	}
	v := c.must("GET", "/api/vault", nil)
	if v["wrapped_pw"] != blob("k1", 8) || v["version"].(float64) != 2 || v["kdf"].(map[string]any)["iter"].(float64) != 250000 {
		t.Errorf("vault after change: %v", v)
	}
	if code, _ := c.do("GET", "/api/me", nil); code != 200 {
		t.Errorf("the device that changed it stays signed in: %d", code)
	}
	if code, _ := other.do("GET", "/api/me", nil); code != 401 {
		t.Errorf("other devices must be signed out: %d", code)
	}
	fresh := newCl(t, srv.URL)
	if code, _ := fresh.do("POST", "/api/login", map[string]string{"username": "alice", "password": "correct horse"}); code != 401 {
		t.Errorf("old password still works: %d", code)
	}
	fresh.must("POST", "/api/login", map[string]string{"username": "alice", "password": "new password 1"})
}

func TestVaultPasswordNeedsAVault(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	code, _ := c.do("POST", "/api/vault/password", map[string]any{"current_password": "correct horse", "new_password": "new password 1", "version": 1, "iter": 250000, "salt": strings.Repeat("n", 22), "wrapped_pw": blob("k1", 8)})
	if code != 409 {
		t.Errorf("password change with no vault: %d", code)
	}
	fresh := newCl(t, srv.URL)
	fresh.must("POST", "/api/login", map[string]string{"username": "alice", "password": "correct horse"}) // nothing changed
}

func TestVaultPasswordLockout(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	c.must("POST", "/api/vault/create", vaultBodyFor(1))
	body := map[string]any{"current_password": "nope nope", "new_password": "new password 1", "version": 1, "iter": 250000, "salt": strings.Repeat("n", 22), "wrapped_pw": blob("k1", 8)}
	last := 0
	for i := 0; i < 6; i++ {
		last, _ = c.do("POST", "/api/vault/password", body)
	}
	if last != 429 {
		t.Errorf("repeated wrong passwords must be throttled: %d", last)
	}
}

func TestVaultRecovery(t *testing.T) {
	a, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	body := vaultBodyFor(1)
	c.must("POST", "/api/vault/create", body)
	rcAuth := body["rc_auth"].(string)
	anon := newCl(t, srv.URL)

	open := func(user, proof string) (int, map[string]any) {
		return anon.do("POST", "/api/vault/recover/open", map[string]string{"username": user, "rc_auth": proof})
	}
	// a wrong code and an unknown account look exactly alike
	c1, o1 := open("alice", strings.Repeat("z", 43))
	c2, o2 := open("nobody", strings.Repeat("z", 43))
	c3, o3 := open("alice", "short")
	if c1 != 401 || c2 != 401 || c3 != 401 || o1["error"] != o2["error"] || o1["error"] != o3["error"] {
		t.Fatalf("wrong code %d %v, unknown account %d %v, malformed %d %v", c1, o1, c2, o2, c3, o3)
	}
	code, out := open("Alice", rcAuth) // the username isn't case sensitive, like sign-in
	if code != 200 || out["wrapped_rc"] != body["wrapped_rc"] {
		t.Fatalf("open with the right code: %d %v", code, out)
	}

	finish := func(proof, pass string, seed byte) (int, map[string]any) {
		return anon.do("POST", "/api/vault/recover/finish", map[string]any{"username": "alice", "rc_auth": proof, "new_password": pass, "iter": 260000,
			"salt": strings.Repeat("r", 22), "wrapped_pw": blob("k1", seed), "wrapped_rc": blob("k1", seed+1), "new_rc_auth": strings.Repeat(string(rune('A'+seed%26)), 43), "rc_display": blob("k1", seed+2)})
	}
	if code, _ := finish(strings.Repeat("z", 43), "brand new pass", 11); code != 401 {
		t.Errorf("finish with a wrong code: %d", code)
	}
	if code, _ := finish(rcAuth, "short", 11); code != 400 {
		t.Errorf("finish with a short password: %d", code)
	}
	if code, out := finish(rcAuth, "brand new pass", 11); code != 200 {
		t.Fatalf("finish: %d %v", code, out)
	}
	if code, _ := finish(rcAuth, "another pass 2", 14); code != 401 {
		t.Errorf("a recovery code must only work once: %d", code)
	}
	if code, _ := open("alice", rcAuth); code != 401 {
		t.Errorf("the old recovery code still opens the vault: %d", code)
	}
	newProof := strings.Repeat(string(rune('A'+11%26)), 43)
	if code, out := open("alice", newProof); code != 200 || out["wrapped_rc"] != blob("k1", 12) {
		t.Errorf("the new recovery code: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/me", nil); code != 401 {
		t.Errorf("recovery must sign every device out: %d", code)
	}
	fresh := newCl(t, srv.URL)
	if code, _ := fresh.do("POST", "/api/login", map[string]string{"username": "alice", "password": "correct horse"}); code != 401 {
		t.Errorf("old password after recovery: %d", code)
	}
	fresh.must("POST", "/api/login", map[string]string{"username": "alice", "password": "brand new pass"})
	v := fresh.must("GET", "/api/vault", nil)
	if v["wrapped_pw"] != blob("k1", 11) || v["rc_display"] != blob("k1", 13) || v["version"].(float64) != 2 {
		t.Errorf("vault after recovery: %v", v)
	}
	if n := count(t, a, `SELECT count(*) FROM audit WHERE action='account recovered'`); n != 1 {
		t.Errorf("recovery should be recorded: %d", n)
	}
}

func TestVaultRecoveryLockout(t *testing.T) {
	_, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	body := vaultBodyFor(1)
	c.must("POST", "/api/vault/create", body)
	anon := newCl(t, srv.URL)
	var last int
	for i := 0; i < 5; i++ {
		last, _ = anon.do("POST", "/api/vault/recover/open", map[string]string{"username": "alice", "rc_auth": strings.Repeat("z", 43)})
	}
	if last != 429 {
		t.Errorf("repeated wrong codes must be throttled: %d", last)
	}
	if code, _ := anon.do("POST", "/api/vault/recover/open", map[string]string{"username": "alice", "rc_auth": body["rc_auth"].(string)}); code != 429 {
		t.Errorf("a locked account/address must wait even for the right code: %d", code)
	}
	// the throttle for recovery doesn't lock the real person out of ordinary sign-in
	c2 := newCl(t, srv.URL)
	c2.must("POST", "/api/login", map[string]string{"username": "alice", "password": "correct horse"})
}

func TestVaultFromTheApp(t *testing.T) {
	_, srv := vaultServer(t)
	signUp(t, srv, "alice", "correct horse")
	_, out := appReq(t, "POST", srv.URL+"/api/app/login", "", defaultAppOrigin, map[string]string{"username": "alice", "password": "correct horse"})
	tok, _ := out["token"].(string)
	resp, got := appReq(t, "GET", srv.URL+"/api/vault", tok, defaultAppOrigin, nil)
	if resp.StatusCode != 200 || got["exists"] != false || resp.Header.Get("Access-Control-Allow-Origin") != defaultAppOrigin {
		t.Fatalf("vault from the app: %d %v %v", resp.StatusCode, got, resp.Header)
	}
	if resp, _ := appReq(t, "POST", srv.URL+"/api/vault/create", tok, defaultAppOrigin, vaultBodyFor(1)); resp.StatusCode != 200 {
		t.Errorf("create from the app: %d", resp.StatusCode)
	}
	if resp, _ := appReq(t, "GET", srv.URL+"/api/vault", "", defaultAppOrigin, nil); resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
}

func TestVaultSurvivesAccountDeletion(t *testing.T) {
	a, srv := vaultServer(t)
	c := signUp(t, srv, "alice", "correct horse")
	c.must("POST", "/api/vault/create", vaultBodyFor(1))
	a.db.Exec(testCtx(), `DELETE FROM owners WHERE username='alice'`)
	if n := count(t, a, `SELECT count(*) FROM vaults`); n != 0 {
		t.Errorf("vault left behind after the account was removed: %d", n)
	}
}

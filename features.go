package main

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	qrcode "github.com/skip2/go-qrcode"
)

var (
	// e2eBody matches an encrypted message or contact: "e1." + 12 byte IV + ciphertext, all base64url.
	e2eBody = regexp.MustCompile(`^e1\.[A-Za-z0-9_-]{16}\.[A-Za-z0-9_-]{22,}$`)
	// hexID matches ids and tokens that the browser generates (idempotency keys, guest tokens).
	hexID  = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
	hex32  = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// validPub checks that s is a real uncompressed P-256 public key, so nobody can store junk as an encryption key.
func validPub(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 65 {
		return false
	}
	_, err = ecdh.P256().NewPublicKey(b)
	return err == nil
}

func (a *App) featureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/links/{id}/edit", a.editLink)
	mux.HandleFunc("POST /api/links/{id}/active", a.setLinkActive)
	mux.HandleFunc("POST /api/links/{id}/key", a.setLinkKey)
	mux.HandleFunc("GET /api/links/{id}/qr.png", a.linkQR)
	mux.HandleFunc("GET /api/blocks", a.blockCount)
	mux.HandleFunc("POST /api/blocks/clear", a.blockClear)
	mux.HandleFunc("GET /r/{slug}/{token}", a.resume)
	mux.HandleFunc("GET /api/chats", a.listChats)
	mux.HandleFunc("GET /api/stats", a.stats)
	mux.HandleFunc("POST /api/l/{slug}/claim", a.claimChat)
	mux.HandleFunc("POST /api/m/{id}/unclaim", a.unclaimChat)
}

func jsonCached(w http.ResponseWriter, v any, secs int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(secs))
	json.NewEncoder(w).Encode(v)
}

// clean removes invalid UTF-8, null bytes, control characters and text-direction overrides from user input.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0xFEFF:
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	s = clean(s)
	if utf8.RuneCountInString(s) > n {
		s = strings.TrimSpace(string([]rune(s)[:n]))
	}
	return s
}

// vidHash returns the hash of the guest's long-lived device cookie, creating it if needed.
func (a *App) vidHash(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("vid"); err == nil && len(c.Value) == 32 {
		return sha256hex(c.Value)
	}
	v := randHex(16)
	a.setCookie(w, "vid", v, 365*86400, false)
	return sha256hex(v)
}

// banned only applies to plain-text chats. End-to-end encrypted messages can't be read by the server, by design.
func (a *App) banned(s string) bool {
	s = strings.ToLower(s)
	split := func(r rune) bool { return r == ',' || r == '\n' }
	for _, w := range strings.FieldsFunc(strings.ToLower(a.set.get("banned_words")), split) {
		if w = strings.TrimSpace(w); w != "" && strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// turnstileOK verifies a Cloudflare Turnstile token on the server. The secret key never reaches the browser.
// With no secret configured it always passes.
func (a *App) turnstileOK(r *http.Request, token string) bool {
	secret := a.set.get("turnstile_secret")
	if secret == "" {
		return true
	}
	if token == "" || len(token) > 4096 {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {clientIP(r)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logErr("turnstile", err)
		return false
	}
	defer resp.Body.Close()
	var out struct{ Success bool }
	json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&out)
	return out.Success
}

func inQuiet(start, end, tz int) bool {
	if start < 0 || end < 0 {
		return true
	}
	h := time.Now().UTC().Add(-time.Duration(tz) * time.Minute).Hour()
	if start <= end {
		return h >= start && h < end
	}
	return h >= start || h < end
}

func (a *App) editLink(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	var in struct {
		Label      string `json:"label"`
		Welcome    string `json:"welcome"`
		About      string `json:"about"`
		Autoreply  string `json:"autoreply"`
		QuietStart int    `json:"quiet_start"`
		QuietEnd   int    `json:"quiet_end"`
		Tz         int    `json:"tz"`
		TTL        int    `json:"ttl_hours"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	label := clip(in.Label, 60)
	if label == "" || in.QuietStart < -1 || in.QuietStart > 23 || in.QuietEnd < -1 || in.QuietEnd > 23 || in.Tz < -840 || in.Tz > 840 || !ttlOK[in.TTL] {
		fail(w, 400, "Check the name, quiet hours and chat lifetime")
		return
	}
	if !a.exec(r.Context(), "edit link", `UPDATE links SET label=$3, welcome=$4, about=$5, autoreply=$6, quiet_start=$7, quiet_end=$8, tz=$9, ttl_hours=$10 WHERE id=$1 AND owner_id=$2`,
		id, oid, label, clip(in.Welcome, 280), clip(in.About, 120), clip(in.Autoreply, 200), in.QuietStart, in.QuietEnd, in.Tz, in.TTL) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.cache.delPrefix("info:")
	jsonOut(w, 200, map[string]any{"ok": true})
}

// setLinkActive sets a link live or paused. It sets an exact state, so repeating the request changes nothing.
func (a *App) setLinkActive(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	var in struct{ On bool }
	if !readJSON(w, r, &in) {
		return
	}
	a.exec(r.Context(), "link active", `UPDATE links SET active=$3 WHERE id=$1 AND owner_id=$2`, id, oid, in.On)
	a.cache.delPrefix("info:")
	jsonOut(w, 200, map[string]any{"ok": true})
}

// setLinkKey publishes the link's public encryption key. The private half never leaves the owner's browser.
// Publishing the same key again succeeds; a different key is refused so a link's key can't be swapped under guests.
func (a *App) setLinkKey(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	var in struct{ Pub string }
	if !readJSON(w, r, &in) {
		return
	}
	if !validPub(in.Pub) {
		fail(w, 400, "That isn't a valid encryption key")
		return
	}
	var slug string
	err = a.db.QueryRow(r.Context(), `UPDATE links SET pubkey=$3 WHERE id=$1 AND owner_id=$2 AND (pubkey='' OR pubkey=$3) RETURNING slug`, id, oid, in.Pub).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 409, "This link already has a different encryption key")
		return
	}
	if err != nil {
		a.fail500(w, "link key", err)
		return
	}
	a.cache.delPrefix("info:")
	jsonOut(w, 200, map[string]any{"ok": true})
}

// linkQR draws the QR code on the server, so no third-party script runs inside the signed-in app.
// High error correction leaves room for the logo that the share card places in the middle.
func (a *App) linkQR(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	var slug string
	if a.db.QueryRow(r.Context(), `SELECT slug FROM links WHERE id=$1 AND owner_id=$2`, id, oid).Scan(&slug) != nil {
		fail(w, 404, "Not found")
		return
	}
	data, err := qrcode.Encode(a.base(r)+"/l/"+slug, qrcode.High, 640)
	if err != nil {
		a.fail500(w, "qr", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}

func (a *App) blockCount(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	var n int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM blocks WHERE owner_id=$1`, oid).Scan(&n); err != nil {
		a.fail500(w, "block count", err)
		return
	}
	jsonOut(w, 200, map[string]any{"count": n})
}

func (a *App) blockClear(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	a.exec(r.Context(), "clear blocks", `DELETE FROM blocks WHERE owner_id=$1`, oid)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// resume restores a guest's chat on a new device or after cookies were cleared.
func (a *App) resume(w http.ResponseWriter, r *http.Request) {
	slug, tok := r.PathValue("slug"), r.PathValue("token")
	if a.rl.allow("res:"+clientIP(r), 30, time.Hour) && hex32.MatchString(tok) {
		var n int
		if a.db.QueryRow(r.Context(), `SELECT 1 FROM threads t JOIN links l ON l.id=t.link_id WHERE l.slug=$1 AND t.token_hash=$2 AND t.expires_at>now()`, slug, sha256hex(tok)).Scan(&n) == nil {
			a.setCookie(w, "v_"+slug, tok, 7*86400, false)
		}
	}
	http.Redirect(w, r, "/l/"+url.PathEscape(slug), http.StatusSeeOther) // the browser keeps any #fragment, which is where the encryption key travels
}

// ---------- chats saved to an account ----------

// accountThread resolves a chat that a signed-in guest saved to their account. They take part as the guest side.
func (a *App) accountThread(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	oid, ok := a.owner(w, r)
	if !ok {
		return 0, "", false
	}
	tid, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if perr != nil {
		fail(w, 404, "Not found")
		return 0, "", false
	}
	var n int
	if err := a.db.QueryRow(r.Context(), `SELECT 1 FROM threads WHERE id=$1 AND account_id=$2 AND expires_at>now()`, tid, oid).Scan(&n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "account thread", err)
		}
		return 0, "", false
	}
	return tid, "v", true
}

// claimChat attaches the caller's guest chat to their account. It is always the guest's own choice, and repeating it is harmless.
func (a *App) claimChat(w http.ResponseWriter, r *http.Request) {
	tid, _, ok := a.visitorThread(w, r)
	if !ok {
		return
	}
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	ct, err := a.db.Exec(r.Context(), `UPDATE threads SET account_id=$2 WHERE id=$1 AND (account_id IS NULL OR account_id=$2) AND link_id NOT IN (SELECT id FROM links WHERE owner_id=$2)`, tid, oid)
	if err != nil {
		a.fail500(w, "claim chat", err)
		return
	}
	if ct.RowsAffected() == 0 {
		fail(w, 409, "This chat can't be saved to your account")
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) unclaimChat(w http.ResponseWriter, r *http.Request) {
	tid, _, ok := a.accountThread(w, r)
	if !ok {
		return
	}
	a.exec(r.Context(), "unclaim chat", `UPDATE threads SET account_id=NULL WHERE id=$1`, tid)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// listChats returns the chats on the caller's links plus the chats they joined and saved, in one query.
// The latest message and the unread count come from indexed lateral joins, not one query per chat.
func (a *App) listChats(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT * FROM (
		SELECT t.id, 'owner' AS role, t.alias AS peer, l.label, l.slug, l.id AS link_id, t.status, t.last_at, t.expires_at, t.e2e, t.pub_v AS gpub, l.pubkey AS lpub,
			COALESCE(lm.body,'') AS preview, COALESCE(lm.side,'') AS last_side, COALESCE(u.n,0) AS unread
		FROM threads t JOIN links l ON l.id=t.link_id
		LEFT JOIN LATERAL (SELECT m.body, m.side FROM messages m WHERE m.thread_id=t.id ORDER BY m.id DESC LIMIT 1) lm ON true
		LEFT JOIN LATERAL (SELECT count(*) AS n FROM messages m WHERE m.thread_id=t.id AND m.side='v' AND m.read_at IS NULL) u ON true
		WHERE l.owner_id=$1 AND t.status<>'blocked' AND t.expires_at>now()
		UNION ALL
		SELECT t.id, 'guest' AS role, CASE WHEN t.owner_alias='' THEN l.label ELSE t.owner_alias END AS peer, l.label, l.slug, l.id AS link_id, t.status, t.last_at, t.expires_at, t.e2e, t.pub_v AS gpub, l.pubkey AS lpub,
			COALESCE(lm.body,'') AS preview, COALESCE(lm.side,'') AS last_side, COALESCE(u.n,0) AS unread
		FROM threads t JOIN links l ON l.id=t.link_id
		LEFT JOIN LATERAL (SELECT m.body, m.side FROM messages m WHERE m.thread_id=t.id ORDER BY m.id DESC LIMIT 1) lm ON true
		LEFT JOIN LATERAL (SELECT count(*) AS n FROM messages m WHERE m.thread_id=t.id AND m.side='o' AND m.read_at IS NULL) u ON true
		WHERE t.account_id=$1 AND t.expires_at>now()
	) c ORDER BY last_at DESC LIMIT 200`, oid)
	if err != nil {
		a.fail500(w, "list chats", err)
		return
	}
	jsonOut(w, 200, out)
}

func (a *App) stats(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	key := "stats:" + strconv.FormatInt(oid, 10)
	if v, hit := a.cache.get(key); hit {
		jsonOut(w, 200, v)
		return
	}
	var chats, links, shared, msgs int
	err := a.db.QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM threads t JOIN links l ON l.id=t.link_id WHERE l.owner_id=$1 AND t.status='open' AND t.expires_at>now()),
		(SELECT count(*) FROM links WHERE owner_id=$1),
		(SELECT count(*) FROM threads t JOIN links l ON l.id=t.link_id WHERE l.owner_id=$1 AND t.revealed_at IS NOT NULL AND t.expires_at>now()),
		(SELECT count(*) FROM messages m JOIN threads t ON t.id=m.thread_id JOIN links l ON l.id=t.link_id WHERE l.owner_id=$1)`, oid).Scan(&chats, &links, &shared, &msgs)
	if err != nil {
		a.fail500(w, "stats", err)
		return
	}
	out := map[string]any{"chats": chats, "links": links, "shared": shared, "messages": msgs}
	a.cache.set(key, out, 10*time.Second)
	jsonOut(w, 200, out)
}

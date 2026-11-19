package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Hub fans chat events out to everyone watching a thread and counts who is connected (for online status).
type Hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan string]bool
	pc   map[int64]map[string]int
}

func newHub() *Hub {
	return &Hub{subs: map[int64]map[chan string]bool{}, pc: map[int64]map[string]int{}}
}

func (h *Hub) sub(id int64) chan string {
	c := make(chan string, 16)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan string]bool{}
	}
	h.subs[id][c] = true
	h.mu.Unlock()
	return c
}

func (h *Hub) unsub(id int64, c chan string) {
	h.mu.Lock()
	delete(h.subs[id], c)
	if len(h.subs[id]) == 0 {
		delete(h.subs, id)
	}
	h.mu.Unlock()
}

func (h *Hub) pub(id int64, v any) {
	b, _ := json.Marshal(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs[id] {
		select {
		case c <- string(b):
		default:
		}
	}
}

func (h *Hub) conns(id int64, side string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pc[id][side]
}

func (h *Hub) join(id int64, side string) {
	h.mu.Lock()
	if h.pc[id] == nil {
		h.pc[id] = map[string]int{}
	}
	h.pc[id][side]++
	h.mu.Unlock()
}

func (h *Hub) part(id int64, side string) {
	h.mu.Lock()
	if h.pc[id] != nil {
		h.pc[id][side]--
		if h.pc[id][side] <= 0 {
			delete(h.pc[id], side)
		}
		if len(h.pc[id]) == 0 {
			delete(h.pc, id)
		}
	}
	h.mu.Unlock()
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) { jsonOut(w, code, map[string]string{"error": msg}) }

// readJSON decodes one JSON object into a typed struct. Unknown fields and trailing data are rejected,
// so a client can never set fields the handler didn't ask for.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.More() {
		fail(w, 400, "That request didn't look right")
		return false
	}
	return true
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	s := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(s))
	jsonOut(w, 429, map[string]any{"error": fmt.Sprintf("Too many attempts. Try again in %d:%02d.", s/60, s%60), "retry_after": s})
}

// rowsOut runs a query and returns rows as maps keyed by column name.
func (a *App) rowsOut(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = v[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

var (
	userRe = regexp.MustCompile(`^[a-z0-9_]{3,24}$`)
	adjs   = []string{"Quiet", "Calm", "Bright", "Swift", "Gentle", "Brave", "Clever", "Lucky", "Misty", "Sunny", "Witty", "Cosmic", "Amber", "Silver", "Velvet", "Wandering", "Hidden", "Mellow", "Nimble", "Polar"}
	birds  = []string{"Fox", "Otter", "Heron", "Panda", "Falcon", "Koala", "Lynx", "Owl", "Seal", "Whale", "Gecko", "Robin", "Badger", "Finch", "Ibis", "Marten", "Newt", "Quail", "Raven", "Stoat"}

	reportCodes = map[string]string{"spam": "Spam or ads", "harassment": "Harassment or abuse", "scam": "Scam or asking for money", "explicit": "Sexual or explicit content", "other": "Something else"}

	// Link lifetimes an owner can pick, and the shorter times either person can pick for one chat. 7 days (168h) is the hard limit.
	ttlOK = map[int]bool{1: true, 6: true, 24: true, 72: true, 168: true}
	retOK = map[int]bool{1: true, 6: true, 24: true, 72: true}
)

// randAlias makes a temporary name. Every new chat gets two fresh ones, one for each person.
func randAlias() string {
	return adjs[mrand.IntN(len(adjs))] + " " + birds[mrand.IntN(len(birds))]
}

const activeLink = `SELECT l.id, l.label, l.welcome, l.about, l.ttl_hours, l.pubkey FROM links l JOIN owners o ON o.id=l.owner_id WHERE l.slug=$1 AND l.active AND NOT o.disabled`

func (a *App) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/public", a.publicInfo)
	mux.HandleFunc("POST /api/register", a.register)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.me)
	mux.HandleFunc("GET /api/links", a.listLinks)
	mux.HandleFunc("POST /api/links", a.createLink)
	mux.HandleFunc("POST /api/links/{id}/delete", a.linkAct(`DELETE FROM links WHERE id=$1 AND owner_id=$2`))
	mux.HandleFunc("POST /api/t/{id}/accept", a.ownerAct("open"))
	mux.HandleFunc("POST /api/t/{id}/block", a.ownerAct("blocked"))
	a.threadRoutes(mux, "/api/t/{id}", a.ownerThread)
	a.threadRoutes(mux, "/api/m/{id}", a.accountThread)
	mux.HandleFunc("GET /api/l/{slug}/info", a.info)
	mux.HandleFunc("POST /api/l/{slug}/start", a.start)
	a.threadRoutes(mux, "/api/l/{slug}", a.visitorThread)
	a.featureRoutes(mux)
	a.pushRoutes(mux)
}

func (a *App) publicInfo(w http.ResponseWriter, r *http.Request) {
	site := ""
	if a.set.get("turnstile_secret") != "" {
		site = a.set.get("turnstile_site") // the public site key only; the secret never leaves the server
	}
	jsonCached(w, map[string]any{"name": a.set.get("site_name"), "tagline": a.set.get("tagline"),
		"announcement": a.set.get("announcement"), "registration_open": a.set.get("registration_open") == "1",
		"base_url": a.cfg.BaseURL, "turnstile_site": site}, 30)
}

// ---------- accounts ----------

func (a *App) owner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id := a.ownerID(r)
	if id == 0 {
		fail(w, 401, "Please sign in")
		return 0, false
	}
	return id, true
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	if a.set.get("registration_open") != "1" {
		fail(w, 403, "Sign-ups are closed right now")
		return
	}
	if !a.rl.allow("reg:"+clientIP(r), 5, time.Hour) {
		fail(w, 429, "Too many attempts. Try again later")
		return
	}
	var in struct{ Username, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	u := strings.ToLower(clean(in.Username))
	if !userRe.MatchString(u) {
		fail(w, 400, "Username: 3-24 letters, digits or _")
		return
	}
	if len(in.Password) < 8 || len(in.Password) > 200 {
		fail(w, 400, "Password needs 8 to 200 characters")
		return
	}
	var id int64
	err := a.db.QueryRow(r.Context(), `INSERT INTO owners(username,pass) VALUES($1,$2) RETURNING id`, u, hashPass(in.Password)).Scan(&id)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		fail(w, 409, "That username is taken")
		return
	}
	if err != nil {
		a.fail500(w, "register", err)
		return
	}
	if err := a.startOwner(w, r, id); err != nil {
		a.fail500(w, "register session", err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password, Turnstile string }
	if !readJSON(w, r, &in) {
		return
	}
	u, ip := strings.ToLower(clean(in.Username)), clientIP(r)
	pair, acct, byIP, wait := a.loginWait(u, ip)
	if wait > 0 {
		tooMany(w, wait)
		return
	}
	// Lots of failures against one account from many places: ask for a human check instead of locking the owner out.
	if a.fails.count(acct) >= 20 && a.set.get("turnstile_secret") != "" && !a.turnstileOK(r, in.Turnstile) {
		jsonOut(w, 403, map[string]any{"error": "Please complete the human check to sign in.", "challenge": true, "site": a.set.get("turnstile_site")})
		return
	}
	var id int64
	var hash string
	var dis bool
	err := a.db.QueryRow(r.Context(), `SELECT id, pass, disabled FROM owners WHERE username=$1`, u).Scan(&id, &hash, &dis)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.fail500(w, "login lookup", err)
		return
	}
	ok := err == nil && !dis
	if ok {
		ok = checkPass(in.Password, hash)
	} else {
		checkPass(in.Password, dummyHash)
	}
	if !ok {
		a.fails.fail(pair, acct, byIP)
		log.Printf("login failed user=%q ip=%s", u, ip)
		if w2 := a.fails.wait(pair, 5); w2 > 0 {
			tooMany(w, w2)
			return
		}
		fail(w, 401, "Wrong username or password")
		return
	}
	a.fails.reset(pair, acct)
	if err := a.startOwner(w, r, id); err != nil {
		a.fail500(w, "login session", err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.endSession(r, "os")
	a.setCookie(w, "os", "", -1, false)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	var u string
	if err := a.db.QueryRow(r.Context(), `SELECT username FROM owners WHERE id=$1`, id).Scan(&u); err != nil {
		a.fail500(w, "me", err)
		return
	}
	jsonOut(w, 200, map[string]any{"username": u})
}

// listLinks joins a per-link chat count in one query instead of counting per row.
func (a *App) listLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT l.id, l.slug, l.label, l.active, l.welcome, l.about, l.autoreply, l.quiet_start, l.quiet_end, l.ttl_hours, l.pubkey, COALESCE(c.n,0) AS chats
		FROM links l
		LEFT JOIN (SELECT t.link_id, count(*) AS n FROM threads t JOIN links x ON x.id=t.link_id WHERE x.owner_id=$1 AND t.expires_at>now() GROUP BY t.link_id) c ON c.link_id=l.id
		WHERE l.owner_id=$1 ORDER BY l.id DESC`, id)
	if err != nil {
		a.fail500(w, "list links", err)
		return
	}
	jsonOut(w, 200, out)
}

// createLink is idempotent: the browser sends a key (cid) with each attempt, so a retry or double tap returns the same link.
func (a *App) createLink(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct{ Label, Cid string }
	if !readJSON(w, r, &in) {
		return
	}
	l := clip(in.Label, 60)
	if l == "" {
		fail(w, 400, "Give the link a short name")
		return
	}
	cid := ""
	if hexID.MatchString(in.Cid) {
		cid = in.Cid
	}
	ctx := r.Context()
	var slug string
	if cid != "" && a.db.QueryRow(ctx, `SELECT slug FROM links WHERE owner_id=$1 AND idem=$2`, id, cid).Scan(&slug) == nil {
		jsonOut(w, 200, map[string]any{"slug": slug})
		return
	}
	var n int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM links WHERE owner_id=$1`, id).Scan(&n); err != nil {
		a.fail500(w, "count links", err)
		return
	}
	if n >= a.set.num("max_links_per_owner") {
		fail(w, 403, "You've reached the link limit")
		return
	}
	err := a.db.QueryRow(ctx, `INSERT INTO links(owner_id,slug,label,idem) VALUES($1,$2,$3,NULLIF($4,'')) ON CONFLICT (owner_id, idem) WHERE idem IS NOT NULL DO NOTHING RETURNING slug`, id, randSlug(8), l, cid).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) { // a parallel duplicate won the race
		err = a.db.QueryRow(ctx, `SELECT slug FROM links WHERE owner_id=$1 AND idem=$2`, id, cid).Scan(&slug)
	}
	if err != nil {
		a.fail500(w, "create link", err)
		return
	}
	jsonOut(w, 200, map[string]any{"slug": slug})
}

func (a *App) linkAct(sql string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oid, ok := a.owner(w, r)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			fail(w, 404, "Not found")
			return
		}
		a.exec(r.Context(), "link action", sql, id, oid) // the owner_id condition in every statement is the ownership check
		a.cache.delPrefix("info:")
		jsonOut(w, 200, map[string]any{"ok": true})
	}
}

func (a *App) ownerAct(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, _, ok := a.ownerThread(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		ct, serr := a.db.Exec(ctx, `UPDATE threads SET status=$2::text WHERE id=$1 AND ($2::text='blocked' OR status='pending')`, tid, to)
		if serr != nil {
			logErr("thread status", serr)
		} else if to == "open" && ct.RowsAffected() > 0 {
			a.notify(tid, "v", "accepted")
		}
		if to == "blocked" {
			a.exec(ctx, "block guest", `INSERT INTO blocks(owner_id, vid_hash) SELECT l.owner_id, t.vid_hash FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1 AND t.vid_hash<>'' ON CONFLICT DO NOTHING`, tid)
		}
		a.hub.pub(tid, map[string]any{"t": "status", "v": to})
		jsonOut(w, 200, map[string]any{"ok": true})
	}
}

// ---------- thread resolution (link owner, guest with a saved chat, or guest with the link cookie) ----------

type resolver func(w http.ResponseWriter, r *http.Request) (int64, string, bool)

func (a *App) ownerThread(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
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
	if err := a.db.QueryRow(r.Context(), `SELECT 1 FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1 AND l.owner_id=$2 AND t.expires_at>now()`, tid, oid).Scan(&n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "owner thread", err)
		}
		return 0, "", false
	}
	return tid, "o", true
}

func (a *App) visitorThread(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	slug := r.PathValue("slug")
	c, cerr := r.Cookie("v_" + slug)
	if cerr != nil {
		fail(w, 404, "No chat here yet")
		return 0, "", false
	}
	var tid int64
	if err := a.db.QueryRow(r.Context(), `SELECT t.id FROM threads t JOIN links l ON l.id=t.link_id WHERE l.slug=$1 AND t.token_hash=$2 AND t.expires_at>now()`, slug, sha256hex(c.Value)).Scan(&tid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "No chat here yet")
		} else {
			a.fail500(w, "visitor thread", err)
		}
		return 0, "", false
	}
	return tid, "v", true
}

func (a *App) threadRoutes(mux *http.ServeMux, prefix string, res resolver) {
	h := func(f func(http.ResponseWriter, *http.Request, int64, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if tid, side, ok := res(w, r); ok {
				f(w, r, tid, side)
			}
		}
	}
	mux.HandleFunc("GET "+prefix+"/state", h(a.state))
	mux.HandleFunc("GET "+prefix+"/stream", h(a.stream))
	mux.HandleFunc("POST "+prefix+"/send", h(a.send))
	mux.HandleFunc("POST "+prefix+"/typing", h(a.typing))
	mux.HandleFunc("POST "+prefix+"/reveal", h(a.reveal))
	mux.HandleFunc("POST "+prefix+"/report", h(a.report))
	mux.HandleFunc("POST "+prefix+"/leave", h(a.leave))
	mux.HandleFunc("POST "+prefix+"/privacy", h(a.setPrivacy))
	mux.HandleFunc("POST "+prefix+"/retention", h(a.setRetention))
}

// ---------- guests ----------

// info is public and hit by every guest who opens a link, so it is cached for a few seconds.
func (a *App) info(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if v, hit := a.cache.get("info:" + slug); hit {
		jsonCached(w, v, 10)
		return
	}
	var id int64
	var label, welcome, about, pub string
	var ttl int
	if err := a.db.QueryRow(r.Context(), activeLink, slug).Scan(&id, &label, &welcome, &about, &ttl, &pub); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "This link isn't active")
		} else {
			a.fail500(w, "link info", err)
		}
		return
	}
	out := map[string]any{"label": label, "welcome": welcome, "about": about, "ttl_hours": ttl, "pub": pub}
	a.cache.set("info:"+slug, out, 10*time.Second)
	jsonCached(w, out, 10)
}

// start creates a chat. It is idempotent: the browser makes the guest token itself, so a retry of the same request
// (lost response, double tap, page reload) finds the chat it already made instead of creating a second one.
func (a *App) start(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.rl.allow("start:"+clientIP(r), 20, time.Hour) {
		fail(w, 429, "Too many new chats. Try again later")
		return
	}
	var in struct{ Message, Turnstile, Token, Pub string }
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	var lid int64
	var label, welcome, about, lkey string
	var ttl int
	if err := a.db.QueryRow(ctx, activeLink, slug).Scan(&lid, &label, &welcome, &about, &ttl, &lkey); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "This link isn't active")
		} else {
			a.fail500(w, "start lookup", err)
		}
		return
	}
	tok := in.Token
	if !hex32.MatchString(tok) {
		tok = randHex(16)
	}
	th := sha256hex(tok)
	var have int
	if a.db.QueryRow(ctx, `SELECT 1 FROM threads WHERE link_id=$1 AND token_hash=$2 AND expires_at>now()`, lid, th).Scan(&have) == nil {
		a.setCookie(w, "v_"+slug, tok, 7*86400, false) // same request again: the chat already exists
		jsonOut(w, 200, map[string]any{"ok": true})
		return
	}
	secure := lkey != ""
	m, pubv := in.Message, ""
	mx := a.set.num("max_msg_len")
	if secure {
		if !validPub(in.Pub) || !e2eBody.MatchString(m) || len(m) > mx*6+128 {
			fail(w, 400, "Please refresh the page and try again.")
			return
		}
		pubv = in.Pub
	} else {
		m = clean(m)
		if m == "" || utf8.RuneCountInString(m) > mx {
			fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
			return
		}
		if a.banned(m) {
			fail(w, 400, "That message isn't allowed here")
			return
		}
	}
	vid := a.vidHash(w, r)
	var bl int
	if a.db.QueryRow(ctx, `SELECT 1 FROM blocks b JOIN links l ON l.owner_id=b.owner_id WHERE l.id=$1 AND b.vid_hash=$2`, lid, vid).Scan(&bl) == nil {
		fail(w, 403, "This chat isn't available")
		return
	}
	if !a.turnstileOK(r, in.Turnstile) {
		fail(w, 400, "Please complete the human check and try again")
		return
	}
	if c, err := r.Cookie("v_" + slug); err == nil && c.Value != tok {
		var n int
		if a.db.QueryRow(ctx, `SELECT 1 FROM threads WHERE link_id=$1 AND token_hash=$2 AND expires_at>now()`, lid, sha256hex(c.Value)).Scan(&n) == nil {
			fail(w, 409, "You already have a chat on this link")
			return
		}
	}
	// Fresh temporary names for both people, and a hard end time taken from the link's lifetime (never more than 7 days).
	al, oal := randAlias(), randAlias()
	for oal == al {
		oal = randAlias()
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "start begin", err)
		return
	}
	defer tx.Rollback(ctx)
	var tid int64
	err = tx.QueryRow(ctx, `INSERT INTO threads(link_id,token_hash,alias,owner_alias,vid_hash,expires_at,e2e,pub_v) SELECT $1,$2,$3,$4,$5, now()+make_interval(hours => LEAST(ttl_hours,168)), $6, $7 FROM links WHERE id=$1 ON CONFLICT (link_id, token_hash) DO NOTHING RETURNING id`, lid, th, al, oal, vid, secure, pubv).Scan(&tid)
	if errors.Is(err, pgx.ErrNoRows) { // a parallel copy of this request created it first
		a.setCookie(w, "v_"+slug, tok, 7*86400, false)
		jsonOut(w, 200, map[string]any{"ok": true})
		return
	}
	if err != nil {
		a.fail500(w, "start thread", err)
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO messages(thread_id,side,body) VALUES($1,'v',$2)`, tid, m); err != nil {
		a.fail500(w, "start message", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail500(w, "start commit", err)
		return
	}
	a.setCookie(w, "v_"+slug, tok, 7*86400, false)
	a.notify(tid, "o", "request")
	jsonOut(w, 200, map[string]any{"ok": true})
}

// ---------- chat (shared by every kind of participant) ----------

func (a *App) isPriv(ctx context.Context, tid int64, side string) bool {
	col := "priv_o"
	if side == "v" {
		col = "priv_v"
	}
	var p bool
	a.db.QueryRow(ctx, `SELECT `+col+` FROM threads WHERE id=$1`, tid).Scan(&p)
	return p
}

// presence tells the other person this side is online, unless this side is in privacy mode.
func (a *App) presence(tid int64, side string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a.hub.pub(tid, map[string]any{"t": "presence", "side": side, "on": a.hub.conns(tid, side) > 0 && !a.isPriv(ctx, tid, side)})
}

// state returns everything a chat screen needs in two queries: the thread with its link, and the recent messages.
func (a *App) state(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	ctx := r.Context()
	var alias, oalias, status, label, slug, lpub, pubv, oc, vc, areply string
	var lid int64
	var e2eOn, oa, va, po, pv bool
	var rev *time.Time
	var exp time.Time
	var acct *int64
	var qs, qe, tz int
	err := a.db.QueryRow(ctx, `SELECT t.alias,t.owner_alias,t.status,l.label,l.slug,l.id,l.pubkey,t.e2e,t.pub_v,t.owner_agreed,t.visitor_agreed,t.owner_contact,t.visitor_contact,t.revealed_at,t.priv_o,t.priv_v,t.expires_at,t.account_id,l.autoreply,l.quiet_start,l.quiet_end,l.tz FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1`, tid).Scan(&alias, &oalias, &status, &label, &slug, &lid, &lpub, &e2eOn, &pubv, &oa, &va, &oc, &vc, &rev, &po, &pv, &exp, &acct, &areply, &qs, &qe, &tz)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "state", err)
		}
		return
	}
	other, peer, you, they, contact, mine, theirs := "v", alias, oa, va, vc, po, pv
	if side == "v" {
		other, peer, you, they, contact, mine, theirs = "o", oalias, va, oa, oc, pv, po
		if peer == "" {
			peer = label
		}
	}
	if n, err := a.db.Exec(ctx, `UPDATE messages SET read_at=now() WHERE thread_id=$1 AND side=$2 AND read_at IS NULL`, tid, other); err == nil && n.RowsAffected() > 0 && !mine {
		a.hub.pub(tid, map[string]any{"t": "read", "s": side}) // read receipts are only sent when this side isn't in privacy mode
	}
	msgs, err := a.rowsOut(ctx, `SELECT id, side, body, created_at AS ts, (read_at IS NOT NULL AND NOT $2::bool) AS is_read FROM (SELECT * FROM messages WHERE thread_id=$1 ORDER BY id DESC LIMIT 200) x ORDER BY id`, tid, theirs)
	if err != nil {
		a.fail500(w, "state messages", err)
		return
	}
	done := rev != nil
	if !done || time.Since(*rev) > 24*time.Hour {
		contact = "" // the other person's details are only ever sent after both agreed, and only for 24 hours
	}
	out := map[string]any{"peer": peer, "label": label, "slug": slug, "link_id": lid, "status": status, "side": side, "messages": msgs, "expires_at": exp,
		"e2e": e2eOn, "gpub": pubv, "lpub": lpub, "privacy": mine, "online": a.hub.conns(tid, other) > 0 && !theirs, "saved": acct != nil,
		"reveal": map[string]any{"you": you, "they": they, "done": done, "contact": contact}}
	if side == "v" {
		if s := r.PathValue("slug"); s != "" {
			if c, err := r.Cookie("v_" + s); err == nil {
				out["resume"] = "/r/" + s + "/" + c.Value
			}
		}
		if status == "pending" && areply != "" && inQuiet(qs, qe, tz) {
			out["auto"] = areply
		}
	}
	jsonOut(w, 200, out)
}

func (a *App) stream(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "Live updates aren't available here")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	ch := a.hub.sub(tid)
	defer a.hub.unsub(tid, ch)
	a.hub.join(tid, side)
	a.presence(tid, side)
	defer func() {
		a.hub.part(tid, side)
		a.presence(tid, side)
	}()
	fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	tk := time.NewTicker(20 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", m)
			fl.Flush()
		case <-tk.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// send is idempotent: the browser attaches a key (cid) to each message, and the database refuses a second
// message with the same key in the same chat, so a retried request never posts twice.
func (a *App) send(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct{ Body, Cid string }
	if !readJSON(w, r, &in) {
		return
	}
	if !a.rl.allow("m:"+strconv.FormatInt(tid, 10)+side, a.set.num("msgs_per_min"), time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	ctx := r.Context()
	var status string
	var secure bool
	if err := a.db.QueryRow(ctx, `SELECT status, e2e FROM threads WHERE id=$1`, tid).Scan(&status, &secure); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "send status", err)
		}
		return
	}
	if status != "open" {
		fail(w, 403, "This chat isn't open")
		return
	}
	b, mx := in.Body, a.set.num("max_msg_len")
	if secure {
		if !e2eBody.MatchString(b) || len(b) > mx*6+128 {
			fail(w, 400, "That message couldn't be sent. Refresh and try again.")
			return
		}
	} else {
		b = clean(b)
		if b == "" || utf8.RuneCountInString(b) > mx {
			fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
			return
		}
		if a.banned(b) {
			fail(w, 400, "That message isn't allowed here")
			return
		}
	}
	cid := ""
	if hexID.MatchString(in.Cid) {
		cid = in.Cid
	}
	var id int64
	var ts time.Time
	err := a.db.QueryRow(ctx, `INSERT INTO messages(thread_id,side,body,cid) VALUES($1,$2,$3,NULLIF($4,'')) ON CONFLICT (thread_id, cid) WHERE cid IS NOT NULL DO NOTHING RETURNING id, created_at`, tid, side, b, cid).Scan(&id, &ts)
	if errors.Is(err, pgx.ErrNoRows) { // the same message was already stored
		jsonOut(w, 200, map[string]any{"ok": true})
		return
	}
	if err != nil {
		a.fail500(w, "send", err)
		return
	}
	a.exec(ctx, "touch thread", `UPDATE threads SET last_at=now() WHERE id=$1`, tid)
	a.hub.pub(tid, map[string]any{"t": "msg", "id": id, "side": side, "body": b, "ts": ts, "cid": cid})
	to := "o"
	if side == "o" {
		to = "v"
	}
	a.notify(tid, to, "message")
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) typing(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if a.rl.allow("ty:"+strconv.FormatInt(tid, 10)+side, 20, 10*time.Second) && !a.isPriv(r.Context(), tid, side) {
		a.hub.pub(tid, map[string]any{"t": "typing", "side": side})
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// setPrivacy turns privacy mode on or off for the caller's side: no typing indicator, read receipts or online status.
func (a *App) setPrivacy(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct{ On bool }
	if !readJSON(w, r, &in) {
		return
	}
	col := "priv_o"
	if side == "v" {
		col = "priv_v"
	}
	if !a.exec(r.Context(), "privacy", `UPDATE threads SET `+col+`=$2 WHERE id=$1`, tid, in.On) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.presence(tid, side)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// setRetention lets either person end the chat sooner. It can only shorten the time left, never extend it.
func (a *App) setRetention(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct{ Hours int }
	if !readJSON(w, r, &in) {
		return
	}
	if !retOK[in.Hours] {
		fail(w, 400, "Pick one of the listed times")
		return
	}
	if !a.exec(r.Context(), "retention", `UPDATE threads SET expires_at=LEAST(expires_at, now()+make_interval(hours => $2::int)) WHERE id=$1`, tid, in.Hours) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.hub.pub(tid, map[string]any{"t": "retention"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

// reveal records this side's choice in ONE statement, so two people agreeing at the same moment can't miss each other,
// and repeating the request changes nothing once the reveal has happened. Contact details of encrypted chats arrive
// already encrypted, so the server only stores and returns ciphertext.
func (a *App) reveal(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct {
		Agree   bool
		Contact string
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	var secure bool
	if err := a.db.QueryRow(ctx, `SELECT e2e FROM threads WHERE id=$1`, tid).Scan(&secure); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "reveal lookup", err)
		}
		return
	}
	// Column names come from these constants only, never from the request.
	col, ccol, other := "owner_agreed", "owner_contact", "visitor_agreed"
	if side == "v" {
		col, ccol, other = "visitor_agreed", "visitor_contact", "owner_agreed"
	}
	var ct pgconn.CommandTag
	var err error
	if in.Agree {
		c := in.Contact
		if secure {
			if !e2eBody.MatchString(c) || len(c) > 600 {
				fail(w, 400, "That contact couldn't be sent. Refresh and try again.")
				return
			}
		} else if c = clip(c, 100); c == "" {
			fail(w, 400, "Enter what you want to share (max 100 characters)")
			return
		}
		ct, err = a.db.Exec(ctx, `UPDATE threads SET `+col+`=CASE WHEN revealed_at IS NULL THEN true ELSE `+col+` END, `+ccol+`=CASE WHEN revealed_at IS NULL THEN $2 ELSE `+ccol+` END, revealed_at=CASE WHEN revealed_at IS NULL AND `+other+` THEN now() ELSE revealed_at END WHERE id=$1 AND status='open'`, tid, c)
	} else {
		ct, err = a.db.Exec(ctx, `UPDATE threads SET `+col+`=false, `+ccol+`='' WHERE id=$1 AND status='open' AND revealed_at IS NULL`, tid)
	}
	if err != nil {
		a.fail500(w, "reveal", err)
		return
	}
	if ct.RowsAffected() == 0 && in.Agree {
		fail(w, 409, "Reveal isn't available right now")
		return
	}
	if in.Agree {
		// The reveal has just completed if its timestamp is brand new. The other person agreed earlier, so tell them.
		var fresh bool
		if a.db.QueryRow(ctx, `SELECT revealed_at > now() - interval '5 seconds' FROM threads WHERE id=$1 AND revealed_at IS NOT NULL`, tid).Scan(&fresh) == nil && fresh {
			to := "o"
			if side == "o" {
				to = "v"
			}
			a.notify(tid, to, "reveal")
		}
	}
	a.hub.pub(tid, map[string]any{"t": "reveal"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

// report is idempotent too (the browser sends a key per report). For end-to-end encrypted chats the reporter's
// browser can attach the recent messages as evidence, because moderators can't read the chat themselves.
func (a *App) report(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if !a.rl.allow("rep:"+clientIP(r), 10, time.Hour) {
		fail(w, 429, "Too many reports. Try again later")
		return
	}
	var in struct{ Code, Note, Evidence, Cid string }
	if !readJSON(w, r, &in) {
		return
	}
	reason, ok := reportCodes[in.Code]
	if !ok {
		fail(w, 400, "Pick a reason")
		return
	}
	if n := clip(in.Note, 300); n != "" {
		reason += ": " + n
	}
	cid := ""
	if hexID.MatchString(in.Cid) {
		cid = in.Cid
	}
	if !a.exec(r.Context(), "report", `INSERT INTO reports(thread_id,side,reason,evidence,cid) VALUES($1,$2,$3,$4,NULLIF($5,'')) ON CONFLICT (thread_id, cid) WHERE cid IS NOT NULL DO NOTHING`, tid, side, reason, clip(in.Evidence, 4000), cid) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// leave is the emergency exit: it ends the conversation straight away for both people, with no questions asked.
func (a *App) leave(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if !a.exec(r.Context(), "leave", `DELETE FROM threads WHERE id=$1`, tid) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.hub.pub(tid, map[string]any{"t": "gone"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

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
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
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

const activeLink = `SELECT l.id, l.label, l.welcome, l.about, l.ttl_hours FROM links l JOIN owners o ON o.id=l.owner_id WHERE l.slug=$1 AND l.active AND NOT o.disabled`

func (a *App) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/public", a.publicInfo)
	mux.HandleFunc("POST /api/register", a.register)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.me)
	mux.HandleFunc("GET /api/links", a.listLinks)
	mux.HandleFunc("POST /api/links", a.createLink)
	mux.HandleFunc("POST /api/links/{id}/toggle", a.linkAct(`UPDATE links SET active = NOT active WHERE id=$1 AND owner_id=$2`))
	mux.HandleFunc("POST /api/links/{id}/delete", a.linkAct(`DELETE FROM links WHERE id=$1 AND owner_id=$2`))
	mux.HandleFunc("POST /api/t/{id}/accept", a.ownerAct("open"))
	mux.HandleFunc("POST /api/t/{id}/block", a.ownerAct("blocked"))
	a.threadRoutes(mux, "/api/t/{id}", a.ownerThread)
	a.threadRoutes(mux, "/api/m/{id}", a.accountThread)
	mux.HandleFunc("GET /api/l/{slug}/info", a.info)
	mux.HandleFunc("POST /api/l/{slug}/start", a.start)
	a.threadRoutes(mux, "/api/l/{slug}", a.visitorThread)
	a.featureRoutes(mux)
}

func (a *App) publicInfo(w http.ResponseWriter, r *http.Request) {
	site := ""
	if a.set.get("turnstile_secret") != "" {
		site = a.set.get("turnstile_site") // the public site key only; the secret never leaves the server
	}
	jsonOut(w, 200, map[string]any{"name": a.set.get("site_name"), "tagline": a.set.get("tagline"),
		"announcement": a.set.get("announcement"), "registration_open": a.set.get("registration_open") == "1",
		"base_url": a.cfg.BaseURL, "turnstile_site": site})
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

func (a *App) listLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT l.id, l.slug, l.label, l.active, l.welcome, l.about, l.autoreply, l.quiet_start, l.quiet_end, l.ttl_hours, (SELECT count(*) FROM threads t WHERE t.link_id=l.id AND t.expires_at>now()) AS chats FROM links l WHERE l.owner_id=$1 ORDER BY l.id DESC`, id)
	if err != nil {
		a.fail500(w, "list links", err)
		return
	}
	jsonOut(w, 200, out)
}

func (a *App) createLink(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct{ Label string }
	if !readJSON(w, r, &in) {
		return
	}
	l := clip(in.Label, 60)
	if l == "" {
		fail(w, 400, "Give the link a short name")
		return
	}
	var n int
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM links WHERE owner_id=$1`, id).Scan(&n); err != nil {
		a.fail500(w, "count links", err)
		return
	}
	if n >= a.set.num("max_links_per_owner") {
		fail(w, 403, "You've reached the link limit")
		return
	}
	slug := randSlug(8)
	if _, err := a.db.Exec(r.Context(), `INSERT INTO links(owner_id,slug,label) VALUES($1,$2,$3)`, id, slug, l); err != nil {
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
		a.exec(ctx, "thread status", `UPDATE threads SET status=$2::text WHERE id=$1 AND ($2::text='blocked' OR status='pending')`, tid, to)
		if to == "blocked" {
			a.exec(ctx, "block visitor", `INSERT INTO blocks(owner_id, vid_hash) SELECT l.owner_id, t.vid_hash FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1 AND t.vid_hash<>'' ON CONFLICT DO NOTHING`, tid)
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

func (a *App) info(w http.ResponseWriter, r *http.Request) {
	var id int64
	var label, welcome, about string
	var ttl int
	if err := a.db.QueryRow(r.Context(), activeLink, r.PathValue("slug")).Scan(&id, &label, &welcome, &about, &ttl); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "This link isn't active")
		} else {
			a.fail500(w, "link info", err)
		}
		return
	}
	jsonOut(w, 200, map[string]any{"label": label, "welcome": welcome, "about": about, "ttl_hours": ttl})
}

func (a *App) start(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.rl.allow("start:"+clientIP(r), 10, time.Hour) {
		fail(w, 429, "Too many new chats. Try again later")
		return
	}
	var in struct{ Message, Turnstile string }
	if !readJSON(w, r, &in) {
		return
	}
	m := clean(in.Message)
	mx := a.set.num("max_msg_len")
	if m == "" || utf8.RuneCountInString(m) > mx {
		fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
		return
	}
	ctx := r.Context()
	var lid int64
	var label, welcome, about string
	var ttl int
	if err := a.db.QueryRow(ctx, activeLink, slug).Scan(&lid, &label, &welcome, &about, &ttl); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "This link isn't active")
		} else {
			a.fail500(w, "start lookup", err)
		}
		return
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
	if a.banned(m) {
		fail(w, 400, "That message isn't allowed here")
		return
	}
	if c, err := r.Cookie("v_" + slug); err == nil {
		var n int
		if a.db.QueryRow(ctx, `SELECT 1 FROM threads WHERE link_id=$1 AND token_hash=$2 AND expires_at>now()`, lid, sha256hex(c.Value)).Scan(&n) == nil {
			fail(w, 409, "You already have a chat on this link")
			return
		}
	}
	tok := randHex(16)
	// Fresh temporary names for both people, and a hard end time taken from the link's lifetime (never more than 7 days).
	al, oal := randAlias(), randAlias()
	for oal == al {
		oal = randAlias()
	}
	var tid int64
	if err := a.db.QueryRow(ctx, `INSERT INTO threads(link_id,token_hash,alias,owner_alias,vid_hash,expires_at) SELECT $1,$2,$3,$4,$5, now()+make_interval(hours => LEAST(ttl_hours,168)) FROM links WHERE id=$1 RETURNING id`, lid, sha256hex(tok), al, oal, vid).Scan(&tid); err != nil {
		a.fail500(w, "start thread", err)
		return
	}
	if !a.exec(ctx, "start message", `INSERT INTO messages(thread_id,side,body) VALUES($1,'v',$2)`, tid, m) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.setCookie(w, "v_"+slug, tok, 7*86400, false)
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

func (a *App) state(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	ctx := r.Context()
	var alias, oalias, status, label, oc, vc string
	var oa, va, po, pv bool
	var rev *time.Time
	var exp time.Time
	var acct *int64
	err := a.db.QueryRow(ctx, `SELECT t.alias,t.owner_alias,t.status,l.label,t.owner_agreed,t.visitor_agreed,t.owner_contact,t.visitor_contact,t.revealed_at,t.priv_o,t.priv_v,t.expires_at,t.account_id FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1`, tid).Scan(&alias, &oalias, &status, &label, &oa, &va, &oc, &vc, &rev, &po, &pv, &exp, &acct)
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
	out := map[string]any{"peer": peer, "label": label, "status": status, "side": side, "messages": msgs, "expires_at": exp,
		"privacy": mine, "online": a.hub.conns(tid, other) > 0 && !theirs, "saved": acct != nil,
		"reveal": map[string]any{"you": you, "they": they, "done": done, "contact": contact}}
	if side == "v" {
		if slug := r.PathValue("slug"); slug != "" {
			if c, err := r.Cookie("v_" + slug); err == nil {
				out["resume"] = "/r/" + slug + "/" + c.Value
			}
		}
		if status == "pending" {
			out["auto"] = a.autoReply(ctx, tid)
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

func (a *App) send(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct{ Body string }
	if !readJSON(w, r, &in) {
		return
	}
	b := clean(in.Body)
	mx := a.set.num("max_msg_len")
	if b == "" || utf8.RuneCountInString(b) > mx {
		fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
		return
	}
	if a.banned(b) {
		fail(w, 400, "That message isn't allowed here")
		return
	}
	if !a.rl.allow("m:"+strconv.FormatInt(tid, 10)+side, a.set.num("msgs_per_min"), time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	ctx := r.Context()
	var status string
	if err := a.db.QueryRow(ctx, `SELECT status FROM threads WHERE id=$1`, tid).Scan(&status); err != nil {
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
	var id int64
	var ts time.Time
	if err := a.db.QueryRow(ctx, `INSERT INTO messages(thread_id,side,body) VALUES($1,$2,$3) RETURNING id, created_at`, tid, side, b).Scan(&id, &ts); err != nil {
		a.fail500(w, "send", err)
		return
	}
	a.exec(ctx, "touch thread", `UPDATE threads SET last_at=now() WHERE id=$1`, tid)
	a.hub.pub(tid, map[string]any{"t": "msg", "id": id, "side": side, "body": b, "ts": ts})
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

func (a *App) reveal(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct {
		Agree   bool
		Contact string
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	var status string
	var rev *time.Time
	if err := a.db.QueryRow(ctx, `SELECT status, revealed_at FROM threads WHERE id=$1`, tid).Scan(&status, &rev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "Not found")
		} else {
			a.fail500(w, "reveal lookup", err)
		}
		return
	}
	if status != "open" || rev != nil {
		fail(w, 409, "Reveal isn't available right now")
		return
	}
	// Column names come from these two constants only, never from the request.
	col, ccol := "owner_agreed", "owner_contact"
	if side == "v" {
		col, ccol = "visitor_agreed", "visitor_contact"
	}
	if in.Agree {
		c := clip(in.Contact, 100)
		if c == "" {
			fail(w, 400, "Enter what you want to share (max 100 characters)")
			return
		}
		ok := a.exec(ctx, "reveal agree", `UPDATE threads SET `+col+`=true, `+ccol+`=$2 WHERE id=$1`, tid, c)
		ok = ok && a.exec(ctx, "reveal complete", `UPDATE threads SET revealed_at=now() WHERE id=$1 AND owner_agreed AND visitor_agreed`, tid)
		if !ok {
			fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
			return
		}
	} else if !a.exec(ctx, "reveal withdraw", `UPDATE threads SET `+col+`=false, `+ccol+`='' WHERE id=$1`, tid) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.hub.pub(tid, map[string]any{"t": "reveal"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) report(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if !a.rl.allow("rep:"+clientIP(r), 5, time.Hour) {
		fail(w, 429, "Too many reports. Try again later")
		return
	}
	var in struct{ Code, Note string }
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
	if !a.exec(r.Context(), "report", `INSERT INTO reports(thread_id,side,reason) VALUES($1,$2,$3)`, tid, side, reason) {
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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// Hub fans chat events out to everyone watching a thread.
type Hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan string]bool
}

func newHub() *Hub { return &Hub{subs: map[int64]map[chan string]bool{}} }

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

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) { jsonOut(w, code, map[string]string{"error": msg}) }

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if json.NewDecoder(r.Body).Decode(v) != nil {
		fail(w, 400, "Invalid request")
		return false
	}
	return true
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
	userRe  = regexp.MustCompile(`^[a-z0-9_]{3,24}$`)
	adjs    = []string{"Quiet", "Calm", "Bright", "Swift", "Gentle", "Brave", "Clever", "Lucky", "Misty", "Sunny", "Witty", "Cosmic"}
	animals = []string{"Fox", "Otter", "Heron", "Panda", "Falcon", "Koala", "Lynx", "Owl", "Seal", "Whale", "Gecko", "Robin"}
)

func randAlias() string {
	return adjs[mrand.IntN(len(adjs))] + " " + animals[mrand.IntN(len(animals))]
}

const activeLink = `SELECT l.id, l.label FROM links l JOIN owners o ON o.id=l.owner_id WHERE l.slug=$1 AND l.active AND NOT o.disabled`

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
	mux.HandleFunc("GET /api/threads", a.listThreads)
	mux.HandleFunc("POST /api/t/{id}/accept", a.ownerAct("open"))
	mux.HandleFunc("POST /api/t/{id}/block", a.ownerAct("blocked"))
	a.threadRoutes(mux, "/api/t/{id}", a.ownerThread)
	mux.HandleFunc("GET /api/l/{slug}/info", a.info)
	mux.HandleFunc("POST /api/l/{slug}/start", a.start)
	a.threadRoutes(mux, "/api/l/{slug}", a.visitorThread)
}

func (a *App) publicInfo(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"name": a.set.get("site_name"), "tagline": a.set.get("tagline"),
		"announcement": a.set.get("announcement"), "registration_open": a.set.get("registration_open") == "1", "base_url": a.cfg.BaseURL})
}

// ---------- owners ----------

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
		fail(w, 403, "Sign-ups are closed")
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
	u := strings.ToLower(strings.TrimSpace(in.Username))
	if !userRe.MatchString(u) {
		fail(w, 400, "Username: 3-24 letters, digits or _")
		return
	}
	if len(in.Password) < 8 {
		fail(w, 400, "Password needs 8 or more characters")
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
		fail(w, 500, "Server error")
		return
	}
	a.startOwner(w, id)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.rl.allow("login:"+clientIP(r), 10, 10*time.Minute) {
		fail(w, 429, "Too many attempts. Try again later")
		return
	}
	var in struct{ Username, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	var id int64
	var hash string
	var dis bool
	err := a.db.QueryRow(r.Context(), `SELECT id, pass, disabled FROM owners WHERE username=$1`, strings.ToLower(strings.TrimSpace(in.Username))).Scan(&id, &hash, &dis)
	if err != nil || dis || !checkPass(in.Password, hash) {
		fail(w, 401, "Wrong username or password")
		return
	}
	a.startOwner(w, id)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.setCookie(w, "os", "", -1, false)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	var u string
	a.db.QueryRow(r.Context(), `SELECT username FROM owners WHERE id=$1`, id).Scan(&u)
	jsonOut(w, 200, map[string]any{"username": u})
}

func (a *App) listLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT l.id, l.slug, l.label, l.active, (SELECT count(*) FROM threads t WHERE t.link_id=l.id) AS chats FROM links l WHERE l.owner_id=$1 ORDER BY l.id DESC`, id)
	if err != nil {
		fail(w, 500, "Server error")
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
	l := strings.TrimSpace(in.Label)
	if l == "" || utf8.RuneCountInString(l) > 60 {
		fail(w, 400, "Give the link a short label (max 60 characters)")
		return
	}
	var n int
	a.db.QueryRow(r.Context(), `SELECT count(*) FROM links WHERE owner_id=$1`, id).Scan(&n)
	if n >= a.set.num("max_links_per_owner") {
		fail(w, 403, "You've reached the link limit")
		return
	}
	slug := randSlug(8)
	if _, err := a.db.Exec(r.Context(), `INSERT INTO links(owner_id,slug,label) VALUES($1,$2,$3)`, id, slug, l); err != nil {
		fail(w, 500, "Server error")
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
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		a.db.Exec(r.Context(), sql, id, oid)
		jsonOut(w, 200, map[string]any{"ok": true})
	}
}

func (a *App) listThreads(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT t.id, t.alias, t.status, l.label, t.last_at,
		COALESCE((SELECT body FROM messages m WHERE m.thread_id=t.id ORDER BY m.id DESC LIMIT 1),'') AS preview,
		(SELECT count(*) FROM messages m WHERE m.thread_id=t.id AND m.side='v' AND m.read_at IS NULL) AS unread
		FROM threads t JOIN links l ON l.id=t.link_id WHERE l.owner_id=$1 AND t.status<>'blocked' ORDER BY t.last_at DESC LIMIT 200`, id)
	if err != nil {
		fail(w, 500, "Server error")
		return
	}
	jsonOut(w, 200, out)
}

func (a *App) ownerAct(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, _, ok := a.ownerThread(w, r)
		if !ok {
			return
		}
		a.db.Exec(r.Context(), `UPDATE threads SET status=$2::text WHERE id=$1 AND ($2::text='blocked' OR status='pending')`, tid, to)
		a.hub.pub(tid, map[string]any{"t": "status", "v": to})
		jsonOut(w, 200, map[string]any{"ok": true})
	}
}

// ---------- thread resolution (owner or visitor) ----------

type resolver func(w http.ResponseWriter, r *http.Request) (int64, string, bool)

func (a *App) ownerThread(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	oid, ok := a.owner(w, r)
	if !ok {
		return 0, "", false
	}
	tid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var n int
	if err != nil || a.db.QueryRow(r.Context(), `SELECT 1 FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1 AND l.owner_id=$2`, tid, oid).Scan(&n) != nil {
		fail(w, 404, "Not found")
		return 0, "", false
	}
	return tid, "o", true
}

func (a *App) visitorThread(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	slug := r.PathValue("slug")
	c, err := r.Cookie("v_" + slug)
	var tid int64
	if err != nil || a.db.QueryRow(r.Context(), `SELECT t.id FROM threads t JOIN links l ON l.id=t.link_id WHERE l.slug=$1 AND t.token_hash=$2`, slug, sha256hex(c.Value)).Scan(&tid) != nil {
		fail(w, 404, "No chat here yet")
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
}

// ---------- visitors ----------

func (a *App) info(w http.ResponseWriter, r *http.Request) {
	var id int64
	var label string
	if a.db.QueryRow(r.Context(), activeLink, r.PathValue("slug")).Scan(&id, &label) != nil {
		fail(w, 404, "This link isn't active")
		return
	}
	jsonOut(w, 200, map[string]any{"label": label})
}

func (a *App) start(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !a.rl.allow("start:"+clientIP(r), 10, time.Hour) {
		fail(w, 429, "Too many new chats. Try again later")
		return
	}
	var in struct{ Message string }
	if !readJSON(w, r, &in) {
		return
	}
	m := strings.TrimSpace(in.Message)
	mx := a.set.num("max_msg_len")
	if m == "" || utf8.RuneCountInString(m) > mx {
		fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
		return
	}
	var lid int64
	var label string
	if a.db.QueryRow(r.Context(), activeLink, slug).Scan(&lid, &label) != nil {
		fail(w, 404, "This link isn't active")
		return
	}
	if c, err := r.Cookie("v_" + slug); err == nil {
		var n int
		if a.db.QueryRow(r.Context(), `SELECT 1 FROM threads WHERE link_id=$1 AND token_hash=$2`, lid, sha256hex(c.Value)).Scan(&n) == nil {
			fail(w, 409, "You already have a chat on this link")
			return
		}
	}
	tok := randHex(16)
	var tid int64
	if a.db.QueryRow(r.Context(), `INSERT INTO threads(link_id,token_hash,alias) VALUES($1,$2,$3) RETURNING id`, lid, sha256hex(tok), randAlias()).Scan(&tid) != nil {
		fail(w, 500, "Server error")
		return
	}
	a.db.Exec(r.Context(), `INSERT INTO messages(thread_id,side,body) VALUES($1,'v',$2)`, tid, m)
	a.setCookie(w, "v_"+slug, tok, 30*86400, false)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// ---------- chat (shared by owner and visitor) ----------

func (a *App) state(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	ctx := r.Context()
	var alias, status, label, oc, vc string
	var oa, va bool
	var rev *time.Time
	err := a.db.QueryRow(ctx, `SELECT t.alias,t.status,l.label,t.owner_agreed,t.visitor_agreed,t.owner_contact,t.visitor_contact,t.revealed_at FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1`, tid).Scan(&alias, &status, &label, &oa, &va, &oc, &vc, &rev)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	other, peer, you, they, contact := "v", alias, oa, va, vc
	if side == "v" {
		other, peer, you, they, contact = "o", label, va, oa, oc
	}
	if n, _ := a.db.Exec(ctx, `UPDATE messages SET read_at=now() WHERE thread_id=$1 AND side=$2 AND read_at IS NULL`, tid, other); n.RowsAffected() > 0 {
		a.hub.pub(tid, map[string]any{"t": "read", "s": side})
	}
	msgs, _ := a.rowsOut(ctx, `SELECT id, side, body, created_at AS ts, read_at IS NOT NULL AS is_read FROM (SELECT * FROM messages WHERE thread_id=$1 ORDER BY id DESC LIMIT 200) x ORDER BY id`, tid)
	if msgs == nil {
		msgs = []map[string]any{}
	}
	done := rev != nil
	if !done || time.Since(*rev) > 24*time.Hour {
		contact = ""
	}
	jsonOut(w, 200, map[string]any{"peer": peer, "label": label, "status": status, "side": side, "messages": msgs,
		"reveal": map[string]any{"you": you, "they": they, "done": done, "contact": contact}})
}

func (a *App) stream(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "Streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	ch := a.hub.sub(tid)
	defer a.hub.unsub(tid, ch)
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
	b := strings.TrimSpace(in.Body)
	mx := a.set.num("max_msg_len")
	if b == "" || utf8.RuneCountInString(b) > mx {
		fail(w, 400, fmt.Sprintf("Messages must be 1-%d characters", mx))
		return
	}
	if !a.rl.allow("m:"+strconv.FormatInt(tid, 10)+side, a.set.num("msgs_per_min"), time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	ctx := r.Context()
	var status string
	if a.db.QueryRow(ctx, `SELECT status FROM threads WHERE id=$1`, tid).Scan(&status) != nil {
		fail(w, 404, "Not found")
		return
	}
	if status != "open" {
		fail(w, 403, "This chat isn't open")
		return
	}
	var id int64
	var ts time.Time
	if a.db.QueryRow(ctx, `INSERT INTO messages(thread_id,side,body) VALUES($1,$2,$3) RETURNING id, created_at`, tid, side, b).Scan(&id, &ts) != nil {
		fail(w, 500, "Server error")
		return
	}
	a.db.Exec(ctx, `UPDATE threads SET last_at=now() WHERE id=$1`, tid)
	a.hub.pub(tid, map[string]any{"t": "msg", "id": id, "side": side, "body": b, "ts": ts})
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) typing(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if a.rl.allow("ty:"+strconv.FormatInt(tid, 10)+side, 20, 10*time.Second) {
		a.hub.pub(tid, map[string]any{"t": "typing", "side": side})
	}
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
	if a.db.QueryRow(ctx, `SELECT status, revealed_at FROM threads WHERE id=$1`, tid).Scan(&status, &rev) != nil {
		fail(w, 404, "Not found")
		return
	}
	if status != "open" || rev != nil {
		fail(w, 409, "Reveal isn't available right now")
		return
	}
	col, ccol := "owner_agreed", "owner_contact"
	if side == "v" {
		col, ccol = "visitor_agreed", "visitor_contact"
	}
	if in.Agree {
		c := strings.TrimSpace(in.Contact)
		if c == "" || utf8.RuneCountInString(c) > 100 {
			fail(w, 400, "Enter what you want to share (max 100 characters)")
			return
		}
		a.db.Exec(ctx, `UPDATE threads SET `+col+`=true, `+ccol+`=$2 WHERE id=$1`, tid, c)
		a.db.Exec(ctx, `UPDATE threads SET revealed_at=now() WHERE id=$1 AND owner_agreed AND visitor_agreed`, tid)
	} else {
		a.db.Exec(ctx, `UPDATE threads SET `+col+`=false, `+ccol+`='' WHERE id=$1`, tid)
	}
	a.hub.pub(tid, map[string]any{"t": "reveal"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) report(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	if !a.rl.allow("rep:"+clientIP(r), 5, time.Hour) {
		fail(w, 429, "Too many reports. Try again later")
		return
	}
	var in struct{ Reason string }
	if !readJSON(w, r, &in) {
		return
	}
	re := []rune(strings.TrimSpace(in.Reason))
	if len(re) == 0 {
		fail(w, 400, "Tell us what happened")
		return
	}
	if len(re) > 300 {
		re = re[:300]
	}
	a.db.Exec(r.Context(), `INSERT INTO reports(thread_id,side,reason) VALUES($1,$2,$3)`, tid, side, string(re))
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) leave(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	a.db.Exec(r.Context(), `DELETE FROM threads WHERE id=$1`, tid)
	a.hub.pub(tid, map[string]any{"t": "gone"})
	jsonOut(w, 200, map[string]any{"ok": true})
}

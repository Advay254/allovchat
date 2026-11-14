package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) featureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/links/{id}/edit", a.editLink)
	mux.HandleFunc("GET /api/blocks", a.blockCount)
	mux.HandleFunc("POST /api/blocks/clear", a.blockClear)
	mux.HandleFunc("GET /r/{slug}/{token}", a.resume)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// vidHash returns the hash of the visitor's long-lived device cookie, creating it if needed.
func (a *App) vidHash(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("vid"); err == nil && len(c.Value) == 32 {
		return sha256hex(c.Value)
	}
	v := randHex(16)
	a.setCookie(w, "vid", v, 365*86400, false)
	return sha256hex(v)
}

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

// turnstileOK verifies a Cloudflare Turnstile token. With no secret configured it always passes.
func (a *App) turnstileOK(r *http.Request, token string) bool {
	secret := a.set.get("turnstile_secret")
	if secret == "" {
		return true
	}
	if token == "" {
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
		return false
	}
	defer resp.Body.Close()
	var out struct{ Success bool }
	json.NewDecoder(resp.Body).Decode(&out)
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

// autoReply returns the link's auto-reply text when it applies right now.
func (a *App) autoReply(ctx context.Context, tid int64) string {
	var t string
	var qs, qe, tz int
	if a.db.QueryRow(ctx, `SELECT l.autoreply,l.quiet_start,l.quiet_end,l.tz FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$1`, tid).Scan(&t, &qs, &qe, &tz) != nil || t == "" {
		return ""
	}
	if inQuiet(qs, qe, tz) {
		return t
	}
	return ""
}

func (a *App) editLink(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Label      string `json:"label"`
		Welcome    string `json:"welcome"`
		About      string `json:"about"`
		Autoreply  string `json:"autoreply"`
		QuietStart int    `json:"quiet_start"`
		QuietEnd   int    `json:"quiet_end"`
		Tz         int    `json:"tz"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	label := clip(in.Label, 60)
	if label == "" || in.QuietStart < -1 || in.QuietStart > 23 || in.QuietEnd < -1 || in.QuietEnd > 23 || in.Tz < -840 || in.Tz > 840 {
		fail(w, 400, "Check the label and quiet hours")
		return
	}
	a.db.Exec(r.Context(), `UPDATE links SET label=$3, welcome=$4, about=$5, autoreply=$6, quiet_start=$7, quiet_end=$8, tz=$9 WHERE id=$1 AND owner_id=$2`,
		id, oid, label, clip(in.Welcome, 280), clip(in.About, 120), clip(in.Autoreply, 200), in.QuietStart, in.QuietEnd, in.Tz)
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) blockCount(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	var n int
	a.db.QueryRow(r.Context(), `SELECT count(*) FROM blocks WHERE owner_id=$1`, oid).Scan(&n)
	jsonOut(w, 200, map[string]any{"count": n})
}

func (a *App) blockClear(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	a.db.Exec(r.Context(), `DELETE FROM blocks WHERE owner_id=$1`, oid)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// resume restores a visitor's chat on a new device or after cookies were cleared.
func (a *App) resume(w http.ResponseWriter, r *http.Request) {
	slug, tok := r.PathValue("slug"), r.PathValue("token")
	if a.rl.allow("res:"+clientIP(r), 30, time.Hour) {
		var n int
		if a.db.QueryRow(r.Context(), `SELECT 1 FROM threads t JOIN links l ON l.id=t.link_id WHERE l.slug=$1 AND t.token_hash=$2`, slug, sha256hex(tok)).Scan(&n) == nil {
			a.setCookie(w, "v_"+slug, tok, 30*86400, false)
		}
	}
	http.Redirect(w, r, "/l/"+url.PathEscape(slug), http.StatusSeeOther)
}

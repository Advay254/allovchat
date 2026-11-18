package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Small public endpoints: the privacy and terms pages, security.txt, the limits the chat screen needs,
// the link-preview page for chat links, and account deletion.

func (a *App) publicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /privacy", a.privacy)
	mux.HandleFunc("GET /terms", a.terms)
	mux.HandleFunc("GET /api/limits", a.limits)
	mux.HandleFunc("GET /.well-known/security.txt", a.securityTxt)
	mux.HandleFunc("POST /api/account/delete", a.deleteAccount)
}

func (a *App) privacy(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/privacy", "Privacy notice | "+site, "What "+site+" stores, why, for how long, and the controls you have. Messages are end-to-end encrypted and every chat is temporary.")
	a.render(w, "privacy", d)
}

func (a *App) terms(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/terms", "Terms of use | "+site, "The terms for using "+site+": who can use it, what is not allowed, and how reports and enforcement work.")
	a.render(w, "terms", d)
}

// chatPage serves the guest chat screen. It stays out of search results (header and meta tag) but is open to link-preview
// crawlers, and its preview is generic so it never reveals the link's name.
func (a *App) chatPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	a.page("chat.html")(w, r)
}

// limits tells the chat screen how long a message may be and which words it must not send.
// Messages are encrypted in the browser, so the word filter runs there before a message is sealed.
func (a *App) limits(w http.ResponseWriter, r *http.Request) {
	words := []string{}
	for _, f := range strings.FieldsFunc(strings.ToLower(a.set.get("banned_words")), func(c rune) bool { return c == ',' || c == '\n' }) {
		if f = strings.TrimSpace(f); f != "" {
			words = append(words, f)
		}
	}
	jsonCached(w, map[string]any{"max_msg_len": a.set.num("max_msg_len"), "blocked_words": words}, 30)
}

// securityTxt follows RFC 9116. The expiry rolls forward with every request, so it never goes stale.
func (a *App) securityTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	exp := time.Now().UTC().AddDate(0, 6, 0).Format("2006-01-02T15:04:05.000Z")
	fmt.Fprintf(w, "Contact: https://github.com/Advay254/allovchat/security/advisories/new\nExpires: %s\nPreferred-Languages: en\nCanonical: %s/.well-known/security.txt\nPolicy: https://github.com/Advay254/allovchat/blob/main/SECURITY.md\n", exp, a.base(r))
}

// deleteAccount removes an account for good. It needs the signed-in session, the password and the typed word DELETE;
// the app asks for a second confirmation before it ever calls this. The foreign keys remove the account's links,
// the chats on them, its sessions and its block list in the same statement.
func (a *App) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.rl.allow("acd:"+strconv.FormatInt(id, 10), 5, time.Hour) || !a.rl.allow("acd-ip:"+clientIP(r), 10, time.Hour) {
		fail(w, http.StatusTooManyRequests, "Too many attempts. Try again in a while.")
		return
	}
	if strings.TrimSpace(in.Confirm) != "DELETE" {
		fail(w, http.StatusBadRequest, "Type DELETE to confirm")
		return
	}
	ctx := r.Context()
	var hash string
	if a.db.QueryRow(ctx, `SELECT pass FROM owners WHERE id=$1`, id).Scan(&hash) != nil {
		fail(w, http.StatusNotFound, "Account not found")
		return
	}
	if !checkPass(in.Password, hash) {
		fail(w, http.StatusForbidden, "That password isn't right")
		return
	}
	// Chats on this account's links end for everyone, so tell any open screens.
	var gone []int64
	if rows, err := a.db.Query(ctx, `SELECT t.id FROM threads t JOIN links l ON l.id=t.link_id WHERE l.owner_id=$1`, id); err == nil {
		for rows.Next() {
			var t int64
			if rows.Scan(&t) == nil {
				gone = append(gone, t)
			}
		}
		rows.Close()
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM owners WHERE id=$1`, id); err != nil {
		a.fail500(w, "delete account", err)
		return
	}
	for _, t := range gone {
		a.hub.pub(t, map[string]any{"t": "gone"})
	}
	a.cache.delPrefix("s:")
	a.cache.delPrefix("info:")
	a.setCookie(w, "os", "", -1, false)
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

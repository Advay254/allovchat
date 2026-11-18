package main

import (
	"fmt"
	"net/http"
)

// Small public endpoints: the privacy and terms pages, security.txt, and the limits the chat screen needs.

func (a *App) publicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /privacy", a.privacy)
	mux.HandleFunc("GET /terms", a.terms)
	mux.HandleFunc("GET /api/limits", a.limits)
	mux.HandleFunc("GET /.well-known/security.txt", a.securityTxt)
}

func (a *App) privacy(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/privacy", "Privacy | "+site, "What "+site+" stores, how long it keeps it, and who can see it.")
	a.render(w, "privacy", d)
}

func (a *App) terms(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/terms", "Terms of use | "+site, "The rules for using "+site+", in plain language.")
	a.render(w, "terms", d)
}

// limits tells the chat screen how long a message may be, so it can stop typing at the limit instead of failing after sending.
func (a *App) limits(w http.ResponseWriter, r *http.Request) {
	jsonCached(w, map[string]any{"max_msg_len": a.set.num("max_msg_len")}, 30)
}

// securityTxt follows RFC 9116. Renew the Expires date when you review SECURITY.md.
func (a *App) securityTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Contact: https://github.com/Advay254/allovchat/security/advisories/new\nExpires: 2027-11-18T00:00:00.000Z\nPreferred-Languages: en\nCanonical: %s/.well-known/security.txt\nPolicy: https://github.com/Advay254/allovchat/blob/main/SECURITY.md\n", a.base(r))
}

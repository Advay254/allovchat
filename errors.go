package main

import (
	"bytes"
	"context"
	"html"
	"log"
	"net/http"
	"strings"
)

// logErr writes the real error to the server log and returns a short reference id
// that can be shown to the user so a log line is easy to find.
func logErr(op string, err error) string {
	ref := randHex(3)
	log.Printf("ERR %s ref=%s: %v", op, ref, err)
	return ref
}

func (a *App) fail500(w http.ResponseWriter, op string, err error) {
	ref := logErr(op, err)
	fail(w, http.StatusInternalServerError, "Something went wrong on our side. Please try again in a moment. (ref "+ref+")")
}

// exec runs a write and logs any failure. Callers that must react to failure use the return value.
func (a *App) exec(ctx context.Context, op, sql string, args ...any) bool {
	if _, err := a.db.Exec(ctx, sql, args...); err != nil {
		logErr(op, err)
		return false
	}
	return true
}

// nfw holds back plain-text 404s so the designed 404 page can replace them.
type nfw struct {
	http.ResponseWriter
	r         *http.Request
	swallowed bool
}

func (w *nfw) WriteHeader(code int) {
	if code == http.StatusNotFound && !strings.HasPrefix(w.r.URL.Path, "/api/") && strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		w.swallowed = true
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *nfw) Write(b []byte) (int, error) {
	if w.swallowed {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *nfw) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok && !w.swallowed {
		f.Flush()
	}
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusNotFound, "Not found")
		return
	}
	d := a.pd(r, "/", "Page not found | "+a.set.get("site_name"), "This page doesn't exist.")
	d.Noindex = true
	t, err := loadTpl("notfound")
	if err == nil {
		var buf bytes.Buffer
		if err = t.ExecuteTemplate(&buf, "layout.html", d); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			w.Write(buf.Bytes())
			return
		}
	}
	logErr("404 page", err)
	http.Error(w, "Page not found", http.StatusNotFound)
}

func (a *App) errorPage(w http.ResponseWriter, r *http.Request, code int, msg, ref string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if ref != "" {
			msg += " (ref " + ref + ")"
		}
		fail(w, code, msg)
		return
	}
	note := ""
	if ref != "" {
		note = `<p class="fine">Reference: ` + html.EscapeString(ref) + `</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Something went wrong</title><link rel="stylesheet" href="/static/style.css"><link rel="stylesheet" href="/static/pages.css"></head><body class="maint"><main class="maint-card"><h1>Something went wrong</h1><p>` + html.EscapeString(msg) + `</p>` + note + `<a class="btn" href="/">Back to home</a></main></body></html>`))
}

func (a *App) maintenance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "300")
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusServiceUnavailable, "The site is down for maintenance. Please try again soon.")
		return
	}
	b, err := staticFS.ReadFile("static/maintenance.html")
	if err != nil {
		http.Error(w, "Down for maintenance. Back soon.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write(bytes.ReplaceAll(b, []byte("{{SITE}}"), []byte(html.EscapeString(a.set.get("site_name")))))
}

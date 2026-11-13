package main

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type stat struct {
	L string
	V int
}

type setRow struct {
	Key, Label, Kind, Val string
	On                    bool
}

type adminData struct {
	Path, Tab, Err, Flash string
	Login                 bool
	Tabs                  []string
	Stats                 []stat
	Rows                  []map[string]any
	Set                   []setRow
}

var adminTabs = []string{"overview", "owners", "links", "reports", "settings"}

func actForm(p, tab, action string, id any, label string, danger bool) template.HTML {
	confirm, cls := "", ""
	if danger {
		confirm, cls = ` onsubmit="return confirm('Are you sure?')"`, " d"
	}
	return template.HTML(fmt.Sprintf(`<form method="post" action="%s/act" style="display:inline"%s><input type="hidden" name="a" value="%s"><input type="hidden" name="id" value="%v"><input type="hidden" name="tab" value="%s"><button class="b%s">%s</button></form>`, p, confirm, action, id, tab, cls, label))
}

var adminTpl = template.Must(template.New("admin").Funcs(template.FuncMap{"act": actForm}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Admin</title>
<style>body{font:15px system-ui,sans-serif;margin:0;background:#f6f5fb;color:#1b1530}main{max-width:980px;margin:0 auto;padding:16px}nav{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0 0 16px}nav a,.b{padding:8px 12px;border-radius:8px;border:1px solid #d8d2ee;background:#fff;color:#1b1530;text-decoration:none;font:inherit;cursor:pointer}nav a.on{background:#6d4aff;color:#fff;border-color:#6d4aff}.d{color:#a32d2d}table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #e4dff5}td,th{padding:8px 10px;border-bottom:1px solid #eee9fa;text-align:left;vertical-align:top}.g{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px}.s{background:#fff;border:1px solid #e4dff5;border-radius:10px;padding:14px;margin-bottom:10px}.s b{display:block;font-size:26px}input[type=text],input[type=password],input[type=number]{width:100%;padding:9px;border:1px solid #cfc7ee;border-radius:8px;font:inherit}label{display:block;margin:12px 0 4px;font-weight:500}pre{white-space:pre-wrap;background:#f6f5fb;padding:8px;border-radius:8px}.err{color:#a32d2d}</style></head><body><main>
{{if .Login}}<form method="post" action="{{.Path}}/login" style="max-width:340px;margin:12vh auto"><h2>Admin sign in</h2>{{if .Err}}<p class="err">{{.Err}}</p>{{end}}<label>Username</label><input type="text" name="u" autocomplete="username" required><label>Password</label><input type="password" name="p" autocomplete="current-password" required><p><button class="b">Sign in</button></p></form>
{{else}}<nav>{{range .Tabs}}<a href="{{$.Path}}/?tab={{.}}" class="{{if eq . $.Tab}}on{{end}}">{{.}}</a>{{end}}<form method="post" action="{{.Path}}/logout" style="margin-left:auto"><button class="b">Sign out</button></form></nav>{{if .Flash}}<p>{{.Flash}}</p>{{end}}
{{if eq .Tab "overview"}}<div class="g">{{range .Stats}}<div class="s">{{.L}}<b>{{.V}}</b></div>{{end}}</div>
{{else if eq .Tab "owners"}}<table><tr><th>ID</th><th>Username</th><th>Links</th><th>Joined</th><th></th></tr>{{range .Rows}}<tr><td>{{.id}}</td><td>{{.username}}{{if .disabled}} (disabled){{end}}</td><td>{{.links}}</td><td>{{.joined}}</td><td>{{if .disabled}}{{act $.Path "owners" "owner_toggle" .id "Enable" false}}{{else}}{{act $.Path "owners" "owner_toggle" .id "Disable" false}}{{end}} {{act $.Path "owners" "owner_delete" .id "Delete" true}}</td></tr>{{end}}</table>
{{else if eq .Tab "links"}}<table><tr><th>ID</th><th>Slug</th><th>Label</th><th>Owner</th><th>Chats</th><th></th></tr>{{range .Rows}}<tr><td>{{.id}}</td><td>{{.slug}}</td><td>{{.label}}{{if not .active}} (paused){{end}}</td><td>{{.username}}</td><td>{{.chats}}</td><td>{{if .active}}{{act $.Path "links" "link_toggle" .id "Pause" false}}{{else}}{{act $.Path "links" "link_toggle" .id "Resume" false}}{{end}} {{act $.Path "links" "link_delete" .id "Delete" true}}</td></tr>{{end}}</table>
{{else if eq .Tab "reports"}}{{range .Rows}}<div class="s"><b style="font-size:16px">#{{.id}} · chat {{.thread_id}} · by {{if eq .side "o"}}owner{{else}}visitor{{end}} · {{.ts}}{{if .resolved}} · resolved{{end}}</b><p>{{.reason}}</p><pre>{{range .lines}}{{.}}
{{end}}</pre>{{if not .resolved}}{{act $.Path "reports" "report_resolve" .id "Resolve" false}} {{act $.Path "reports" "report_block" .id "Block chat" false}} {{act $.Path "reports" "report_delete" .id "Delete chat" true}}{{end}}</div>{{else}}<p>No reports.</p>{{end}}
{{else}}<form method="post" action="{{.Path}}/settings">{{range .Set}}<label>{{.Label}}</label>{{if eq .Kind "bool"}}<input type="checkbox" name="{{.Key}}" value="1"{{if .On}} checked{{end}}>{{else if eq .Kind "num"}}<input type="number" name="{{.Key}}" value="{{.Val}}">{{else}}<input type="text" name="{{.Key}}" value="{{.Val}}">{{end}}{{end}}<p><button class="b">Save settings</button></p></form>{{end}}
{{end}}</main></body></html>`))

func (a *App) adminPage(w http.ResponseWriter, code int, d adminData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	adminTpl.Execute(w, d)
}

func inTabs(t string) bool {
	for _, x := range adminTabs {
		if x == t {
			return true
		}
	}
	return false
}

func (a *App) adminRoot(w http.ResponseWriter, r *http.Request) {
	ap := "/" + a.cfg.AdminPath
	rel := strings.TrimPrefix(r.URL.Path, ap)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost && rel == "/login" {
		if !a.rl.allow("adm:"+clientIP(r), 8, 10*time.Minute) {
			http.Error(w, "Too many attempts", http.StatusTooManyRequests)
			return
		}
		r.ParseForm()
		if same(r.FormValue("u"), a.cfg.AdminUser) && same(r.FormValue("p"), a.cfg.AdminPass) {
			exp := time.Now().Add(12 * time.Hour).Unix()
			a.setCookie(w, "as", a.sign("a:"+strconv.FormatInt(exp, 10)), 12*3600, true)
			http.Redirect(w, r, ap+"/", http.StatusSeeOther)
			return
		}
		a.adminPage(w, http.StatusUnauthorized, adminData{Path: ap, Login: true, Err: "Wrong username or password"})
		return
	}
	if !a.adminOK(r) {
		a.adminPage(w, http.StatusOK, adminData{Path: ap, Login: true})
		return
	}
	switch {
	case r.Method == http.MethodPost && rel == "/logout":
		a.setCookie(w, "as", "", -1, true)
		http.Redirect(w, r, ap+"/", http.StatusSeeOther)
	case r.Method == http.MethodPost && rel == "/act":
		a.adminAct(w, r)
	case r.Method == http.MethodPost && rel == "/settings":
		a.adminSettings(w, r)
	case rel == "/":
		a.adminView(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) adminView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := adminData{Path: "/" + a.cfg.AdminPath, Tab: r.URL.Query().Get("tab"), Tabs: adminTabs}
	if !inTabs(d.Tab) {
		d.Tab = "overview"
	}
	if r.URL.Query().Get("ok") == "1" {
		d.Flash = "Saved."
	}
	var err error
	switch d.Tab {
	case "overview":
		var o, l, t, m, rp, m24 int
		err = a.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM owners),(SELECT count(*) FROM links),(SELECT count(*) FROM threads),(SELECT count(*) FROM messages),(SELECT count(*) FROM reports WHERE NOT resolved),(SELECT count(*) FROM messages WHERE created_at > now() - interval '24 hours')`).Scan(&o, &l, &t, &m, &rp, &m24)
		d.Stats = []stat{{"Owners", o}, {"Links", l}, {"Chats", t}, {"Messages", m}, {"Open reports", rp}, {"Messages (24h)", m24}}
	case "owners":
		d.Rows, err = a.rowsOut(ctx, `SELECT o.id, o.username, o.disabled, to_char(o.created_at,'YYYY-MM-DD') AS joined, (SELECT count(*) FROM links l WHERE l.owner_id=o.id) AS links FROM owners o ORDER BY o.id DESC LIMIT 200`)
	case "links":
		d.Rows, err = a.rowsOut(ctx, `SELECT l.id, l.slug, l.label, l.active, o.username, (SELECT count(*) FROM threads t WHERE t.link_id=l.id) AS chats FROM links l JOIN owners o ON o.id=l.owner_id ORDER BY l.id DESC LIMIT 200`)
	case "reports":
		d.Rows, err = a.rowsOut(ctx, `SELECT r.id, r.thread_id, r.side, r.reason, r.resolved, to_char(r.created_at,'YYYY-MM-DD HH24:MI') AS ts FROM reports r ORDER BY r.resolved, r.id DESC LIMIT 50`)
		for _, row := range d.Rows {
			lines, _ := a.rowsOut(ctx, `SELECT side || ': ' || body AS line FROM (SELECT * FROM messages WHERE thread_id=$1 ORDER BY id DESC LIMIT 8) x ORDER BY id`, row["thread_id"])
			ls := []string{}
			for _, l := range lines {
				ls = append(ls, fmt.Sprint(l["line"]))
			}
			row["lines"] = ls
		}
	case "settings":
		for _, m := range settingMeta {
			v := a.set.get(m.Key)
			d.Set = append(d.Set, setRow{m.Key, m.Label, m.Kind, v, v == "1"})
		}
	}
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	a.adminPage(w, http.StatusOK, d)
}

func (a *App) adminAct(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	switch r.FormValue("a") {
	case "owner_toggle":
		a.db.Exec(ctx, `UPDATE owners SET disabled = NOT disabled WHERE id=$1`, id)
	case "owner_delete":
		a.db.Exec(ctx, `DELETE FROM owners WHERE id=$1`, id)
	case "link_toggle":
		a.db.Exec(ctx, `UPDATE links SET active = NOT active WHERE id=$1`, id)
	case "link_delete":
		a.db.Exec(ctx, `DELETE FROM links WHERE id=$1`, id)
	case "report_resolve":
		a.db.Exec(ctx, `UPDATE reports SET resolved=true WHERE id=$1`, id)
	case "report_block":
		var tid int64
		if a.db.QueryRow(ctx, `UPDATE reports SET resolved=true WHERE id=$1 RETURNING thread_id`, id).Scan(&tid) == nil {
			a.db.Exec(ctx, `UPDATE threads SET status='blocked' WHERE id=$1`, tid)
			a.hub.pub(tid, map[string]any{"t": "status", "v": "blocked"})
		}
	case "report_delete":
		var tid int64
		if a.db.QueryRow(ctx, `SELECT thread_id FROM reports WHERE id=$1`, id).Scan(&tid) == nil {
			a.db.Exec(ctx, `DELETE FROM threads WHERE id=$1`, tid)
			a.hub.pub(tid, map[string]any{"t": "gone"})
		}
	}
	tab := r.FormValue("tab")
	if !inTabs(tab) {
		tab = "overview"
	}
	http.Redirect(w, r, "/"+a.cfg.AdminPath+"/?tab="+url.QueryEscape(tab), http.StatusSeeOther)
}

func (a *App) adminSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ctx := r.Context()
	for _, m := range settingMeta {
		v := strings.TrimSpace(r.FormValue(m.Key))
		switch m.Kind {
		case "bool":
			if v != "1" {
				v = "0"
			}
		case "num":
			n, err := strconv.Atoi(v)
			lim := limits[m.Key]
			if err != nil || n < lim[0] {
				n = lim[0]
			}
			if n > lim[1] {
				n = lim[1]
			}
			v = strconv.Itoa(n)
		default:
			if rs := []rune(v); len(rs) > 200 {
				v = string(rs[:200])
			}
			if m.Key == "site_name" && v == "" {
				v = defaults["site_name"]
			}
		}
		a.saveSetting(ctx, m.Key, v)
	}
	a.loadSettings(ctx)
	http.Redirect(w, r, "/"+a.cfg.AdminPath+"/?tab=settings&ok=1", http.StatusSeeOther)
}

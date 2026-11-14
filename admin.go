package main

import (
	"encoding/csv"
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
	ChatsSVG, MsgsSVG     template.HTML
}

var adminTabs = []string{"overview", "owners", "links", "reports", "audit", "settings"}

func actForm(p, tab, action string, id any, label string, danger bool) template.HTML {
	confirm, cls := "", ""
	if danger {
		confirm, cls = ` onsubmit="return confirm('Are you sure?')"`, " d"
	}
	return template.HTML(fmt.Sprintf(`<form method="post" action="%s/act" style="display:inline"%s><input type="hidden" name="a" value="%s"><input type="hidden" name="id" value="%v"><input type="hidden" name="tab" value="%s"><button class="b%s">%s</button></form>`, p, confirm, action, id, tab, cls, label))
}

func barSVG(label string, days []string, vals []int, color string) template.HTML {
	n := len(vals)
	if n == 0 {
		return ""
	}
	mx := 1
	for _, v := range vals {
		if v > mx {
			mx = v
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 560 140" width="100%%" role="img" aria-label="%s">`, label)
	bw := 560 / n
	for i, v := range vals {
		h := v * 100 / mx
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="3" fill="%s"><title>%s: %d</title></rect><text x="%d" y="132" font-size="10" text-anchor="middle" fill="#5b5675">%s</text>`,
			i*bw+4, 108-h, bw-8, h, color, days[i], v, i*bw+bw/2, days[i][3:])
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

var adminTpl = template.Must(template.New("admin").Funcs(template.FuncMap{"act": actForm}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Dashboard</title>
<style>body{font:15px system-ui,sans-serif;margin:0;background:#f6f5fb;color:#1b1530}main{max-width:980px;margin:0 auto;padding:16px}nav{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin:0 0 16px}nav a,.b{padding:8px 12px;border-radius:8px;border:1px solid #d8d2ee;background:#fff;color:#1b1530;text-decoration:none;font:inherit;cursor:pointer;display:inline-block}nav a.on{background:#6d4aff;color:#fff;border-color:#6d4aff}.d{color:#a32d2d}table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #e4dff5}td,th{padding:8px 10px;border-bottom:1px solid #eee9fa;text-align:left;vertical-align:top}.g{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin-bottom:10px}.s{background:#fff;border:1px solid #e4dff5;border-radius:10px;padding:14px;margin-bottom:10px}.s b{display:block;font-size:26px}input[type=text],input[type=password],input[type=number],textarea{width:100%;padding:9px;border:1px solid #cfc7ee;border-radius:8px;font:inherit}label{display:block;margin:12px 0 4px;font-weight:500}pre{white-space:pre-wrap;background:#f6f5fb;padding:8px;border-radius:8px}.err{color:#a32d2d}a:focus-visible,button:focus-visible,input:focus-visible,textarea:focus-visible{outline:2px solid #6d4aff;outline-offset:2px}</style></head><body><main>
{{if .Login}}<form method="post" action="{{.Path}}/login" style="max-width:340px;margin:12vh auto"><h2>Sign in</h2>{{if .Err}}<p class="err" role="alert">{{.Err}}</p>{{end}}<label for="u">Username</label><input id="u" type="text" name="u" autocomplete="username" required><label for="p">Password</label><input id="p" type="password" name="p" autocomplete="current-password" required><p><button class="b">Sign in</button></p></form>
{{else}}<nav aria-label="Dashboard sections">{{range .Tabs}}<a href="{{$.Path}}/?tab={{.}}" class="{{if eq . $.Tab}}on{{end}}"{{if eq . $.Tab}} aria-current="page"{{end}}>{{.}}</a>{{end}}<form method="post" action="{{.Path}}/logout" style="margin-left:auto"><button class="b">Sign out</button></form></nav>{{if .Flash}}<p role="status">{{.Flash}}</p>{{end}}
{{if eq .Tab "overview"}}<div class="g">{{range .Stats}}<div class="s">{{.L}}<b>{{.V}}</b></div>{{end}}</div><div class="s"><b style="font-size:16px">Chats started per day</b>{{.ChatsSVG}}</div><div class="s"><b style="font-size:16px">Messages per day</b>{{.MsgsSVG}}</div>
{{else if eq .Tab "owners"}}<p><a class="b" href="{{.Path}}/export?kind=owners">Export CSV</a></p><table><tr><th>ID</th><th>Username</th><th>Links</th><th>Joined</th><th></th></tr>{{range .Rows}}<tr><td>{{.id}}</td><td>{{.username}}{{if .disabled}} (disabled){{end}}</td><td>{{.links}}</td><td>{{.joined}}</td><td>{{if .disabled}}{{act $.Path "owners" "owner_toggle" .id "Enable" false}}{{else}}{{act $.Path "owners" "owner_toggle" .id "Disable" false}}{{end}} {{act $.Path "owners" "owner_delete" .id "Delete" true}}</td></tr>{{end}}</table>
{{else if eq .Tab "links"}}<table><tr><th>ID</th><th>Slug</th><th>Label</th><th>Owner</th><th>Chats</th><th></th></tr>{{range .Rows}}<tr><td>{{.id}}</td><td>{{.slug}}</td><td>{{.label}}{{if not .active}} (paused){{end}}</td><td>{{.username}}</td><td>{{.chats}}</td><td>{{if .active}}{{act $.Path "links" "link_toggle" .id "Pause" false}}{{else}}{{act $.Path "links" "link_toggle" .id "Resume" false}}{{end}} {{act $.Path "links" "link_delete" .id "Delete" true}}</td></tr>{{end}}</table>
{{else if eq .Tab "reports"}}<p><a class="b" href="{{.Path}}/export?kind=reports">Export CSV</a></p>{{range .Rows}}<div class="s"><b style="font-size:16px">#{{.id}} · chat {{.thread_id}} · reported by {{if eq .side "o"}}the owner{{else}}the visitor{{end}} · {{.ts}}{{if .resolved}} · resolved{{end}}</b><p>{{.reason}}</p><pre>{{range .lines}}{{.}}
{{end}}</pre>{{if not .resolved}}{{act $.Path "reports" "report_resolve" .id "Dismiss" false}} {{act $.Path "reports" "report_block" .id "Close chat" false}} {{act $.Path "reports" "report_delete" .id "Delete chat" true}}{{end}}</div>{{else}}<p>No reports.</p>{{end}}
{{else if eq .Tab "audit"}}<table><tr><th>When</th><th>Action</th><th>Detail</th></tr>{{range .Rows}}<tr><td>{{.ts}}</td><td>{{.action}}</td><td>{{.detail}}</td></tr>{{else}}<tr><td colspan="3">Nothing logged yet.</td></tr>{{end}}</table>
{{else}}<form method="post" action="{{.Path}}/settings">{{range .Set}}<label for="{{.Key}}">{{.Label}}</label>{{if eq .Kind "bool"}}<input id="{{.Key}}" type="checkbox" name="{{.Key}}" value="1"{{if .On}} checked{{end}}>{{else if eq .Kind "num"}}<input id="{{.Key}}" type="number" name="{{.Key}}" value="{{.Val}}">{{else if eq .Kind "area"}}<textarea id="{{.Key}}" name="{{.Key}}" rows="4">{{.Val}}</textarea>{{else if eq .Kind "secret"}}<input id="{{.Key}}" type="password" name="{{.Key}}" autocomplete="off" placeholder="{{if .On}}Saved. Leave blank to keep it{{end}}">{{else}}<input id="{{.Key}}" type="text" name="{{.Key}}" value="{{.Val}}">{{end}}{{end}}<p><button class="b">Save settings</button></p></form>{{end}}
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
	case r.Method == http.MethodGet && rel == "/export":
		a.adminExport(w, r)
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
		days, cs, ms := []string{}, []int{}, []int{}
		if rows, qerr := a.rowsOut(ctx, `SELECT to_char(d,'MM-DD') AS day, (SELECT count(*) FROM threads t WHERE t.created_at::date = d::date) AS chats, (SELECT count(*) FROM messages m WHERE m.created_at::date = d::date) AS msgs FROM generate_series((current_date - 13)::timestamp, current_date::timestamp, interval '1 day') AS d ORDER BY d`); qerr == nil {
			for _, row := range rows {
				c, _ := row["chats"].(int64)
				mm, _ := row["msgs"].(int64)
				days = append(days, fmt.Sprint(row["day"]))
				cs = append(cs, int(c))
				ms = append(ms, int(mm))
			}
		}
		d.ChatsSVG = barSVG("Chats started per day, last 14 days", days, cs, "#6d4aff")
		d.MsgsSVG = barSVG("Messages per day, last 14 days", days, ms, "#19b386")
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
	case "audit":
		d.Rows, err = a.rowsOut(ctx, `SELECT to_char(created_at,'YYYY-MM-DD HH24:MI') AS ts, action, detail FROM audit ORDER BY id DESC LIMIT 100`)
	case "settings":
		for _, m := range settingMeta {
			v := a.set.get(m.Key)
			row := setRow{Key: m.Key, Label: m.Label, Kind: m.Kind, Val: v, On: v == "1"}
			if m.Kind == "secret" {
				row.Val, row.On = "", v != ""
			}
			d.Set = append(d.Set, row)
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
	act := r.FormValue("a")
	switch act {
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
	default:
		act = ""
	}
	if act != "" {
		a.audit(ctx, act, "id "+strconv.FormatInt(id, 10))
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
		case "secret":
			if v == "" {
				continue
			}
		default:
			max := 200
			if m.Kind == "area" {
				max = 2000
			}
			if rs := []rune(v); len(rs) > max {
				v = string(rs[:max])
			}
			if m.Key == "site_name" && v == "" {
				v = defaults["site_name"]
			}
		}
		a.saveSetting(ctx, m.Key, v)
	}
	a.loadSettings(ctx)
	a.audit(ctx, "settings_saved", "")
	http.Redirect(w, r, "/"+a.cfg.AdminPath+"/?tab=settings&ok=1", http.StatusSeeOther)
}

func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (a *App) adminExport(w http.ResponseWriter, r *http.Request) {
	var name, q string
	switch r.URL.Query().Get("kind") {
	case "owners":
		name, q = "owners.csv", `SELECT o.id, o.username, o.disabled, to_char(o.created_at,'YYYY-MM-DD') AS joined, (SELECT count(*) FROM links l WHERE l.owner_id=o.id) AS links FROM owners o ORDER BY o.id`
	case "reports":
		name, q = "reports.csv", `SELECT r.id, r.thread_id, r.side, r.reason, r.resolved, to_char(r.created_at,'YYYY-MM-DD HH24:MI') AS created FROM reports r ORDER BY r.id`
	default:
		http.NotFound(w, r)
		return
	}
	rows, err := a.db.Query(r.Context(), q)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	cw := csv.NewWriter(w)
	var hdr []string
	for _, f := range rows.FieldDescriptions() {
		hdr = append(hdr, f.Name)
	}
	cw.Write(hdr)
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			break
		}
		rec := make([]string, len(v))
		for i, x := range v {
			rec[i] = csvSafe(fmt.Sprint(x))
		}
		cw.Write(rec)
	}
	cw.Flush()
}

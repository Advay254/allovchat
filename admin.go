package main

import (
	"crypto/hmac"
	"encoding/csv"
	"fmt"
	"html"
	"html/template"
	"log"
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

type adminTab struct{ Key, Label string }

type setRow struct {
	Key, Label, Kind, Val, Help string
	On                          bool
}

type setGroup struct {
	Title string
	Rows  []setRow
}

type adminData struct {
	Path, Tab, Err, Flash, CSRF, Heading, Sub string
	Login                                     bool
	Wait, OpenReports                         int
	Tabs                                      []adminTab
	Stats                                     []stat
	Rows                                      []map[string]any
	Groups                                    []setGroup
	ChatsSVG, MsgsSVG                         template.HTML
}

var adminTabs = []adminTab{{"overview", "Overview"}, {"owners", "Owners"}, {"links", "Links"}, {"reports", "Reports"}, {"audit", "Activity log"}, {"settings", "Settings"}}

var tabText = map[string][2]string{
	"overview": {"Overview", "How the site is doing right now."},
	"owners":   {"Owners", "Everyone who has an account. Disable or delete accounts here."},
	"links":    {"Links", "Every chat link on the site."},
	"reports":  {"Reports", "Chats that people reported. Encrypted chats are only visible if the reporter chose to attach messages."},
	"audit":    {"Activity log", "What has been changed from this dashboard. Kept for 180 days."},
	"settings": {"Settings", "Changes apply straight away. Nothing here needs a redeploy."},
}

var settingInfo = map[string]string{
	"site_name":           "Shown in the app, page titles and search results.",
	"tagline":             "One line under the logo and in link previews.",
	"announcement":        "A banner at the top of the app and landing page. Leave empty for none.",
	"registration_open":   "Turn off to stop new owner accounts from being created.",
	"maintenance":         "Shows a maintenance page to everyone except you. This dashboard keeps working.",
	"max_msg_len":         "Longest message anyone can send, in characters.",
	"retention_days":      "Chats with no activity for this many days are deleted automatically. Every chat ends within 7 days regardless.",
	"max_links_per_owner": "How many links one owner can have at a time.",
	"msgs_per_min":        "How fast one person can send messages in a chat.",
	"banned_words":        "Plain-text messages containing any of these are rejected. Encrypted chats can't be filtered. One per line or separated by commas.",
	"turnstile_site":      "The public site key from Cloudflare Turnstile. Leave empty to turn the human check off.",
	"turnstile_secret":    "The secret key from Cloudflare Turnstile. It stays on the server and is never shown again.",
}

var settingGroups = []struct {
	Title string
	Keys  []string
}{
	{"General", []string{"site_name", "tagline", "announcement"}},
	{"Access", []string{"registration_open", "maintenance"}},
	{"Limits", []string{"max_msg_len", "retention_days", "max_links_per_owner", "msgs_per_min"}},
	{"Safety", []string{"banned_words", "turnstile_site", "turnstile_secret"}},
}

var auditLabels = map[string]string{
	"owner_set": "Owner enabled or disabled", "owner_delete": "Owner deleted", "link_set": "Link paused or resumed", "link_delete": "Link deleted",
	"report_resolve": "Report marked reviewed", "report_block": "Chat closed from a report", "report_delete": "Chat deleted from a report",
	"settings_saved": "Settings saved", "admin_login": "Signed in",
}

// actForm renders one small POST form. The CSRF token and every value are written by the server.
// Actions that switch something on or off carry the exact state to set (val), so submitting twice never flips it back.
func actForm(p, csrf, tab, action string, id any, label, val, confirm string, danger bool) template.HTML {
	data, cls := "", "btn soft sm"
	if confirm != "" {
		data = ` data-title="` + html.EscapeString(label) + `" data-confirm="` + html.EscapeString(confirm) + `"`
	}
	if danger {
		cls = "btn sm dng"
	}
	return template.HTML(fmt.Sprintf(`<form method="post" action="%s/act"%s><input type="hidden" name="_csrf" value="%s"><input type="hidden" name="a" value="%s"><input type="hidden" name="id" value="%v"><input type="hidden" name="v" value="%s"><input type="hidden" name="tab" value="%s"><button class="%s">%s</button></form>`,
		p, data, csrf, action, id, html.EscapeString(val), tab, cls, html.EscapeString(label)))
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
	fmt.Fprintf(&b, `<svg viewBox="0 0 560 140" width="100%%" role="img" aria-label="%s">`, html.EscapeString(label))
	bw := 560 / n
	for i, v := range vals {
		h := v * 100 / mx
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="4" style="fill:%s"><title>%s: %d</title></rect><text x="%d" y="132" font-size="10" text-anchor="middle" style="fill:var(--mute)">%s</text>`,
			i*bw+4, 108-h, bw-8, h, color, days[i], v, i*bw+bw/2, days[i][3:])
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

var adminTpl = template.Must(template.New("admin").Funcs(template.FuncMap{"act": actForm}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="robots" content="noindex,nofollow"><title>Dashboard</title><link rel="icon" href="/favicon.svg" type="image/svg+xml"><link rel="stylesheet" href="/static/style.css"><link rel="stylesheet" href="/static/ui.css"><link rel="stylesheet" href="/static/admin.css"></head><body class="adm-body">
{{if .Login}}<main class="lg"><form method="post" action="{{.Path}}/login" class="lg-card stag"><img src="/favicon.svg" width="48" height="48" alt=""><h1 class="ui-h">Dashboard</h1><p class="fine" style="margin:0">Sign in to manage the site.</p>{{if .Err}}<p class="err" role="alert">{{.Err}}</p>{{end}}<label class="field">Username<input name="u" autocomplete="username" autocapitalize="none" required></label><label class="field">Password<input type="password" name="p" autocomplete="current-password" required></label><button id="lbtn" class="btn wide"{{if .Wait}} data-wait="{{.Wait}}"{{end}}>Sign in</button><p id="lwait" class="fine center" role="status"></p></form></main>
{{else}}<div class="adm">
<nav class="adm-side" aria-label="Dashboard sections"><a class="adm-brand" href="{{.Path}}/"><img src="/favicon.svg" width="28" height="28" alt=""><span>Dashboard</span></a>{{range .Tabs}}<a href="{{$.Path}}/?tab={{.Key}}"{{if eq .Key $.Tab}} aria-current="page"{{end}}>{{.Label}}{{if and (eq .Key "reports") $.OpenReports}}<span class="cnt">{{$.OpenReports}}</span>{{end}}</a>{{end}}<form method="post" action="{{.Path}}/logout" class="adm-out"><input type="hidden" name="_csrf" value="{{.CSRF}}"><button class="btn soft sm">Sign out</button></form></nav>
<main class="adm-main"><header class="adm-head"><h1 class="ui-h">{{.Heading}}</h1><p class="fine">{{.Sub}}</p></header>{{if .Flash}}<div hidden data-flash="{{.Flash}}"></div>{{end}}
{{if eq .Tab "overview"}}<div class="stats">{{range $i, $s := .Stats}}<div class="stat" style="--i:{{$i}}"><span>{{$s.L}}</span><b>{{$s.V}}</b></div>{{end}}</div><section class="card"><h2>Chats started per day</h2>{{.ChatsSVG}}</section><section class="card"><h2>Messages per day</h2>{{.MsgsSVG}}</section>
{{else if eq .Tab "owners"}}<div class="toolbar"><p class="fine"><b>Disable</b> signs the owner out and pauses all their links. <b>Delete</b> removes their account, links and chats for good.</p><a class="btn soft sm" href="{{.Path}}/export?kind=owners">Export CSV</a></div><section class="card flush"><table class="tbl"><thead><tr><th>Username</th><th>Links</th><th>Joined</th><th>Status</th><th></th></tr></thead><tbody>{{range $i, $r := .Rows}}<tr style="--i:{{$i}}"><td data-label="Username"><b>{{$r.username}}</b></td><td data-label="Links">{{$r.links}}</td><td data-label="Joined">{{$r.joined}}</td><td data-label="Status">{{if $r.disabled}}<span class="pill off">Disabled</span>{{else}}<span class="pill on">Active</span>{{end}}</td><td><div class="ac">{{if $r.disabled}}{{act $.Path $.CSRF "owners" "owner_set" $r.id "Enable" "0" "" false}}{{else}}{{act $.Path $.CSRF "owners" "owner_set" $r.id "Disable" "1" "Sign this owner out and pause all of their links?" false}}{{end}}{{act $.Path $.CSRF "owners" "owner_delete" $r.id "Delete" "" "Permanently delete this account with all of its links and chats?" true}}</div></td></tr>{{else}}<tr><td colspan="5">No owners yet.</td></tr>{{end}}</tbody></table></section>
{{else if eq .Tab "links"}}<div class="toolbar"><p class="fine"><b>Pause</b> stops new chats on a link; existing chats keep working. <b>Delete</b> removes the link and all of its chats.</p></div><section class="card flush"><table class="tbl"><thead><tr><th>Name</th><th>Owner</th><th>Chats</th><th>Status</th><th></th></tr></thead><tbody>{{range $i, $r := .Rows}}<tr style="--i:{{$i}}"><td data-label="Name"><b>{{$r.label}}</b><div class="fine">{{$r.slug}}</div></td><td data-label="Owner">{{$r.username}}</td><td data-label="Chats">{{$r.chats}}</td><td data-label="Status">{{if $r.active}}<span class="pill on">Live</span>{{else}}<span class="pill off">Paused</span>{{end}}</td><td><div class="ac">{{if $r.active}}{{act $.Path $.CSRF "links" "link_set" $r.id "Pause" "0" "" false}}{{else}}{{act $.Path $.CSRF "links" "link_set" $r.id "Resume" "1" "" false}}{{end}}{{act $.Path $.CSRF "links" "link_delete" $r.id "Delete" "" "Permanently delete this link and all of its chats?" true}}</div></td></tr>{{else}}<tr><td colspan="5">No links yet.</td></tr>{{end}}</tbody></table></section>
{{else if eq .Tab "reports"}}<div class="toolbar"><p class="fine">Pick one action per report. Each button says what it does.</p><a class="btn soft sm" href="{{.Path}}/export?kind=reports">Export CSV</a></div>{{range $i, $r := .Rows}}<section class="card" style="--i:{{$i}}"><div class="rep-head"><b>Report #{{$r.id}} · chat {{$r.thread_id}}</b>{{if $r.resolved}}<span class="pill off">Handled</span>{{else}}<span class="pill warn">Needs review</span>{{end}}</div><p class="fine">Reported by the {{if eq $r.side "o"}}owner{{else}}guest{{end}} on {{$r.ts}}</p><p class="reason"><b>Reason:</b> {{$r.reason}}</p>{{if $r.evidence}}<div class="tx" style="white-space:pre-wrap" aria-label="Messages the reporter attached">{{$r.evidence}}</div>{{else}}<div class="tx" aria-label="Last messages in this chat">{{range $r.lines}}<div>{{.}}</div>{{else}}<div class="fine">No messages left in this chat.</div>{{end}}</div>{{end}}{{if not $r.resolved}}<div class="acts"><div class="act"><div><b>Mark reviewed</b><span>No action needed. The report leaves the queue and the chat carries on as normal.</span></div>{{act $.Path $.CSRF "reports" "report_resolve" $r.id "Mark reviewed" "" "" false}}</div><div class="act"><div><b>Close chat</b><span>Stops any new messages. Both people can still read what is already there. This can't be undone.</span></div>{{act $.Path $.CSRF "reports" "report_block" $r.id "Close chat" "" "Close this chat so nobody can send new messages? This can't be undone." false}}</div><div class="act"><div><b>Delete chat</b><span>Permanently removes the chat and every message from both people's screens.</span></div>{{act $.Path $.CSRF "reports" "report_delete" $r.id "Delete chat" "" "Permanently delete this chat and all of its messages?" true}}</div></div>{{end}}</section>{{else}}<section class="card"><p class="fine" style="margin:0">No reports. Nice and quiet.</p></section>{{end}}
{{else if eq .Tab "audit"}}<section class="card flush"><table class="tbl"><thead><tr><th>When</th><th>What happened</th><th>Details</th></tr></thead><tbody>{{range $i, $r := .Rows}}<tr style="--i:{{$i}}"><td data-label="When">{{$r.ts}}</td><td data-label="What">{{$r.action}}</td><td data-label="Details">{{$r.detail}}</td></tr>{{else}}<tr><td colspan="3">Nothing logged yet.</td></tr>{{end}}</tbody></table></section>
{{else}}<form method="post" action="{{.Path}}/settings"><input type="hidden" name="_csrf" value="{{.CSRF}}">{{range .Groups}}<section class="card"><h2>{{.Title}}</h2>{{range .Rows}}<div class="setrow"><label for="{{.Key}}"><b>{{.Label}}</b><span class="fine">{{.Help}}</span></label><div class="ctl">{{if eq .Kind "bool"}}<label class="swl"><input type="checkbox" role="switch" id="{{.Key}}" name="{{.Key}}" value="1"{{if .On}} checked{{end}}><span class="trk"></span><span class="kn"></span></label>{{else if eq .Kind "num"}}<input id="{{.Key}}" type="number" inputmode="numeric" name="{{.Key}}" value="{{.Val}}">{{else if eq .Kind "area"}}<textarea id="{{.Key}}" name="{{.Key}}" rows="4">{{.Val}}</textarea>{{else if eq .Kind "secret"}}<input id="{{.Key}}" type="password" name="{{.Key}}" autocomplete="off" placeholder="{{if .On}}Saved. Leave blank to keep it{{end}}">{{else}}<input id="{{.Key}}" name="{{.Key}}" value="{{.Val}}">{{end}}</div></div>{{end}}</section>{{end}}<div class="savebar"><button class="btn">Save settings</button></div></form>{{end}}
</main></div>{{end}}
<script src="/static/ui.js"></script><script src="/static/admin.js"></script></body></html>`))

func (a *App) adminPage(w http.ResponseWriter, code int, d adminData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := adminTpl.Execute(w, d); err != nil {
		logErr("admin template", err)
	}
}

func inTabs(t string) bool {
	for _, x := range adminTabs {
		if x.Key == t {
			return true
		}
	}
	return false
}

// adminRoot is the single entry point for every route under the secret path.
// Nothing past the login form runs without a valid admin session, and every POST needs the CSRF token.
func (a *App) adminRoot(w http.ResponseWriter, r *http.Request) {
	ap := "/" + a.cfg.AdminPath
	rel := strings.TrimPrefix(r.URL.Path, ap)
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	if r.Method == http.MethodPost && rel == "/login" {
		a.adminLogin(w, r, ap)
		return
	}
	tok := a.adminToken(r)
	if tok == "" {
		a.adminPage(w, http.StatusOK, adminData{Path: ap, Login: true})
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if r.ParseForm() != nil || !hmac.Equal([]byte(r.FormValue("_csrf")), []byte(a.csrf(tok))) {
			a.errorPage(w, r, http.StatusForbidden, "That form expired. Reload the page and try again.", "")
			return
		}
	}
	switch {
	case r.Method == http.MethodPost && rel == "/logout":
		a.endSession(r, "as")
		a.setCookie(w, "as", "", -1, true)
		http.Redirect(w, r, ap+"/", http.StatusSeeOther)
	case r.Method == http.MethodPost && rel == "/act":
		a.adminAct(w, r)
	case r.Method == http.MethodPost && rel == "/settings":
		a.adminSettings(w, r)
	case r.Method == http.MethodGet && rel == "/export":
		a.adminExport(w, r)
	case r.Method == http.MethodGet && rel == "/":
		a.adminView(w, r, tok)
	default:
		a.notFound(w, r)
	}
}

func (a *App) adminLogin(w http.ResponseWriter, r *http.Request, ap string) {
	ip := clientIP(r)
	pair, acct, byIP, wait := a.loginWait("admin", ip)
	if wait > 0 {
		a.adminPage(w, http.StatusTooManyRequests, adminData{Path: ap, Login: true, Err: "Too many attempts.", Wait: int(wait.Seconds()) + 1})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	ok1, ok2 := same(r.FormValue("u"), a.cfg.AdminUser), same(r.FormValue("p"), a.cfg.AdminPass)
	if !(ok1 && ok2) {
		a.fails.fail(pair, acct, byIP)
		log.Printf("dashboard login failed ip=%s", ip)
		if w2 := a.fails.wait(pair, 5); w2 > 0 {
			a.adminPage(w, http.StatusTooManyRequests, adminData{Path: ap, Login: true, Err: "Too many attempts.", Wait: int(w2.Seconds()) + 1})
			return
		}
		a.adminPage(w, http.StatusUnauthorized, adminData{Path: ap, Login: true, Err: "Wrong username or password"})
		return
	}
	a.fails.reset(pair, acct)
	tok, err := a.newSession(r.Context(), "admin", 0, 8*time.Hour)
	if err != nil {
		ref := logErr("dashboard session", err)
		a.errorPage(w, r, http.StatusInternalServerError, "Something went wrong on our side.", ref)
		return
	}
	a.setCookie(w, "as", tok, 8*3600, true)
	a.audit(r.Context(), "admin_login", "ip "+ip)
	http.Redirect(w, r, ap+"/", http.StatusSeeOther)
}

// adminView loads each tab with joined, aggregated queries: no query runs once per row.
func (a *App) adminView(w http.ResponseWriter, r *http.Request, tok string) {
	ctx := r.Context()
	d := adminData{Path: "/" + a.cfg.AdminPath, Tab: r.URL.Query().Get("tab"), Tabs: adminTabs, CSRF: a.csrf(tok)}
	if !inTabs(d.Tab) {
		d.Tab = "overview"
	}
	d.Heading, d.Sub = tabText[d.Tab][0], tabText[d.Tab][1]
	switch {
	case r.URL.Query().Get("ok") == "1":
		d.Flash = "Settings saved."
	case r.URL.Query().Get("done") == "1":
		d.Flash = "Done."
	}
	a.db.QueryRow(ctx, `SELECT count(*) FROM reports WHERE NOT resolved`).Scan(&d.OpenReports)
	var err error
	switch d.Tab {
	case "overview":
		var o, l, t, m, rp, m24 int
		err = a.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM owners),(SELECT count(*) FROM links),(SELECT count(*) FROM threads),(SELECT count(*) FROM messages),(SELECT count(*) FROM reports WHERE NOT resolved),(SELECT count(*) FROM messages WHERE created_at > now() - interval '24 hours')`).Scan(&o, &l, &t, &m, &rp, &m24)
		d.Stats = []stat{{"Owners", o}, {"Links", l}, {"Chats", t}, {"Messages", m}, {"Open reports", rp}, {"Messages in 24h", m24}}
		days, cs, ms := []string{}, []int{}, []int{}
		if rows, qerr := a.rowsOut(ctx, `WITH d AS (SELECT generate_series(current_date - 13, current_date, interval '1 day')::date AS day),
			c AS (SELECT created_at::date AS day, count(*) AS n FROM threads WHERE created_at >= current_date - 13 GROUP BY 1),
			m AS (SELECT created_at::date AS day, count(*) AS n FROM messages WHERE created_at >= current_date - 13 GROUP BY 1)
			SELECT to_char(d.day,'MM-DD') AS day, COALESCE(c.n,0) AS chats, COALESCE(m.n,0) AS msgs FROM d LEFT JOIN c USING (day) LEFT JOIN m USING (day) ORDER BY d.day`); qerr == nil {
			for _, row := range rows {
				c, _ := row["chats"].(int64)
				mm, _ := row["msgs"].(int64)
				days = append(days, fmt.Sprint(row["day"]))
				cs = append(cs, int(c))
				ms = append(ms, int(mm))
			}
		}
		d.ChatsSVG = barSVG("Chats started per day, last 14 days", days, cs, "var(--pri)")
		d.MsgsSVG = barSVG("Messages per day, last 14 days", days, ms, "var(--moss)")
	case "owners":
		d.Rows, err = a.rowsOut(ctx, `SELECT o.id, o.username, o.disabled, to_char(o.created_at,'YYYY-MM-DD') AS joined, COALESCE(l.n,0) AS links FROM owners o LEFT JOIN (SELECT owner_id, count(*) AS n FROM links GROUP BY owner_id) l ON l.owner_id=o.id ORDER BY o.id DESC LIMIT 200`)
	case "links":
		d.Rows, err = a.rowsOut(ctx, `SELECT l.id, l.slug, l.label, l.active, o.username, COALESCE(c.n,0) AS chats FROM links l JOIN owners o ON o.id=l.owner_id LEFT JOIN (SELECT link_id, count(*) AS n FROM threads GROUP BY link_id) c ON c.link_id=l.id ORDER BY l.id DESC LIMIT 200`)
	case "reports":
		// One query returns every report with its last 8 messages (encrypted ones are shown as such, never as ciphertext).
		d.Rows, err = a.rowsOut(ctx, `SELECT r.id, r.thread_id, r.side, r.reason, r.resolved, r.evidence, to_char(r.created_at,'YYYY-MM-DD HH24:MI') AS ts, COALESCE(x.lines, ARRAY[]::text[]) AS lines
			FROM reports r
			LEFT JOIN LATERAL (
				SELECT array_agg(s.line ORDER BY s.id) AS lines FROM (
					SELECT m.id, (CASE m.side WHEN 'v' THEN 'Guest' ELSE 'Owner' END) || ': ' || (CASE WHEN m.body LIKE 'e1.%' THEN '[encrypted]' ELSE m.body END) AS line
					FROM messages m WHERE m.thread_id=r.thread_id ORDER BY m.id DESC LIMIT 8) s) x ON true
			ORDER BY r.resolved, r.id DESC LIMIT 50`)
	case "audit":
		d.Rows, err = a.rowsOut(ctx, `SELECT to_char(created_at,'YYYY-MM-DD HH24:MI') AS ts, action, detail FROM audit ORDER BY id DESC LIMIT 100`)
		for _, row := range d.Rows {
			if l, ok := auditLabels[fmt.Sprint(row["action"])]; ok {
				row["action"] = l
			}
		}
	case "settings":
		meta := map[string]struct{ Label, Kind string }{}
		for _, m := range settingMeta {
			meta[m.Key] = struct{ Label, Kind string }{m.Label, m.Kind}
		}
		for _, g := range settingGroups {
			sg := setGroup{Title: g.Title}
			for _, k := range g.Keys {
				v := a.set.get(k)
				row := setRow{Key: k, Label: meta[k].Label, Kind: meta[k].Kind, Val: v, Help: settingInfo[k], On: v == "1"}
				if row.Kind == "secret" {
					row.Val, row.On = "", v != ""
				}
				sg.Rows = append(sg.Rows, row)
			}
			d.Groups = append(d.Groups, sg)
		}
	}
	if err != nil {
		ref := logErr("dashboard "+d.Tab, err)
		a.errorPage(w, r, http.StatusInternalServerError, "The dashboard couldn't load this page.", ref)
		return
	}
	a.adminPage(w, http.StatusOK, d)
}

// adminAct runs one dashboard action. Every action sets an exact end state (or deletes), so a double submit is harmless.
func (a *App) adminAct(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	act, v := r.FormValue("a"), r.FormValue("v")
	if err != nil || id < 1 {
		act = ""
	}
	switch act {
	case "owner_set":
		a.exec(ctx, act, `UPDATE owners SET disabled=($2::text='1') WHERE id=$1`, id, v)
		a.cache.delPrefix("s:")
	case "owner_delete":
		a.exec(ctx, act, `DELETE FROM owners WHERE id=$1`, id)
		a.cache.delPrefix("s:")
		a.cache.delPrefix("info:")
	case "link_set":
		a.exec(ctx, act, `UPDATE links SET active=($2::text='1') WHERE id=$1`, id, v)
		a.cache.delPrefix("info:")
	case "link_delete":
		a.exec(ctx, act, `DELETE FROM links WHERE id=$1`, id)
		a.cache.delPrefix("info:")
	case "report_resolve":
		a.exec(ctx, act, `UPDATE reports SET resolved=true WHERE id=$1`, id)
	case "report_block":
		var tid int64
		if a.db.QueryRow(ctx, `UPDATE reports SET resolved=true WHERE id=$1 RETURNING thread_id`, id).Scan(&tid) == nil {
			a.exec(ctx, act, `UPDATE threads SET status='blocked' WHERE id=$1`, tid)
			a.hub.pub(tid, map[string]any{"t": "status", "v": "blocked"})
		}
	case "report_delete":
		var tid int64
		if a.db.QueryRow(ctx, `SELECT thread_id FROM reports WHERE id=$1`, id).Scan(&tid) == nil {
			a.exec(ctx, act, `DELETE FROM threads WHERE id=$1`, tid)
			a.hub.pub(tid, map[string]any{"t": "gone"})
		}
	default:
		act = ""
	}
	if act != "" {
		a.audit(ctx, act, "#"+strconv.FormatInt(id, 10))
	}
	tab := r.FormValue("tab")
	if !inTabs(tab) {
		tab = "overview"
	}
	http.Redirect(w, r, "/"+a.cfg.AdminPath+"/?done=1&tab="+url.QueryEscape(tab), http.StatusSeeOther)
}

func (a *App) adminSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	for _, m := range settingMeta {
		v := clean(r.FormValue(m.Key))
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
			v = clip(v, 200)
		default:
			n := 200
			if m.Kind == "area" {
				n = 2000
			}
			v = clip(v, n)
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
		name, q = "owners.csv", `SELECT o.id, o.username, o.disabled, to_char(o.created_at,'YYYY-MM-DD') AS joined, COALESCE(l.n,0) AS links FROM owners o LEFT JOIN (SELECT owner_id, count(*) AS n FROM links GROUP BY owner_id) l ON l.owner_id=o.id ORDER BY o.id`
	case "reports":
		name, q = "reports.csv", `SELECT r.id, r.thread_id, r.side, r.reason, r.resolved, to_char(r.created_at,'YYYY-MM-DD HH24:MI') AS created FROM reports r ORDER BY r.id`
	default:
		a.notFound(w, r)
		return
	}
	rows, err := a.db.Query(r.Context(), q)
	if err != nil {
		ref := logErr("export "+name, err)
		a.errorPage(w, r, http.StatusInternalServerError, "The export couldn't be created.", ref)
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
			logErr("export row", err)
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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const siteUpdated = "2026-11-18"

type post struct {
	Slug, Title, Desc, Date string
	Body                    []string
}

// HTML renders the body: lines starting with "## " become headings, everything else a paragraph.
func (p post) HTML() template.HTML {
	var b strings.Builder
	for _, l := range p.Body {
		if strings.HasPrefix(l, "## ") {
			b.WriteString("<h2>" + html.EscapeString(l[3:]) + "</h2>")
		} else {
			b.WriteString("<p>" + html.EscapeString(l) + "</p>")
		}
	}
	return template.HTML(b.String())
}

func (p post) Markdown() string {
	var b strings.Builder
	for _, l := range p.Body {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

func (p post) Pretty() string {
	t, err := time.Parse("2006-01-02", p.Date)
	if err != nil {
		return p.Date
	}
	return t.Format("2 January 2006")
}

var posts = []post{
	{
		Slug:  "ask-a-sensitive-question-without-sharing-your-profile",
		Title: "How to ask a sensitive question without handing over your real profile",
		Desc:  "Replying to a group post often puts your account in front of everyone. Here is a calmer way to start the conversation.",
		Date:  "2026-11-14",
		Body: []string{
			"Say you spot a post in a community group and want to reply. The topic is personal: a health product, a debt, a relationship question. Replying in the comments puts your name in front of everyone. Messaging the poster directly lets them see your profile, your photos and your friends list before they have said a word to you.",
			"## What people usually do",
			"They make a throwaway account, which takes ten minutes and gets flagged half the time. They ask a friend to ask for them. Or they give up and never ask.",
			"## A gentler way",
			"If the poster shares a private chat link instead, you can write to them without an account. You still have to trust them enough to type your question, but nothing about your real identity travels with it.",
			"Start with the actual question, leave names and places out of it, and see how they respond. If the conversation goes well, you can swap contact details later, on your terms.",
			"## One rule that helps",
			"Don't type anything you would mind seeing repeated. A private chat is quieter than a public thread, but it is not a vault.",
		},
	},
	{
		Slug:  "why-mutual-reveal-beats-a-dm-request",
		Title: "Why a mutual reveal beats asking for someone's number",
		Desc:  "Asking for a number puts the other person on the spot. Letting both sides opt in takes the pressure off.",
		Date:  "2026-11-14",
		Body: []string{
			"Moving a chat to WhatsApp usually means one person asks and the other has to decide. If you say no, it is awkward. If you say yes because you feel cornered, you have shared something you were not ready to share.",
			"## The problem with asking",
			"A request has a built-in imbalance. The person asking has already made up their mind. The person answering has to react on the spot, with the whole chat history watching.",
			"## What changes with a mutual reveal",
			"Each person decides on their own time. Details only appear once both of you have agreed, so nobody hands over a number just because the other side asked first. You can see whether the other person is ready, but not what they plan to share, and either of you can withdraw before the reveal happens.",
			"## What gets shared",
			"Whatever each person types: a number, a handle, an email. It shows once, then disappears after 24 hours, so it does not sit in the chat forever.",
		},
	},
	{
		Slug:  "keep-your-chat-link-from-filling-with-spam",
		Title: "Five ways to keep a public chat link from filling with spam",
		Desc:  "Anything you post publicly attracts bots and bored strangers. These habits keep your inbox manageable.",
		Date:  "2026-11-14",
		Body: []string{
			"Public links get found. That is the point, and it is also the problem. A few habits keep the noise down.",
			"## 1. Use one link per place",
			"Make a separate link for each post or group. If one starts attracting junk, pause it and the others keep working.",
			"## 2. Say what the link is for",
			"A welcome message like \"Questions about the bike, please mention your city\" filters out people who did not read anything.",
			"## 3. Accept slowly",
			"Every chat starts as a request. You don't owe anyone a reply, and you can block a visitor in one tap so they cannot reach any of your links.",
			"## 4. Use quiet hours",
			"An auto-reply that says you will answer tonight sets expectations and stops people sending five follow-ups.",
			"## 5. Report what crosses the line",
			"Scams and harassment can be reported from inside the chat. Moderators see that conversation only because you sent it to them.",
		},
	},
}

const aboutMD = `Meet without revealing. Connect without pressure. Post your link anywhere and people can write to you without an account or seeing who you are. Every chat starts as a request that you accept or block, and every person gets a fresh temporary name for each chat. Contact details are shared only if both people agree, appear once, and disappear after 24 hours.

A conversation can last at most 7 days. People are warned 12 hours before it is deleted, and either person can end it sooner. Messages and shared contact details are end-to-end encrypted in the browser, so the server stores only ciphertext. It still sees metadata such as timing and message sizes, chats that started before encryption was added stay readable by the server until they expire, and being anonymous to the person you are talking to is not the same as being untraceable.

The code is open source under the MIT license.`

type pageData struct {
	Site, Tagline, Title, Desc, Canon, Image, Type, Path, Base string
	Noindex                                                   bool
	LD                                                        template.JS
	Posts                                                     []post
	Post                                                      post
	Year                                                      int
}

var (
	tplMu sync.Mutex
	tpls  = map[string]*template.Template{}
)

func loadTpl(name string) (*template.Template, error) {
	tplMu.Lock()
	defer tplMu.Unlock()
	if t, ok := tpls[name]; ok {
		return t, nil
	}
	t, err := template.ParseFS(staticFS, "static/layout.html", "static/"+name+".html")
	if err != nil {
		return nil, err
	}
	tpls[name] = t
	return t, nil
}

func (a *App) render(w http.ResponseWriter, name string, d pageData) {
	t, err := loadTpl(name)
	if err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "Page unavailable", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", d); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "Page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Without BASE_URL the page's links come from the request's Host header, so a shared cache must not keep it.
	if a.cfg.BaseURL == "" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	w.Write(buf.Bytes())
}

var baseWarn sync.Once

func (a *App) base(r *http.Request) string {
	if a.cfg.BaseURL != "" {
		return a.cfg.BaseURL
	}
	baseWarn.Do(func() {
		log.Printf("WARNING: BASE_URL is not set, so canonical links and the sitemap are built from the request Host header. Set BASE_URL to your public address.")
	})
	s := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		s = "https"
	}
	return s + "://" + r.Host
}

func (a *App) pd(r *http.Request, path, title, desc string) pageData {
	b := a.base(r)
	return pageData{Site: a.set.get("site_name"), Tagline: a.set.get("tagline"), Title: title, Desc: desc,
		Canon: b + path, Image: b + "/og.png", Type: "website", Path: path, Base: b, Posts: posts, Year: time.Now().Year()}
}

func ld(v any) template.JS {
	b, _ := json.Marshal(v)
	return template.JS(b)
}

func (a *App) siteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /about", a.about)
	mux.HandleFunc("GET /blog", a.blogIndex)
	mux.HandleFunc("GET /blog/{slug}", a.blogPost)
	mux.HandleFunc("GET /feed.xml", a.feed)
	mux.HandleFunc("GET /sitemap.xml", a.sitemap)
	mux.HandleFunc("GET /robots.txt", a.robots)
	mux.HandleFunc("GET /llms.txt", a.llms)
	mux.HandleFunc("GET /llms-full.txt", a.llmsFull)
	mux.HandleFunc("GET /manifest.webmanifest", a.manifest)
	mux.HandleFunc("GET /favicon.svg", a.favicon)
	mux.HandleFunc("GET /og.png", a.ogImage)
	mux.HandleFunc("GET /icons/{size}", a.icon)
	a.publicRoutes(mux)
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/", site+": meet without revealing", "Meet without revealing. Connect without pressure. Share a link, chat first, and swap contact details later, only when you both agree.")
	d.LD = ld(map[string]any{"@context": "https://schema.org", "@graph": []any{
		map[string]any{"@type": "WebSite", "name": site, "url": d.Base},
		map[string]any{"@type": "Organization", "name": site, "url": d.Base, "logo": d.Base + "/icons/512.png"},
	}})
	a.render(w, "index", d)
}

func (a *App) about(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/about", "About "+site, "What "+site+" is, why it exists, how the contact reveal works, and what it does not protect.")
	d.LD = ld(map[string]any{"@context": "https://schema.org", "@type": "AboutPage", "name": d.Title, "url": d.Canon, "description": d.Desc})
	a.render(w, "about", d)
}

func (a *App) blogIndex(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/blog", "Blog | "+site, "Practical notes on private conversations, contact sharing and keeping a public chat link tidy.")
	d.LD = ld(map[string]any{"@context": "https://schema.org", "@type": "Blog", "name": site + " blog", "url": d.Canon})
	a.render(w, "blog", d)
}

func (a *App) blogPost(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	for _, p := range posts {
		if p.Slug != slug {
			continue
		}
		site := a.set.get("site_name")
		d := a.pd(r, "/blog/"+p.Slug, p.Title+" | "+site, p.Desc)
		d.Type, d.Post = "article", p
		d.LD = ld(map[string]any{"@context": "https://schema.org", "@type": "BlogPosting", "headline": p.Title, "description": p.Desc,
			"datePublished": p.Date, "dateModified": p.Date, "mainEntityOfPage": d.Canon, "image": d.Image,
			"author":    map[string]any{"@type": "Organization", "name": site},
			"publisher": map[string]any{"@type": "Organization", "name": site, "logo": map[string]any{"@type": "ImageObject", "url": d.Base + "/icons/512.png"}}})
		a.render(w, "post", d)
		return
	}
	http.NotFound(w, r)
}

func (a *App) feed(w http.ResponseWriter, r *http.Request) {
	b, site := a.base(r), html.EscapeString(a.set.get("site_name"))
	var s strings.Builder
	s.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>`)
	fmt.Fprintf(&s, "<title>%s blog</title><link>%s/blog</link><description>%s</description>", site, b, html.EscapeString(a.set.get("tagline")))
	for _, p := range posts {
		t, _ := time.Parse("2006-01-02", p.Date)
		fmt.Fprintf(&s, "<item><title>%s</title><link>%s/blog/%s</link><guid>%s/blog/%s</guid><pubDate>%s</pubDate><description>%s</description></item>",
			html.EscapeString(p.Title), b, p.Slug, b, p.Slug, t.Format(time.RFC1123Z), html.EscapeString(p.Desc))
	}
	s.WriteString("</channel></rss>")
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(s.String()))
}

// Only public pages are listed. Private routes never appear here.
func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	b := a.base(r)
	var s strings.Builder
	s.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	add := func(p, d string) { fmt.Fprintf(&s, "<url><loc>%s%s</loc><lastmod>%s</lastmod></url>", b, p, d) }
	add("/", siteUpdated)
	add("/about", siteUpdated)
	add("/privacy", siteUpdated)
	add("/terms", siteUpdated)
	add("/blog", posts[0].Date)
	for _, p := range posts {
		add("/blog/"+p.Slug, p.Date)
	}
	s.WriteString("</urlset>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(s.String()))
}

func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /app\nDisallow: /api/\nDisallow: /l/\nDisallow: /r/\n\nSitemap: %s/sitemap.xml\n", a.base(r))
}

func (a *App) llms(w http.ResponseWriter, r *http.Request) {
	b, site := a.base(r), a.set.get("site_name")
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n> %s\n\nPrivate chats from a link. Guests write without an account, the owner approves each chat, every person gets a fresh temporary name per chat, and contact details are shared only when both people agree. Messages are end-to-end encrypted in the browser. Chats are deleted after 7 days at most.\n\n## Pages\n\n", site, a.set.get("tagline"))
	fmt.Fprintf(&s, "- [About](%s/about): what it is, why it exists, what it does not protect\n- [Privacy](%s/privacy): what is stored and for how long\n- [Terms](%s/terms): the rules for using it\n- [Blog](%s/blog): practical notes on private conversations\n\n## Posts\n\n", b, b, b, b)
	for _, p := range posts {
		fmt.Fprintf(&s, "- [%s](%s/blog/%s): %s\n", p.Title, b, p.Slug, p.Desc)
	}
	fmt.Fprintf(&s, "\n## Optional\n\n- [Full text of all pages](%s/llms-full.txt)\n- [Source code](https://github.com/Advay254/allovchat)\n", b)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(s.String()))
}

func (a *App) llmsFull(w http.ResponseWriter, r *http.Request) {
	b, site := a.base(r), a.set.get("site_name")
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n> %s\n\n## About\n\nSource: %s/about\n\n%s\n\n", site, a.set.get("tagline"), b, aboutMD)
	for _, p := range posts {
		fmt.Fprintf(&s, "## %s\n\nSource: %s/blog/%s\nPublished: %s\n\n%s", p.Title, b, p.Slug, p.Date, strings.ReplaceAll(p.Markdown(), "## ", "### "))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(s.String()))
}

func (a *App) manifest(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{"name": site, "short_name": site, "description": a.set.get("tagline"), "start_url": "/app", "scope": "/", "display": "standalone",
		"background_color": "#0B1220", "theme_color": "#0B1220",
		"icons": []any{
			map[string]any{"src": "/icons/192.png", "sizes": "192x192", "type": "image/png"},
			map[string]any{"src": "/icons/512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable"},
		}})
}

// ---------- brand assets: the symbol is drawn in code so the icons and share image always match the logo ----------

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="15" fill="#0B1220"/><g transform="translate(6 16.8) scale(.52)"><path fill="#2563FF" fill-rule="evenodd" d="M28 4a26 26 0 1 0 0 52 26 26 0 0 0 0-52zm0 14a12 12 0 1 1 0 24 12 12 0 0 1 0-24z"/><path fill="#2563FF" d="M36 48l16 10-6-16z"/><path fill="#fff" fill-rule="evenodd" d="M70 4A26 26 0 0 0 44 30 26 26 0 0 0 70 56H96V30A26 26 0 0 0 70 4zM70 17a11 11 0 1 0 0 22 11 11 0 0 0 0-22z"/><rect x="64" y="22" width="12" height="9" rx="4.5" fill="#fff"/><path fill="#fff" d="M65 30h5l-5 6z"/></g></svg>`

func (a *App) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(faviconSVG))
}

var (
	cNavy  = color.RGBA{0x0B, 0x12, 0x20, 255}
	cBlue  = color.RGBA{0x25, 0x63, 0xFF, 255}
	cWhite = color.RGBA{255, 255, 255, 255}
	cSlate = color.RGBA{0x16, 0x23, 0x38, 255}
	imgs   sync.Map
)

func rrect(img *image.RGBA, x0, y0, x1, y1, r int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			dx, dy := 0, 0
			if x < x0+r {
				dx = x0 + r - x
			} else if x >= x1-r {
				dx = x - (x1 - r - 1)
			}
			if y < y0+r {
				dy = y0 + r - y
			} else if y >= y1-r {
				dy = y - (y1 - r - 1)
			}
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func blendPx(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if !image.Pt(x, y).In(img.Bounds()) || a <= 0 {
		return
	}
	d := img.RGBAAt(x, y)
	mix := func(s, t uint8) uint8 { return uint8(float64(s)*(1-a) + float64(t)*a) }
	img.SetRGBA(x, y, color.RGBA{mix(d.R, c.R), mix(d.G, c.G), mix(d.B, c.B), 255})
}

// glow paints a soft blue radial light.
func glow(img *image.RGBA, cx, cy, radius int, c color.RGBA, strength float64) {
	for y := cy - radius; y < cy+radius; y++ {
		for x := cx - radius; x < cx+radius; x++ {
			d := math.Hypot(float64(x-cx), float64(y-cy)) / float64(radius)
			if d < 1 {
				blendPx(img, x, y, c, strength*(1-d)*(1-d))
			}
		}
	}
}

func tri(px, py, ax, ay, bx, by, cx, cy float64) bool {
	d1 := (px-bx)*(ay-by) - (ax-bx)*(py-by)
	d2 := (px-cx)*(by-cy) - (bx-cx)*(py-cy)
	d3 := (px-ax)*(cy-ay) - (cx-ax)*(py-ay)
	return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
}

func roundRectHit(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx, cy := math.Max(x0+r, math.Min(px, x1-r)), math.Max(y0+r, math.Min(py, y1-r))
	return math.Hypot(px-cx, py-cy) <= r
}

// markAt returns the colour of the brand symbol at a point on its 100 x 60 design grid.
// Two overlapping chat bubbles: a blue ring on the left, a solid bubble with a small speech bubble cut-out on the right.
func markAt(x, y float64, right color.RGBA) (color.RGBA, bool) {
	if math.Hypot(x-70, y-30) <= 26 || (x >= 70 && x <= 96 && y >= 30 && y <= 56) {
		if math.Hypot(x-70, y-28) > 11 || roundRectHit(x, y, 64, 22, 76, 31, 4.5) || tri(x, y, 65, 30, 65, 36, 70, 30) {
			return right, true
		}
	}
	d := math.Hypot(x-28, y-30)
	if (d >= 12 && d <= 26) || tri(x, y, 36, 48, 52, 58, 46, 42) {
		return cBlue, true
	}
	return color.RGBA{}, false
}

func paintMark(img *image.RGBA, ox, oy int, s float64, right color.RGBA) {
	w, h := int(100*s)+1, int(60*s)+1
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			var r, g, b, n float64
			for sy := 0; sy < 2; sy++ {
				for sx := 0; sx < 2; sx++ {
					x, y := (float64(px)+0.25+0.5*float64(sx))/s, (float64(py)+0.25+0.5*float64(sy))/s
					if c, ok := markAt(x, y, right); ok {
						r, g, b, n = r+float64(c.R), g+float64(c.G), b+float64(c.B), n+1
					}
				}
			}
			if n > 0 {
				blendPx(img, ox+px, oy+py, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), 255}, n/4)
			}
		}
	}
}

func renderPNG(key string, w, h int, draw func(*image.RGBA)) []byte {
	if v, ok := imgs.Load(key); ok {
		return v.([]byte)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = cNavy.R, cNavy.G, cNavy.B, 255
	}
	draw(img)
	var buf bytes.Buffer
	png.Encode(&buf, img)
	imgs.Store(key, buf.Bytes())
	return buf.Bytes()
}

func servePNG(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (a *App) ogImage(w http.ResponseWriter, r *http.Request) {
	servePNG(w, renderPNG("og2", 1200, 630, func(img *image.RGBA) {
		glow(img, 1000, 140, 560, cBlue, 0.42)
		paintMark(img, 110, 160, 5.2, cWhite)
		rrect(img, 700, 210, 1090, 246, 18, cSlate)
		rrect(img, 700, 278, 990, 314, 18, cSlate)
		rrect(img, 700, 346, 880, 382, 18, cSlate)
		rrect(img, 700, 440, 900, 496, 28, cBlue)
	}))
}

func (a *App) icon(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("size"), ".png"))
	if err != nil || (n != 180 && n != 192 && n != 512) {
		http.NotFound(w, r)
		return
	}
	data := renderPNG("icon2"+strconv.Itoa(n), n, n, func(img *image.RGBA) {
		s := float64(n) * 0.6 / 100
		paintMark(img, int((float64(n)-100*s)/2), int((float64(n)-60*s)/2), s, cWhite)
	})
	servePNG(w, data)
}

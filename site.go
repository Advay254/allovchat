package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
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
			"Scams and harassment can be reported from inside the chat. Our team sees that conversation only because you chose to send it to us.",
		},
	},
}

type qa struct{ Q, A string }

// faqs appear on the landing page and, word for word, in its FAQPage structured data.
var faqs = []qa{
	{"Do guests need an account?", "No. Anyone who opens your link can write to you straight away. They get a temporary name and never have to share an email address, phone number or profile."},
	{"Who can read my messages?", "Only you and the person you are chatting with. Messages are encrypted in your browser before they are sent, so our servers store ciphertext that we cannot read. We can see basic metadata, such as when a chat started and how large messages are."},
	{"How long do chats last?", "At most 7 days. The link owner can choose a shorter limit, from 1 hour to 7 days, and either person can end a chat sooner. Both people see a warning 12 hours before a chat is deleted."},
	{"Is it anonymous?", "You are anonymous to the person you chat with: guests need no account, and everyone gets a fresh temporary name in every chat. Anonymous is not the same as untraceable, so share only what you are comfortable having connected to you."},
	{"How does the contact reveal work?", "Either person can offer a contact, such as a WhatsApp number or a handle. Nothing is shown until both of you agree. Then each of you sees the other's details once, and they are deleted after 24 hours."},
	{"What if someone is abusive or sends spam?", "You approve every chat before it starts. You can block a guest in one tap, report a chat to our team, or press Leave to delete the chat for both people at once."},
	{"Does privacy mode hide things from me too?", "Yes. Privacy mode hides your typing, read receipts and online status, and it hides theirs from you as well. That keeps things fair, even if the other person has not turned it on."},
	{"Is AllovChat free and open source?", "Yes. The code is open source under the MIT license. You can read it, run your own copy or contribute on GitHub."},
	{"Can I delete my account?", "Yes, at any time from Settings. Deleting your account removes your links and every chat on them, and it cannot be undone."},
}

const aboutMD = `AllovChat gives you a link that opens a private chat. Post it anywhere and people can write to you without an account or seeing who you are. Every chat starts as a request that you accept or block, and every person gets a fresh temporary name for each chat. Contact details are shared only if both people agree, appear once, and disappear after 24 hours.

A conversation lasts at most 7 days. Both people are warned 12 hours before it is deleted, and either person can end it sooner. Messages and shared contact details are end-to-end encrypted in the browser, so the server stores only ciphertext it cannot read. It still sees metadata such as timing and message sizes. Being anonymous to the person you are talking to is different from being untraceable.

The code is open source under the MIT license.`

type pageData struct {
	Site, Tagline, Title, Desc, Canon, Image, Type, Path, Base string
	Noindex                                                    bool
	LD                                                         template.JS
	Posts                                                      []post
	Post                                                       post
	FAQ                                                        []qa
	Year                                                       int
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
	w.Write(versioned(buf.Bytes()))
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
	d := a.pd(r, "/", site+": private chats from a link, without revealing who you are", "Meet without revealing. Connect without pressure. Share a link, chat first with end-to-end encryption, and swap contact details later, only when you both agree.")
	d.FAQ = faqs
	qs := make([]any, 0, len(faqs))
	for _, f := range faqs {
		qs = append(qs, map[string]any{"@type": "Question", "name": f.Q, "acceptedAnswer": map[string]any{"@type": "Answer", "text": f.A}})
	}
	d.LD = ld(map[string]any{"@context": "https://schema.org", "@graph": []any{
		map[string]any{"@type": "WebSite", "name": site, "url": d.Base},
		map[string]any{"@type": "Organization", "name": site, "url": d.Base, "logo": d.Base + "/icons/512.png"},
		map[string]any{"@type": "FAQPage", "mainEntity": qs},
	}})
	a.render(w, "index", d)
}

func (a *App) about(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	d := a.pd(r, "/about", "About "+site, "What "+site+" is, why it exists, how the contact reveal works, and what anonymity does and does not mean.")
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

// Chat links (/l/) are open to link-preview crawlers and kept out of search with a noindex header instead.
// Return links (/r/) carry a secret, so crawlers stay away from them.
func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /app\nDisallow: /api/\nDisallow: /r/\n\nSitemap: %s/sitemap.xml\n", a.base(r))
}

func (a *App) llms(w http.ResponseWriter, r *http.Request) {
	b, site := a.base(r), a.set.get("site_name")
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n> %s\n\nPrivate chats from a link. Guests write without an account, the owner approves each chat, every person gets a fresh temporary name per chat, and contact details are shared only when both people agree. Messages are end-to-end encrypted in the browser. Chats are deleted after 7 days at most.\n\n## Pages\n\n", site, a.set.get("tagline"))
	fmt.Fprintf(&s, "- [About](%s/about): what it is, why it exists, what anonymity does and does not mean\n- [Privacy](%s/privacy): what is stored, why, and for how long\n- [Terms](%s/terms): the terms for using it\n- [Blog](%s/blog): practical notes on private conversations\n\n## Posts\n\n", b, b, b, b)
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

// The app is light only, so the install splash and browser chrome use the same light colour as the pages.
func (a *App) manifest(w http.ResponseWriter, r *http.Request) {
	site := a.set.get("site_name")
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(map[string]any{"name": site, "short_name": site, "description": a.set.get("tagline"), "start_url": "/app", "scope": "/", "display": "standalone",
		"background_color": "#F5F8FD", "theme_color": "#F5F8FD",
		"icons": []any{
			map[string]any{"src": "/icons/192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			map[string]any{"src": "/icons/512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			map[string]any{"src": "/icons/512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		}})
}

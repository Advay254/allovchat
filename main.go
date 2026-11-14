package main

import (
	"context"
	"embed"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed static
var staticFS embed.FS

type Config struct {
	DatabaseURL, AdminUser, AdminPass, AdminPath, BaseURL, Port, Secret string
	Secure                                                               bool
}

type App struct {
	cfg   Config
	db    *pgxpool.Pool
	hub   *Hub
	set   *Settings
	rl    *Limiter
	fails *FailLimiter
}

// Paths the app already uses, which an admin slug must not collide with.
var reservedPaths = map[string]bool{"health": true, "static": true, "about": true, "blog": true, "icons": true}

const csp = "default-src 'self'; script-src 'self' https://challenges.cloudflare.com; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func loadConfig() Config {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AdminUser:   os.Getenv("ADMIN_USERNAME"),
		AdminPass:   os.Getenv("ADMIN_PASSWORD"),
		AdminPath:   strings.Trim(os.Getenv("ADMIN_PATH"), "/"),
		BaseURL:     strings.TrimRight(os.Getenv("BASE_URL"), "/"),
		Port:        os.Getenv("PORT"),
		Secret:      os.Getenv("SESSION_SECRET"),
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	for k, v := range map[string]string{"DATABASE_URL": c.DatabaseURL, "ADMIN_USERNAME": c.AdminUser, "ADMIN_PASSWORD": c.AdminPass, "ADMIN_PATH": c.AdminPath} {
		if v == "" {
			log.Fatalf("missing required env var %s (see .env.example)", k)
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`).MatchString(c.AdminPath) {
		log.Fatal("ADMIN_PATH must be 4-64 characters: letters, digits, - or _")
	}
	if reservedPaths[c.AdminPath] {
		log.Fatalf("ADMIN_PATH can't be %q because the app already uses it; pick another", c.AdminPath)
	}
	if c.Secret == "" {
		c.Secret = sha256hex(c.AdminPass + "|" + c.DatabaseURL)
	}
	if v := os.Getenv("TRUSTED_PROXY_HOPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 5 {
			proxyHops = n
		}
	}
	c.Secure = strings.HasPrefix(c.BaseURL, "https://")
	return c
}

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database config: %v", err)
	}
	defer db.Close()
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	if err := db.Ping(pctx); err != nil {
		log.Fatalf("cannot reach database: %v", err)
	}
	cancel()
	a := &App{cfg: cfg, db: db, hub: newHub(), set: &Settings{m: map[string]string{}}, rl: newLimiter(), fails: newFails()}
	if err := a.migrate(ctx); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	a.loadSettings(ctx)
	go a.janitor(ctx)
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 16}
	go func() {
		<-ctx.Done()
		sc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(sc)
	}()
	log.Printf("allovchat listening on :%s (admin dashboard enabled)", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("GET /app", a.page("app.html"))
	mux.HandleFunc("GET /l/{slug}", a.page("chat.html"))
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	a.siteRoutes(mux)
	a.apiRoutes(mux)
	ap := "/" + a.cfg.AdminPath
	mux.HandleFunc(ap, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ap+"/", http.StatusFound) })
	mux.HandleFunc(ap+"/", a.adminRoot)
	return a.guard(mux)
}

// guard adds security headers, recovers from panics without leaking details,
// blocks cross-site writes, serves the maintenance page, and swaps plain 404s for the designed one.
func (a *App) guard(next http.Handler) http.Handler {
	ap := "/" + a.cfg.AdminPath
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		if a.cfg.Secure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		defer func() {
			if rec := recover(); rec != nil {
				ref := randHex(3)
				log.Printf("PANIC ref=%s %s %s: %v\n%s", ref, r.Method, r.URL.Path, rec, debug.Stack())
				a.errorPage(w, r, http.StatusInternalServerError, "Something went wrong on our side.", ref)
			}
		}()
		p := r.URL.Path
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					a.errorPage(w, r, http.StatusForbidden, "That request was blocked.", "")
					return
				}
			}
			if strings.HasPrefix(p, "/api/") && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, http.StatusUnsupportedMediaType, "Unsupported request")
				return
			}
		}
		if a.set.get("maintenance") == "1" && p != "/health" && p != "/favicon.svg" && !strings.HasPrefix(p, ap) && !strings.HasPrefix(p, "/static/") && !strings.HasPrefix(p, "/icons/") {
			a.maintenance(w, r)
			return
		}
		nw := &nfw{ResponseWriter: w, r: r}
		next.ServeHTTP(nw, r)
		if nw.swallowed {
			a.notFound(w, r)
		}
	})
}

func (a *App) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	}
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := a.db.Ping(ctx); err != nil {
		log.Printf("health: database ping failed: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"degraded"}`))
		return
	}
	w.Write([]byte(`{"status":"ok"}`))
}

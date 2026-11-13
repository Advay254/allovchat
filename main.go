package main

import (
	"context"
	"embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
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
	cfg Config
	db  *pgxpool.Pool
	hub *Hub
	set *Settings
	rl  *Limiter
}

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
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`).MatchString(c.AdminPath) {
		log.Fatal("ADMIN_PATH must be 8-64 characters: letters, digits, - or _")
	}
	if c.Secret == "" {
		c.Secret = sha256hex(c.AdminPass + "|" + c.DatabaseURL)
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
	a := &App{cfg: cfg, db: db, hub: newHub(), set: &Settings{m: map[string]string{}}, rl: newLimiter()}
	if err := a.migrate(ctx); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	a.loadSettings(ctx)
	go a.janitor(ctx)
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
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
	mux.HandleFunc("GET /{$}", a.page("index.html"))
	mux.HandleFunc("GET /app", a.page("app.html"))
	mux.HandleFunc("GET /l/{slug}", a.page("chat.html"))
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	a.apiRoutes(mux)
	ap := "/" + a.cfg.AdminPath
	mux.HandleFunc(ap, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ap+"/", http.StatusFound) })
	mux.HandleFunc(ap+"/", a.adminRoot)
	return a.guard(mux)
}

func (a *App) guard(next http.Handler) http.Handler {
	ap := "/" + a.cfg.AdminPath
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		p := r.URL.Path
		if a.set.get("maintenance") == "1" && p != "/health" && !strings.HasPrefix(p, ap) && !strings.HasPrefix(p, "/static/") {
			http.Error(w, "Down for maintenance. Back soon.", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
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
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"degraded","db":"down"}`))
		return
	}
	w.Write([]byte(`{"status":"ok","db":"up"}`))
}

package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS owners(id BIGSERIAL PRIMARY KEY, username TEXT UNIQUE NOT NULL, pass TEXT NOT NULL, disabled BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS links(id BIGSERIAL PRIMARY KEY, owner_id BIGINT NOT NULL REFERENCES owners(id) ON DELETE CASCADE, slug TEXT UNIQUE NOT NULL, label TEXT NOT NULL, active BOOLEAN NOT NULL DEFAULT true, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS threads(id BIGSERIAL PRIMARY KEY, link_id BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE, token_hash TEXT NOT NULL, alias TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', owner_agreed BOOLEAN NOT NULL DEFAULT false, visitor_agreed BOOLEAN NOT NULL DEFAULT false, owner_contact TEXT NOT NULL DEFAULT '', visitor_contact TEXT NOT NULL DEFAULT '', revealed_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS threads_link ON threads(link_id);
CREATE INDEX IF NOT EXISTS threads_tok ON threads(token_hash);
CREATE TABLE IF NOT EXISTS messages(id BIGSERIAL PRIMARY KEY, thread_id BIGINT NOT NULL REFERENCES threads(id) ON DELETE CASCADE, side TEXT NOT NULL, body TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), read_at TIMESTAMPTZ);
CREATE INDEX IF NOT EXISTS messages_thread ON messages(thread_id, id);
CREATE TABLE IF NOT EXISTS reports(id BIGSERIAL PRIMARY KEY, thread_id BIGINT NOT NULL REFERENCES threads(id) ON DELETE CASCADE, side TEXT NOT NULL, reason TEXT NOT NULL, resolved BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);`

func (a *App) migrate(ctx context.Context) error {
	for _, s := range strings.Split(schema, ";") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if _, err := a.db.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// Settings editable from the admin dashboard.
var settingMeta = []struct{ Key, Label, Kind string }{
	{"site_name", "Site name", "text"},
	{"tagline", "Tagline", "text"},
	{"announcement", "Announcement banner (empty = none)", "text"},
	{"registration_open", "Allow new owner sign-ups", "bool"},
	{"maintenance", "Maintenance mode (public site returns 503)", "bool"},
	{"max_msg_len", "Max message length", "num"},
	{"retention_days", "Delete chats after N days of inactivity", "num"},
	{"max_links_per_owner", "Max links per owner", "num"},
	{"msgs_per_min", "Messages per minute per person", "num"},
}

var defaults = map[string]string{
	"site_name": "allovchat", "tagline": "Chat anonymously. Share your contact only when you both trust each other.",
	"announcement": "", "registration_open": "1", "maintenance": "0", "max_msg_len": "1000",
	"retention_days": "7", "max_links_per_owner": "5", "msgs_per_min": "30",
}

var limits = map[string][2]int{"max_msg_len": {1, 5000}, "retention_days": {1, 365}, "max_links_per_owner": {1, 100}, "msgs_per_min": {1, 300}}

type Settings struct {
	mu sync.RWMutex
	m  map[string]string
}

func (s *Settings) get(k string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.m[k]; ok {
		return v
	}
	return defaults[k]
}

func (s *Settings) num(k string) int {
	n, err := strconv.Atoi(s.get(k))
	if err != nil {
		n, _ = strconv.Atoi(defaults[k])
	}
	return n
}

func (a *App) loadSettings(ctx context.Context) {
	m := map[string]string{}
	rows, err := a.db.Query(ctx, `SELECT key, value FROM settings`)
	if err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				m[k] = v
			}
		}
		rows.Close()
	}
	a.set.mu.Lock()
	a.set.m = m
	a.set.mu.Unlock()
}

func (a *App) saveSetting(ctx context.Context, k, v string) {
	a.db.Exec(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, k, v)
}

// janitor deletes inactive chats and expires revealed contacts.
func (a *App) janitor(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.db.Exec(ctx, `DELETE FROM threads WHERE last_at < now() - make_interval(days => $1::int)`, a.set.num("retention_days"))
		a.db.Exec(ctx, `UPDATE threads SET owner_contact='', visitor_contact='' WHERE revealed_at < now() - interval '24 hours' AND (owner_contact<>'' OR visitor_contact<>'')`)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

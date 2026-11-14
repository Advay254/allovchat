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
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
ALTER TABLE links ADD COLUMN IF NOT EXISTS welcome TEXT NOT NULL DEFAULT '';
ALTER TABLE links ADD COLUMN IF NOT EXISTS about TEXT NOT NULL DEFAULT '';
ALTER TABLE links ADD COLUMN IF NOT EXISTS autoreply TEXT NOT NULL DEFAULT '';
ALTER TABLE links ADD COLUMN IF NOT EXISTS quiet_start INT NOT NULL DEFAULT -1;
ALTER TABLE links ADD COLUMN IF NOT EXISTS quiet_end INT NOT NULL DEFAULT -1;
ALTER TABLE links ADD COLUMN IF NOT EXISTS tz INT NOT NULL DEFAULT 0;
ALTER TABLE links ADD COLUMN IF NOT EXISTS ttl_hours INT NOT NULL DEFAULT 168;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS vid_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN IF NOT EXISTS owner_alias TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN IF NOT EXISTS priv_o BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS priv_v BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
UPDATE threads SET expires_at = created_at + interval '7 days' WHERE expires_at IS NULL;
ALTER TABLE threads ALTER COLUMN expires_at SET DEFAULT now() + interval '7 days';
ALTER TABLE threads ALTER COLUMN expires_at SET NOT NULL;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS account_id BIGINT REFERENCES owners(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS threads_exp ON threads(expires_at);
CREATE INDEX IF NOT EXISTS threads_acct ON threads(account_id);
CREATE TABLE IF NOT EXISTS blocks(owner_id BIGINT NOT NULL REFERENCES owners(id) ON DELETE CASCADE, vid_hash TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(owner_id, vid_hash));
CREATE TABLE IF NOT EXISTS audit(id BIGSERIAL PRIMARY KEY, action TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, kind TEXT NOT NULL, owner_id BIGINT REFERENCES owners(id) ON DELETE CASCADE, expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS sessions_exp ON sessions(expires_at);`

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
	{"announcement", "Announcement banner", "text"},
	{"registration_open", "Allow new sign-ups", "bool"},
	{"maintenance", "Maintenance mode", "bool"},
	{"max_msg_len", "Max message length", "num"},
	{"retention_days", "Delete inactive chats after (days)", "num"},
	{"max_links_per_owner", "Max links per owner", "num"},
	{"msgs_per_min", "Messages per minute", "num"},
	{"banned_words", "Banned words", "area"},
	{"turnstile_site", "Turnstile site key", "text"},
	{"turnstile_secret", "Turnstile secret key", "secret"},
}

var defaults = map[string]string{
	"site_name": "allovchat", "tagline": "Meet without revealing. Connect without pressure.",
	"announcement": "", "registration_open": "1", "maintenance": "0", "max_msg_len": "1000",
	"retention_days": "7", "max_links_per_owner": "5", "msgs_per_min": "30",
	"banned_words": "", "turnstile_site": "", "turnstile_secret": "",
}

var limits = map[string][2]int{"max_msg_len": {1, 5000}, "retention_days": {1, 7}, "max_links_per_owner": {1, 100}, "msgs_per_min": {1, 300}}

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
	} else {
		logErr("load settings", err)
	}
	a.set.mu.Lock()
	a.set.m = m
	a.set.mu.Unlock()
}

func (a *App) saveSetting(ctx context.Context, k, v string) {
	a.exec(ctx, "save setting", `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, k, v)
}

func (a *App) audit(ctx context.Context, action, detail string) {
	a.exec(ctx, "audit", `INSERT INTO audit(action,detail) VALUES($1,$2)`, action, detail)
}

// janitor enforces the hard 7 day limit (and any shorter per-chat limit), removes inactive chats,
// expires revealed contacts and clears old sessions.
func (a *App) janitor(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		a.exec(ctx, "janitor expired", `DELETE FROM threads WHERE expires_at < now()`)
		a.exec(ctx, "janitor inactive", `DELETE FROM threads WHERE last_at < now() - make_interval(days => $1::int)`, a.set.num("retention_days"))
		a.exec(ctx, "janitor contacts", `UPDATE threads SET owner_contact='', visitor_contact='' WHERE revealed_at < now() - interval '24 hours' AND (owner_contact<>'' OR visitor_contact<>'')`)
		a.exec(ctx, "janitor audit", `DELETE FROM audit WHERE created_at < now() - interval '180 days'`)
		a.exec(ctx, "janitor sessions", `DELETE FROM sessions WHERE expires_at < now()`)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

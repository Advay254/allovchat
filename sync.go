package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sync: one cursor to catch a device up on everything that changed for an account.
//
// Every chat, message and removal takes the next number from one database sequence (sync_seq), set by triggers
// (see syncSchema), so no write path can forget it. A device keeps the number it last reached (its cursor) and asks
// for everything after it. The answer carries chats, messages (with delivery and read state), and what was removed.
// Applying a change twice is harmless, which is what makes retries and overlapping requests safe.
//
// Sequence numbers are handed out before a transaction commits, so a slow transaction can commit after a later one.
// To avoid missing it, each request also repeats rows changed in the last syncWindow whose number is within
// syncOverlap below the cursor. Clients key everything by id, so the repeats are ignored.

const (
	syncOverlap = 200
	syncWindow  = "10 seconds"
	syncDefault = 100
	syncMax     = 300
)

// syncSchema is sent as a single statement batch, because it contains function bodies with semicolons.
const syncSchema = `
CREATE SEQUENCE IF NOT EXISTS sync_seq;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT nextval('sync_seq'), ADD COLUMN IF NOT EXISTS changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();
ALTER TABLE messages ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT nextval('sync_seq'), ADD COLUMN IF NOT EXISTS changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();
CREATE TABLE IF NOT EXISTS tombstones(seq BIGINT PRIMARY KEY DEFAULT nextval('sync_seq'), owner_id BIGINT NOT NULL, thread_id BIGINT, link_id BIGINT, changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp());
CREATE INDEX IF NOT EXISTS tombstones_owner ON tombstones(owner_id, seq);
CREATE INDEX IF NOT EXISTS tombstones_age ON tombstones(changed_at);
CREATE INDEX IF NOT EXISTS threads_seq ON threads(seq);
CREATE INDEX IF NOT EXISTS messages_thread_seq ON messages(thread_id, seq);
CREATE OR REPLACE FUNCTION sync_touch() RETURNS trigger AS $$ BEGIN NEW.seq := nextval('sync_seq'); NEW.changed_at := clock_timestamp(); RETURN NEW; END $$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION sync_msg_thread() RETURNS trigger AS $$ BEGIN UPDATE threads SET last_at = last_at WHERE id IN (SELECT DISTINCT thread_id FROM changed_rows); RETURN NULL; END $$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION sync_thread_gone() RETURNS trigger AS $$ DECLARE o BIGINT; BEGIN
  SELECT owner_id INTO o FROM links WHERE id = OLD.link_id;
  IF o IS NOT NULL THEN INSERT INTO tombstones(owner_id, thread_id) VALUES (o, OLD.id); END IF;
  IF OLD.account_id IS NOT NULL THEN INSERT INTO tombstones(owner_id, thread_id) VALUES (OLD.account_id, OLD.id); END IF;
  RETURN OLD; END $$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION sync_thread_unclaimed() RETURNS trigger AS $$ BEGIN INSERT INTO tombstones(owner_id, thread_id) VALUES (OLD.account_id, OLD.id); RETURN NEW; END $$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION sync_priv_msgs() RETURNS trigger AS $$ BEGIN UPDATE messages SET read_at = read_at WHERE thread_id = NEW.id AND read_at IS NOT NULL; RETURN NULL; END $$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION sync_link_gone() RETURNS trigger AS $$ BEGIN INSERT INTO tombstones(owner_id, link_id) VALUES (OLD.owner_id, OLD.id); RETURN OLD; END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS threads_sync ON threads;
CREATE TRIGGER threads_sync BEFORE INSERT OR UPDATE ON threads FOR EACH ROW EXECUTE FUNCTION sync_touch();
DROP TRIGGER IF EXISTS messages_sync ON messages;
CREATE TRIGGER messages_sync BEFORE INSERT OR UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION sync_touch();
DROP TRIGGER IF EXISTS messages_sync_ins ON messages;
CREATE TRIGGER messages_sync_ins AFTER INSERT ON messages REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT EXECUTE FUNCTION sync_msg_thread();
DROP TRIGGER IF EXISTS messages_sync_upd ON messages;
CREATE TRIGGER messages_sync_upd AFTER UPDATE ON messages REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT EXECUTE FUNCTION sync_msg_thread();
DROP TRIGGER IF EXISTS threads_gone ON threads;
CREATE TRIGGER threads_gone BEFORE DELETE ON threads FOR EACH ROW EXECUTE FUNCTION sync_thread_gone();
DROP TRIGGER IF EXISTS threads_unclaimed ON threads;
CREATE TRIGGER threads_unclaimed BEFORE UPDATE OF account_id ON threads FOR EACH ROW WHEN (OLD.account_id IS NOT NULL AND NEW.account_id IS DISTINCT FROM OLD.account_id) EXECUTE FUNCTION sync_thread_unclaimed();
DROP TRIGGER IF EXISTS threads_priv_msgs ON threads;
CREATE TRIGGER threads_priv_msgs AFTER UPDATE OF priv_o, priv_v ON threads FOR EACH ROW WHEN (OLD.priv_o IS DISTINCT FROM NEW.priv_o OR OLD.priv_v IS DISTINCT FROM NEW.priv_v) EXECUTE FUNCTION sync_priv_msgs();
DROP TRIGGER IF EXISTS links_gone ON links;
CREATE TRIGGER links_gone BEFORE DELETE ON links FOR EACH ROW EXECUTE FUNCTION sync_link_gone();
`

func (a *App) syncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sync", a.syncGet)
}

// syncHead is the newest sequence number handed out so far.
func (a *App) syncHead(ctx context.Context) int64 {
	var n int64
	if a.db.QueryRow(ctx, `SELECT last_value FROM sync_seq`).Scan(&n) != nil {
		return 0
	}
	return n
}

func rowsTx(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := rows.FieldDescriptions()
	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(vals))
		for i, v := range vals {
			m[names[i].Name] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// syncQuery builds a page query: the rows after the cursor (cut at the limit) plus, separately, the recent rows just
// below it. Keeping them apart means the repeats can never push new rows out of a page.
func syncQuery(sel, from, where, alias string) string {
	one := func(cond string, tail string) string {
		return "(SELECT " + sel + " FROM " + from + " WHERE " + where + " AND " + cond + " ORDER BY " + alias + ".seq " + tail + ")"
	}
	return one(alias+".seq > $2", "LIMIT $4") + " UNION ALL " +
		one(alias+".seq > $3 AND "+alias+".seq <= $2 AND "+alias+".changed_at > now() - interval '"+syncWindow+"'", "LIMIT 200") + " ORDER BY seq"
}

var syncThreadsSQL = syncQuery(`t.id, CASE WHEN l.owner_id=$1 THEN 'owner' ELSE 'guest' END AS role,
	CASE WHEN l.owner_id=$1 THEN t.alias WHEN t.owner_alias='' THEN l.label ELSE t.owner_alias END AS peer,
	l.label, l.slug, l.id AS link_id, t.status, t.last_at, t.expires_at, t.e2e, t.pub_v AS gpub, l.pubkey AS lpub, t.revealed_at IS NOT NULL AS revealed, t.disappear_secs, COALESCE(cp.pinned,false) AS pinned, COALESCE(cp.muted,false) AS muted,
	(SELECT count(*) FROM messages m WHERE m.thread_id=t.id AND m.side = CASE WHEN l.owner_id=$1 THEN 'v' ELSE 'o' END AND m.read_at IS NULL) AS unread, t.seq`,
	`threads t JOIN links l ON l.id=t.link_id LEFT JOIN chat_prefs cp ON cp.thread_id=t.id AND cp.owner_id=$1`, `(l.owner_id=$1 OR t.account_id=$1) AND t.expires_at>now()`, "t")

// A message's read flag follows the same rule as the chat screen: if the other person turned on privacy mode, or
// turned off read receipts in their own settings, the sender isn't told when their message was read.
var syncMessagesSQL = syncQuery(`m.id, m.thread_id, m.side, m.body, m.created_at AS ts, m.cid,
	(m.read_at IS NOT NULL AND (m.side <> CASE WHEN l.owner_id=$1 THEN 'o' ELSE 'v' END OR NOT CASE
		WHEN l.owner_id=$1 THEN (t.priv_v OR NOT COALESCE((SELECT receipts FROM prefs WHERE owner_id=t.account_id), true))
		ELSE (t.priv_o OR NOT COALESCE((SELECT receipts FROM prefs WHERE owner_id=l.owner_id), true)) END)) AS read,
	m.reply_to, (m.deleted_at IS NOT NULL) AS deleted, m.expires_at, m.attachment_id AS attach,
	COALESCE((SELECT json_agg(json_build_object('side', r.side, 'body', r.body)) FROM reactions r WHERE r.message_id=m.id AND r.body<>''), '[]'::json) AS reactions, m.seq`,
	`messages m JOIN threads t ON t.id=m.thread_id JOIN links l ON l.id=t.link_id`, `(l.owner_id=$1 OR t.account_id=$1) AND t.expires_at>now() AND (m.expires_at IS NULL OR m.expires_at > now())`, "m")

// A removal is skipped if the chat is back and reachable (saved again after being removed), so a repeat can't undo that.
var syncGoneSQL = syncQuery(`tb.thread_id, tb.link_id, tb.seq`, `tombstones tb`,
	`tb.owner_id=$1 AND NOT EXISTS (SELECT 1 FROM threads x JOIN links xl ON xl.id=x.link_id WHERE x.id=tb.thread_id AND (xl.owner_id=$1 OR x.account_id=$1))`, "tb")

// syncChanges returns one page of changes after the cursor, read from a single snapshot.
func (a *App) syncChanges(ctx context.Context, oid, from int64, limit int, overlap bool) (map[string]any, error) {
	lo := from
	if overlap {
		lo = max(from-syncOverlap, 0)
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var head int64
	if err := tx.QueryRow(ctx, `SELECT last_value FROM sync_seq`).Scan(&head); err != nil {
		return nil, err
	}
	if from > head { // the database was reset since this device last synced: it must start over
		return map[string]any{"reset": true, "cursor": 0, "more": false, "threads": []any{}, "messages": []any{}, "gone": []any{}, "gone_links": []any{}}, nil
	}
	threads, err := rowsTx(ctx, tx, syncThreadsSQL, oid, from, lo, limit)
	if err != nil {
		return nil, err
	}
	msgs, err := rowsTx(ctx, tx, syncMessagesSQL, oid, from, lo, limit)
	if err != nil {
		return nil, err
	}
	gone, err := rowsTx(ctx, tx, syncGoneSQL, oid, from, lo, limit)
	if err != nil {
		return nil, err
	}
	var prefs any
	if pr, err := rowsTx(ctx, tx, `SELECT typing, receipts, online, notif_private FROM prefs WHERE owner_id=$1 AND (seq > $2 OR (seq > $3 AND changed_at > now() - interval '`+syncWindow+`'))`, oid, from, lo); err != nil {
		return nil, err
	} else if len(pr) > 0 {
		prefs = pr[0]
	}
	// A page ends at the earliest point where any list was cut short, so nothing between pages is skipped.
	hi, high, cut := int64(0), from, false
	for _, set := range [][]map[string]any{threads, msgs, gone} {
		n, last := 0, int64(0) // only rows past the cursor count towards the limit
		for _, r := range set {
			if s := r["seq"].(int64); s > from {
				n++
				last = s
			}
		}
		if n == 0 {
			continue
		}
		if n >= limit {
			if !cut || last < hi {
				hi = last
			}
			cut = true
		} else if last > high {
			high = last
		}
	}
	if cut {
		high = max(hi, from)
	}
	keep := func(set []map[string]any) []map[string]any {
		out := []map[string]any{}
		for _, r := range set {
			if r["seq"].(int64) <= high {
				out = append(out, r)
			}
		}
		return out
	}
	threads, msgs, gone = keep(threads), keep(msgs), keep(gone)
	goneThreads, goneLinks := []int64{}, []int64{}
	for _, g := range gone {
		if id, ok := g["thread_id"].(int64); ok {
			goneThreads = append(goneThreads, id)
		}
		if id, ok := g["link_id"].(int64); ok {
			goneLinks = append(goneLinks, id)
		}
	}
	return map[string]any{"cursor": high, "more": cut && high > from, "reset": false, "threads": threads, "messages": msgs, "gone": goneThreads, "gone_links": goneLinks, "prefs": prefs}, nil
}

// syncGet is GET /api/sync?cursor=N&limit=N. Start with cursor 0, then keep asking with the cursor from the last answer
// until "more" is false.
func (a *App) syncGet(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	if !a.rl.allow("sync:"+strconv.FormatInt(oid, 10), 240, time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	from, err := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	if r.URL.Query().Get("cursor") == "" {
		from, err = 0, nil
	}
	limit := syncDefault
	if s := r.URL.Query().Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err == nil && (limit < 1 || limit > syncMax) {
			err = strconv.ErrRange
		}
	}
	if err != nil || from < 0 {
		fail(w, 400, "That sync position isn't valid")
		return
	}
	out, err := a.syncChanges(r.Context(), oid, from, limit, true)
	if err != nil {
		a.fail500(w, "sync", err)
		return
	}
	jsonOut(w, 200, out)
}

// syncHas reports whether anything has changed for the account since the cursor.
func (a *App) syncHas(ctx context.Context, oid, from int64) bool {
	out, err := a.syncChanges(ctx, oid, from, 1, false)
	if err != nil {
		return true // when unsure, tell the device to sync
	}
	if out["reset"] == true {
		return true
	}
	return len(out["threads"].([]map[string]any))+len(out["messages"].([]map[string]any))+len(out["gone"].([]int64))+len(out["gone_links"].([]int64)) > 0
}

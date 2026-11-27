package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Message features: replies, reactions, delete for everyone, disappearing messages, per-person privacy settings,
// and pin and mute per chat. With end-to-end encryption the server only ever holds ciphertext. What it does see is
// minimal metadata: which message is quoted or reacted to, who sent what and when, and the chat's disappear timer.

// msgxSchema is sent as one batch (see sync.go for why). It only adds columns and tables.
const msgxSchema = `
ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to BIGINT REFERENCES messages(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS attachment_id TEXT;
ALTER TABLE threads ADD COLUMN IF NOT EXISTS disappear_secs INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS messages_expires ON messages(expires_at) WHERE expires_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS reactions(message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE, side TEXT NOT NULL CHECK (side IN ('o','v')), body TEXT NOT NULL, PRIMARY KEY(message_id, side));
CREATE TABLE IF NOT EXISTS prefs(owner_id BIGINT PRIMARY KEY REFERENCES owners(id) ON DELETE CASCADE, typing BOOLEAN NOT NULL DEFAULT true, receipts BOOLEAN NOT NULL DEFAULT true, online BOOLEAN NOT NULL DEFAULT true, notif_private BOOLEAN NOT NULL DEFAULT true, seq BIGINT NOT NULL DEFAULT nextval('sync_seq'), changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp());
CREATE TABLE IF NOT EXISTS chat_prefs(owner_id BIGINT NOT NULL REFERENCES owners(id) ON DELETE CASCADE, thread_id BIGINT NOT NULL REFERENCES threads(id) ON DELETE CASCADE, pinned BOOLEAN NOT NULL DEFAULT false, muted BOOLEAN NOT NULL DEFAULT false, PRIMARY KEY(owner_id, thread_id));
CREATE INDEX IF NOT EXISTS chat_prefs_thread ON chat_prefs(thread_id);
CREATE TABLE IF NOT EXISTS attachments(id TEXT PRIMARY KEY, thread_id BIGINT NOT NULL REFERENCES threads(id) ON DELETE CASCADE, side TEXT NOT NULL CHECK (side IN ('o','v')), kind TEXT NOT NULL, size INT NOT NULL, attached BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS attachments_thread ON attachments(thread_id);
CREATE TABLE IF NOT EXISTS attach_trash(id TEXT PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE OR REPLACE FUNCTION attach_to_trash() RETURNS trigger AS $$ BEGIN INSERT INTO attach_trash(id) VALUES (OLD.id) ON CONFLICT DO NOTHING; RETURN OLD; END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS attachments_trash ON attachments;
CREATE TRIGGER attachments_trash BEFORE DELETE ON attachments FOR EACH ROW EXECUTE FUNCTION attach_to_trash();
CREATE OR REPLACE FUNCTION sync_touch_message() RETURNS trigger AS $$ BEGIN UPDATE messages SET read_at = read_at WHERE id = COALESCE(NEW.message_id, OLD.message_id); RETURN NULL; END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS reactions_sync ON reactions;
CREATE TRIGGER reactions_sync AFTER INSERT OR UPDATE ON reactions FOR EACH ROW EXECUTE FUNCTION sync_touch_message();
CREATE OR REPLACE FUNCTION sync_touch_thread_id() RETURNS trigger AS $$ BEGIN UPDATE threads SET last_at = last_at WHERE id = NEW.thread_id; RETURN NULL; END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS chat_prefs_sync ON chat_prefs;
CREATE TRIGGER chat_prefs_sync AFTER INSERT OR UPDATE ON chat_prefs FOR EACH ROW EXECUTE FUNCTION sync_touch_thread_id();
DROP TRIGGER IF EXISTS prefs_sync ON prefs;
CREATE TRIGGER prefs_sync BEFORE INSERT OR UPDATE ON prefs FOR EACH ROW EXECUTE FUNCTION sync_touch();
CREATE OR REPLACE FUNCTION sync_receipts_changed() RETURNS trigger AS $$ BEGIN UPDATE messages SET read_at = read_at WHERE read_at IS NOT NULL AND thread_id IN (SELECT t.id FROM threads t JOIN links l ON l.id = t.link_id WHERE l.owner_id = NEW.owner_id OR t.account_id = NEW.owner_id); RETURN NULL; END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS prefs_receipts ON prefs;
CREATE TRIGGER prefs_receipts AFTER UPDATE OF receipts ON prefs FOR EACH ROW WHEN (OLD.receipts IS DISTINCT FROM NEW.receipts) EXECUTE FUNCTION sync_receipts_changed();
`

var disappearOK = map[int]bool{0: true, 300: true, 3600: true, 86400: true, 604800: true}

func (a *App) msgxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/prefs", a.prefsGet)
	mux.HandleFunc("POST /api/prefs", a.prefsSet)
	mux.HandleFunc("POST /api/chats/{id}/prefs", a.chatPrefsSet)
	mux.HandleFunc("GET /api/attach/config", a.attachConfig)
}

// sideFlags says what one person lets the other see.
type sideFlags struct{ Typing, Receipts, Online bool }

// flags combines the chat's privacy mode (which turns all three off for that chat) with the account's own settings.
func (a *App) flags(ctx context.Context, tid int64) (o, v sideFlags) {
	var po, pv bool
	var ot, or, oo, vt, vr, vo bool
	err := a.db.QueryRow(ctx, `SELECT t.priv_o, t.priv_v, COALESCE(a.typing,true), COALESCE(a.receipts,true), COALESCE(a.online,true), COALESCE(b.typing,true), COALESCE(b.receipts,true), COALESCE(b.online,true)
		FROM threads t JOIN links l ON l.id=t.link_id LEFT JOIN prefs a ON a.owner_id=l.owner_id LEFT JOIN prefs b ON b.owner_id=t.account_id WHERE t.id=$1`, tid).Scan(&po, &pv, &ot, &or, &oo, &vt, &vr, &vo)
	if err != nil {
		return sideFlags{true, true, true}, sideFlags{true, true, true}
	}
	return sideFlags{!po && ot, !po && or, !po && oo}, sideFlags{!pv && vt, !pv && vr, !pv && vo}
}

func (a *App) sideOf(ctx context.Context, tid int64, side string) sideFlags {
	o, v := a.flags(ctx, tid)
	if side == "v" {
		return v
	}
	return o
}

// notifyMuteAware sends a push or app event unless the person it is for has muted this chat.
func (a *App) notifyMuteAware(tid int64, to, kind string) {
	if a.push == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var muted bool
		err := a.db.QueryRow(ctx, `SELECT COALESCE(cp.muted,false) FROM threads t JOIN links l ON l.id=t.link_id
			LEFT JOIN chat_prefs cp ON cp.thread_id=t.id AND cp.owner_id = CASE WHEN $2::text='o' THEN l.owner_id ELSE t.account_id END WHERE t.id=$1`, tid, to).Scan(&muted)
		if err == nil && muted {
			return
		}
		a.notify(tid, to, kind)
	}()
}

// ---------- reactions ----------

// react sets this side's reaction on a message. It sets an exact state, so repeating the request changes nothing.
// An empty body removes the reaction. In an encrypted chat the reaction is ciphertext like any message.
func (a *App) react(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct {
		Mid  int64  `json:"mid"`
		Body string `json:"body"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.rl.allow("rx:"+strconv.FormatInt(tid, 10)+side, 60, time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	ctx := r.Context()
	var secure bool
	var status string
	if err := a.db.QueryRow(ctx, `SELECT e2e, status FROM threads WHERE id=$1`, tid).Scan(&secure, &status); err != nil {
		fail(w, 404, "Not found")
		return
	}
	if status != "open" {
		fail(w, 403, "This chat isn't open")
		return
	}
	body := in.Body
	switch {
	case body == "":
	case secure:
		if !e2eBody.MatchString(body) || len(body) > 512 {
			fail(w, 400, "That reaction couldn't be sent")
			return
		}
	default:
		if body = clean(body); body == "" || utf8.RuneCountInString(body) > 8 {
			fail(w, 400, "That reaction couldn't be sent")
			return
		}
	}
	ct, err := a.db.Exec(ctx, `INSERT INTO reactions(message_id, side, body) SELECT m.id, $3, $4 FROM messages m WHERE m.id=$1 AND m.thread_id=$2 AND m.deleted_at IS NULL
		ON CONFLICT (message_id, side) DO UPDATE SET body=EXCLUDED.body WHERE reactions.body IS DISTINCT FROM EXCLUDED.body`, in.Mid, tid, side, body)
	if err != nil {
		a.fail500(w, "react", err)
		return
	}
	if ct.RowsAffected() == 0 {
		var n int
		if a.db.QueryRow(ctx, `SELECT 1 FROM messages WHERE id=$1 AND thread_id=$2 AND deleted_at IS NULL`, in.Mid, tid).Scan(&n) != nil {
			fail(w, 404, "Not found")
			return
		}
		jsonOut(w, 200, map[string]any{"ok": true}) // already exactly this
		return
	}
	a.hub.pub(tid, map[string]any{"t": "react", "id": in.Mid, "side": side, "body": body})
	jsonOut(w, 200, map[string]any{"ok": true})
}

// ---------- delete for everyone ----------

// unsend removes one of the caller's own messages for both people. The text, any attachment and the reactions are
// erased on the server; what stays is an empty marker, so other devices learn it was deleted. Repeating it changes nothing.
func (a *App) unsend(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct {
		Mid int64 `json:"mid"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "unsend begin", err)
		return
	}
	defer tx.Rollback(ctx)
	var att *string
	var already bool
	err = tx.QueryRow(ctx, `SELECT attachment_id, deleted_at IS NOT NULL FROM messages WHERE id=$1 AND thread_id=$2 AND side=$3 FOR UPDATE`, in.Mid, tid, side).Scan(&att, &already)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "Not found") // also what you get for someone else's message
		return
	}
	if err != nil {
		a.fail500(w, "unsend lookup", err)
		return
	}
	if already {
		jsonOut(w, 200, map[string]any{"ok": true})
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE messages SET deleted_at=now(), body='', attachment_id=NULL, reply_to=NULL WHERE id=$1`, in.Mid); err != nil {
		a.fail500(w, "unsend", err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM reactions WHERE message_id=$1`, in.Mid); err != nil {
		a.fail500(w, "unsend reactions", err)
		return
	}
	if att != nil {
		if _, err = tx.Exec(ctx, `DELETE FROM attachments WHERE id=$1`, *att); err != nil {
			a.fail500(w, "unsend attachment", err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail500(w, "unsend commit", err)
		return
	}
	a.hub.pub(tid, map[string]any{"t": "del", "id": in.Mid})
	jsonOut(w, 200, map[string]any{"ok": true})
}

// ---------- disappearing messages ----------

// setDisappear sets how long new messages in this chat last before they are removed. Either person can change it.
// It only affects messages sent afterwards, and the timer starts when a message is sent.
func (a *App) setDisappear(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	var in struct {
		Secs int `json:"secs"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !disappearOK[in.Secs] {
		fail(w, 400, "Pick one of the listed times")
		return
	}
	if !a.exec(r.Context(), "disappear", `UPDATE threads SET disappear_secs=$2 WHERE id=$1 AND disappear_secs<>$2`, tid, in.Secs) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	a.hub.pub(tid, map[string]any{"t": "disappear", "secs": in.Secs})
	jsonOut(w, 200, map[string]any{"ok": true})
}

// purgeExpiredMessages removes messages whose timer ran out, with their attachments. Clients already hide them by
// expires_at, so this is housekeeping.
func (a *App) purgeExpiredMessages(ctx context.Context) {
	a.exec(ctx, "purge expired attachments", `DELETE FROM attachments WHERE id IN (SELECT attachment_id FROM messages WHERE expires_at < now() AND attachment_id IS NOT NULL)`)
	a.exec(ctx, "purge expired messages", `DELETE FROM messages WHERE expires_at < now()`)
}

// ---------- settings that belong to a person ----------

func (a *App) prefsGet(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	jsonOut(w, 200, a.loadPrefs(r.Context(), oid))
}

func (a *App) loadPrefs(ctx context.Context, oid int64) map[string]any {
	p := map[string]any{"typing": true, "receipts": true, "online": true, "notif_private": true}
	var t, rc, on, np bool
	if a.db.QueryRow(ctx, `SELECT typing, receipts, online, notif_private FROM prefs WHERE owner_id=$1`, oid).Scan(&t, &rc, &on, &np) == nil {
		p["typing"], p["receipts"], p["online"], p["notif_private"] = t, rc, on, np
	}
	return p
}

// prefsSet changes any of the four settings. They are exact states, so repeating a request changes nothing.
func (a *App) prefsSet(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct {
		Typing       *bool `json:"typing"`
		Receipts     *bool `json:"receipts"`
		Online       *bool `json:"online"`
		NotifPrivate *bool `json:"notif_private"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	pick := func(v *bool) any {
		if v == nil {
			return nil
		}
		return *v
	}
	if !a.exec(r.Context(), "prefs", `INSERT INTO prefs(owner_id, typing, receipts, online, notif_private) VALUES($1, COALESCE($2,true), COALESCE($3,true), COALESCE($4,true), COALESCE($5,true))
		ON CONFLICT (owner_id) DO UPDATE SET typing=COALESCE($2, prefs.typing), receipts=COALESCE($3, prefs.receipts), online=COALESCE($4, prefs.online), notif_private=COALESCE($5, prefs.notif_private)`,
		oid, pick(in.Typing), pick(in.Receipts), pick(in.Online), pick(in.NotifPrivate)) {
		fail(w, 500, "Something went wrong on our side. Please try again in a moment.")
		return
	}
	jsonOut(w, 200, a.loadPrefs(r.Context(), oid))
}

// chatPrefsSet pins or mutes a chat for the signed-in person only. The other person never sees it.
func (a *App) chatPrefsSet(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	tid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	var in struct {
		Pinned *bool `json:"pinned"`
		Muted  *bool `json:"muted"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Pinned == nil && in.Muted == nil {
		fail(w, 400, "Nothing to change")
		return
	}
	pick := func(v *bool) any {
		if v == nil {
			return nil
		}
		return *v
	}
	ct, err := a.db.Exec(r.Context(), `INSERT INTO chat_prefs(owner_id, thread_id, pinned, muted)
		SELECT $1, t.id, COALESCE($3,false), COALESCE($4,false) FROM threads t JOIN links l ON l.id=t.link_id WHERE t.id=$2 AND t.expires_at>now() AND (l.owner_id=$1 OR t.account_id=$1)
		ON CONFLICT (owner_id, thread_id) DO UPDATE SET pinned=COALESCE($3, chat_prefs.pinned), muted=COALESCE($4, chat_prefs.muted)`, oid, tid, pick(in.Pinned), pick(in.Muted))
	if err != nil {
		a.fail500(w, "chat prefs", err)
		return
	}
	if ct.RowsAffected() == 0 {
		fail(w, 404, "Not found")
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

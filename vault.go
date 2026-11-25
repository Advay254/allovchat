package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Encrypted key vault.
//
// Every account has one master key (MK), 256 random bits made in the client. The server never sees it in plain form.
// It stores MK twice, each copy sealed with AES-256-GCM by the client:
//
//	wrapped_pw  sealed with a key stretched from the account password (PBKDF2-SHA256, per-account salt, iteration count stored here)
//	wrapped_rc  sealed with a key derived from the recovery code (a random code shown once, at least 128 bits)
//
// Each chat key (the private half of a link's key pair, or a guest's key for a chat saved to the account) is sealed with MK
// in the client and stored here as an opaque blob, tied to the link or chat it belongs to. The server only ever holds
// ciphertext, salts, iteration counts and version numbers, and cannot read a chat key. See docs/encryption.md for the protocol,
// including what this does and does not protect against.
//
// The recovery code also derives a second value (rc_auth) that proves the person has the code. The server keeps only a hash of it.

const (
	vaultIterMin  = 200000
	vaultIterMax  = 5000000
	vaultBatchMax = 20   // keys per request; with vaultBlobMax this stays well under the 64 KB request limit
	vaultBlobMax  = 2048 // a sealed key pair is about 1 KB
	vaultKeysMax  = 500  // sealed keys per account
)

// vaultIter is the iteration count new vaults should use. Raise it with VAULT_KDF_ITER as phones get faster.
// Each vault stores the count it was made with, so changing this never locks anyone out.
var vaultIter = func() int {
	if n, err := strconv.Atoi(os.Getenv("VAULT_KDF_ITER")); err == nil && n >= vaultIterMin && n <= vaultIterMax {
		return n
	}
	return 600000
}()

var (
	// vaultBlobRe matches a sealed key or master key: "k1." + 12 byte IV + ciphertext, all base64url.
	vaultBlobRe = regexp.MustCompile(`^k1\.[A-Za-z0-9_-]{16}\.[A-Za-z0-9_-]{22,}$`)
	vaultSaltRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22,86}$`) // 16 to 64 random bytes
	vaultAuthRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)    // 32 bytes
)

func vaultBlobOK(s string) bool { return len(s) <= vaultBlobMax && vaultBlobRe.MatchString(s) }

// dbx is what both the pool and a transaction can do, so helpers work in either.
type dbx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (a *App) vaultRoutes(mux *http.ServeMux) {
	// Only GET and POST: the Android app's WebView is allowed those two methods across origins.
	mux.HandleFunc("GET /api/vault", a.vaultGet)
	mux.HandleFunc("POST /api/vault/create", a.vaultCreate)
	mux.HandleFunc("POST /api/vault/password", a.vaultPassword)
	mux.HandleFunc("GET /api/vault/status", a.vaultStatus)
	mux.HandleFunc("GET /api/vault/keys", a.vaultKeys)
	mux.HandleFunc("POST /api/vault/keys", a.vaultKeysPut)
	mux.HandleFunc("POST /api/vault/recover/open", a.vaultRecoverOpen)
	mux.HandleFunc("POST /api/vault/recover/finish", a.vaultRecoverFinish)
}

// vaultIn is the sealed material a client sends when it makes a vault or replaces its password copy.
type vaultIn struct {
	Iter      int    `json:"iter"`
	Salt      string `json:"salt"`
	WrappedPW string `json:"wrapped_pw"`
	WrappedRC string `json:"wrapped_rc"`
	RCAuth    string `json:"rc_auth"`
	RCDisplay string `json:"rc_display"`
}

func vaultKDFOK(iter int, salt, wrappedPW string) bool {
	return iter >= vaultIterMin && iter <= vaultIterMax && vaultSaltRe.MatchString(salt) && vaultBlobOK(wrappedPW)
}

func (v vaultIn) ok() bool {
	return vaultKDFOK(v.Iter, v.Salt, v.WrappedPW) && vaultBlobOK(v.WrappedRC) && vaultAuthRe.MatchString(v.RCAuth) && vaultBlobOK(v.RCDisplay)
}

const vaultBad = "That vault couldn't be saved. Refresh and try again."

// vaultGet returns what a client needs to unlock the vault after signing in. It never returns the recovery copy.
func (a *App) vaultGet(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	out := map[string]any{"id": oid, "exists": false, "recommended_iter": vaultIter, "min_iter": vaultIterMin}
	var iter, ver int
	var salt, wpw, disp string
	err := a.db.QueryRow(r.Context(), `SELECT version, kdf_iter, kdf_salt, wrapped_pw, rc_display FROM vaults WHERE owner_id=$1`, oid).Scan(&ver, &iter, &salt, &wpw, &disp)
	if err == nil {
		out["exists"], out["version"] = true, ver
		out["kdf"] = map[string]any{"name": "PBKDF2-SHA256", "iter": iter, "salt": salt}
		out["wrapped_pw"], out["rc_display"] = wpw, disp
	} else if !errors.Is(err, pgx.ErrNoRows) {
		a.fail500(w, "vault get", err)
		return
	}
	jsonOut(w, 200, out)
}

// vaultCreate makes the account's vault. It is idempotent: sending the same vault again succeeds, a different one is refused.
func (a *App) vaultCreate(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	if !a.rl.allow("vc:"+strconv.FormatInt(oid, 10), 10, time.Hour) {
		fail(w, 429, "Too many attempts. Try again later")
		return
	}
	var in vaultIn
	if !readJSON(w, r, &in) {
		return
	}
	if !in.ok() {
		fail(w, 400, vaultBad)
		return
	}
	ctx := r.Context()
	ct, err := a.db.Exec(ctx, `INSERT INTO vaults(owner_id,kdf_iter,kdf_salt,wrapped_pw,wrapped_rc,rc_auth_hash,rc_display) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (owner_id) DO NOTHING`,
		oid, in.Iter, in.Salt, in.WrappedPW, in.WrappedRC, sha256hex(in.RCAuth), in.RCDisplay)
	if err != nil {
		a.fail500(w, "vault create", err)
		return
	}
	if ct.RowsAffected() == 0 {
		var have string
		if a.db.QueryRow(ctx, `SELECT wrapped_pw FROM vaults WHERE owner_id=$1`, oid).Scan(&have) == nil && have == in.WrappedPW {
			jsonOut(w, 200, map[string]any{"ok": true}) // the same request again
			return
		}
		fail(w, 409, "You already have a key vault")
		return
	}
	a.audit(ctx, "vault created", "owner #"+strconv.FormatInt(oid, 10))
	jsonOut(w, 200, map[string]any{"ok": true})
}

// lockWait applies the same limits sign-in uses (5 failures per account and address, 20 per address) to a password or
// recovery check. The prefix keeps those attempts apart from ordinary sign-ins.
func (a *App) lockWait(prefix, user, ip string) (pair, acct, byIP string, wait time.Duration) {
	pair, acct, byIP = prefix+"p:"+user+"|"+ip, prefix+"a:"+user, prefix+"i:"+ip
	wait = max(a.fails.wait(pair, 5), a.fails.wait(byIP, 20))
	return
}

// currentSession returns the hash of the session making this request, so it can be kept when others are signed out.
func currentSession(r *http.Request) string {
	if c, err := r.Cookie("os"); err == nil && len(c.Value) == 48 {
		return sha256hex(c.Value)
	}
	if t := bearerToken(r); t != "" {
		return sha256hex(t)
	}
	return ""
}

// vaultPassword changes the account password and replaces the password copy of the master key in one step.
// The client unwraps with the old password and re-wraps with the new one, so no chat is lost. Other devices are signed out.
func (a *App) vaultPassword(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	var in struct {
		Current   string `json:"current_password"`
		New       string `json:"new_password"`
		Version   int    `json:"version"`
		Iter      int    `json:"iter"`
		Salt      string `json:"salt"`
		WrappedPW string `json:"wrapped_pw"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.New) < 8 || len(in.New) > 200 {
		fail(w, 400, "Password needs 8 to 200 characters")
		return
	}
	if !vaultKDFOK(in.Iter, in.Salt, in.WrappedPW) || in.Version < 1 {
		fail(w, 400, vaultBad)
		return
	}
	ctx := r.Context()
	var user, hash string
	if err := a.db.QueryRow(ctx, `SELECT username, pass FROM owners WHERE id=$1`, oid).Scan(&user, &hash); err != nil {
		a.fail500(w, "vault password lookup", err)
		return
	}
	ip := clientIP(r)
	pair, acct, byIP, wait := a.lockWait("pw", user, ip)
	if wait > 0 {
		tooMany(w, wait)
		return
	}
	if !checkPass(in.Current, hash) {
		a.fails.fail(pair, acct, byIP)
		if w2 := a.fails.wait(pair, 5); w2 > 0 {
			tooMany(w, w2)
			return
		}
		fail(w, 401, "That isn't your current password")
		return
	}
	a.fails.reset(pair, acct)
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "vault password begin", err)
		return
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE vaults SET kdf_iter=$2, kdf_salt=$3, wrapped_pw=$4, version=version+1, updated_at=now() WHERE owner_id=$1 AND version=$5`, oid, in.Iter, in.Salt, in.WrappedPW, in.Version)
	if err != nil {
		a.fail500(w, "vault password update", err)
		return
	}
	if ct.RowsAffected() == 0 {
		var n int
		if tx.QueryRow(ctx, `SELECT 1 FROM vaults WHERE owner_id=$1`, oid).Scan(&n) != nil {
			fail(w, 409, "Set up your key vault first")
		} else {
			fail(w, 409, "Your vault changed on another device. Refresh and try again.")
		}
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE owners SET pass=$2 WHERE id=$1`, oid, hashPass(in.New)); err != nil {
		a.fail500(w, "vault password hash", err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE owner_id=$1 AND id<>$2`, oid, currentSession(r)); err != nil {
		a.fail500(w, "vault password sessions", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail500(w, "vault password commit", err)
		return
	}
	a.cache.delPrefix("s:")
	a.audit(ctx, "password changed", "owner #"+strconv.FormatInt(oid, 10))
	jsonOut(w, 200, map[string]any{"ok": true})
}

// vaultStatus says which links and chats have a sealed key in the vault, without sending the keys.
// A client combines it with what it holds locally: key here = present, in the vault only = restorable, neither = missing.
func (a *App) vaultStatus(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := map[string]any{"exists": false}
	var ver int
	if err := a.db.QueryRow(ctx, `SELECT version FROM vaults WHERE owner_id=$1`, oid).Scan(&ver); err == nil {
		out["exists"], out["version"] = true, ver
	} else if !errors.Is(err, pgx.ErrNoRows) {
		a.fail500(w, "vault status", err)
		return
	}
	rows, err := a.db.Query(ctx, `SELECT link_id, thread_id FROM vault_keys WHERE owner_id=$1 ORDER BY id LIMIT $2`, oid, vaultKeysMax)
	if err != nil {
		a.fail500(w, "vault status keys", err)
		return
	}
	defer rows.Close()
	links, chats := []int64{}, []int64{}
	for rows.Next() {
		var l, t *int64
		if err := rows.Scan(&l, &t); err != nil {
			a.fail500(w, "vault status scan", err)
			return
		}
		if l != nil {
			links = append(links, *l)
		} else if t != nil {
			chats = append(chats, *t)
		}
	}
	if err := rows.Err(); err != nil {
		a.fail500(w, "vault status rows", err)
		return
	}
	out["links"], out["chats"] = links, chats
	jsonOut(w, 200, out)
}

// vaultKeys returns the account's sealed keys so a new device can restore them.
func (a *App) vaultKeys(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	out, err := a.rowsOut(r.Context(), `SELECT CASE WHEN link_id IS NOT NULL THEN 'link' ELSE 'chat' END AS kind, COALESCE(link_id, thread_id) AS ref, blob, updated_at FROM vault_keys WHERE owner_id=$1 ORDER BY id LIMIT $2`, oid, vaultKeysMax)
	if err != nil {
		a.fail500(w, "vault keys", err)
		return
	}
	jsonOut(w, 200, map[string]any{"keys": out})
}

// putVaultKey stores one sealed key, but only for a link the account owns or a chat saved to the account.
// Storing the same key again just refreshes it. It reports false when the link or chat isn't theirs.
func putVaultKey(ctx context.Context, q dbx, oid int64, kind string, ref int64, blob string) (bool, error) {
	var sql string
	switch kind {
	case "link":
		sql = `INSERT INTO vault_keys(owner_id, link_id, blob) SELECT $1, l.id, $3 FROM links l WHERE l.id=$2 AND l.owner_id=$1
			ON CONFLICT (owner_id, link_id) WHERE link_id IS NOT NULL DO UPDATE SET blob=EXCLUDED.blob, updated_at=now()`
	case "chat":
		sql = `INSERT INTO vault_keys(owner_id, thread_id, blob) SELECT $1, t.id, $3 FROM threads t WHERE t.id=$2 AND t.account_id=$1 AND t.expires_at>now()
			ON CONFLICT (owner_id, thread_id) WHERE thread_id IS NOT NULL DO UPDATE SET blob=EXCLUDED.blob, updated_at=now()`
	default:
		return false, nil
	}
	ct, err := q.Exec(ctx, sql, oid, ref, blob)
	return err == nil && ct.RowsAffected() > 0, err
}

func vaultExists(ctx context.Context, q dbx, oid int64) bool {
	var n int
	return q.QueryRow(ctx, `SELECT 1 FROM vaults WHERE owner_id=$1`, oid).Scan(&n) == nil
}

// vaultKeysPut stores up to 20 sealed keys at once, all or nothing.
func (a *App) vaultKeysPut(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.owner(w, r)
	if !ok {
		return
	}
	if !a.rl.allow("vk:"+strconv.FormatInt(oid, 10), 120, time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	var in struct {
		Keys []struct {
			Kind string `json:"kind"`
			Ref  int64  `json:"ref"`
			Blob string `json:"blob"`
		} `json:"keys"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Keys) == 0 || len(in.Keys) > vaultBatchMax {
		fail(w, 400, vaultBad)
		return
	}
	for _, k := range in.Keys {
		if (k.Kind != "link" && k.Kind != "chat") || k.Ref < 1 || !vaultBlobOK(k.Blob) {
			fail(w, 400, vaultBad)
			return
		}
	}
	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "vault keys begin", err)
		return
	}
	defer tx.Rollback(ctx)
	if !vaultExists(ctx, tx, oid) {
		fail(w, 409, "Set up your key vault first")
		return
	}
	for _, k := range in.Keys {
		stored, err := putVaultKey(ctx, tx, oid, k.Kind, k.Ref, k.Blob)
		if err != nil {
			a.fail500(w, "vault key put", err)
			return
		}
		if !stored {
			fail(w, 404, "Not found") // the same answer whether it doesn't exist or isn't theirs
			return
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM vault_keys WHERE owner_id=$1`, oid).Scan(&n); err != nil {
		a.fail500(w, "vault key count", err)
		return
	}
	if n > vaultKeysMax {
		fail(w, 403, "Your vault is full")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail500(w, "vault keys commit", err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// ---------- recovery: for someone who forgot the password but kept the recovery code ----------

const recoverFail = "That recovery code didn't work"

// recoverCheck verifies the proof derived from the recovery code. An unknown or disabled account gets the same
// answer and the same work as a wrong code, so the endpoint doesn't reveal which usernames exist.
// It returns the account id, the sealed recovery copy and the stored proof hash on success.
func (a *App) recoverCheck(w http.ResponseWriter, r *http.Request, user, rcAuth string) (id int64, wrapped, stored string, ok bool) {
	user = strings.ToLower(clean(user))
	ip := clientIP(r)
	if !a.rl.allow("rcreq:"+ip, 30, time.Hour) {
		fail(w, 429, "Too many attempts. Try again later")
		return
	}
	pair, acct, byIP, wait := a.lockWait("rc", user, ip)
	if wait > 0 {
		tooMany(w, wait)
		return
	}
	stored = sha256hex("no such account") // compared even when there is no account, so timing doesn't give it away
	var found bool
	if vaultAuthRe.MatchString(rcAuth) {
		var h, wr string
		var dis bool
		err := a.db.QueryRow(r.Context(), `SELECT o.id, o.disabled, v.rc_auth_hash, v.wrapped_rc FROM owners o JOIN vaults v ON v.owner_id=o.id WHERE o.username=$1`, user).Scan(&id, &dis, &h, &wr)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.fail500(w, "recover lookup", err)
			return 0, "", "", false
		}
		if err == nil && !dis {
			found, stored, wrapped = true, h, wr
		}
	}
	match := subtle.ConstantTimeCompare([]byte(sha256hex(rcAuth)), []byte(stored)) == 1
	if !found || !match {
		a.fails.fail(pair, acct, byIP)
		if w2 := a.fails.wait(pair, 5); w2 > 0 {
			tooMany(w, w2)
			return 0, "", "", false
		}
		fail(w, 401, recoverFail)
		return 0, "", "", false
	}
	return id, wrapped, stored, true
}

// vaultRecoverOpen checks the recovery proof and hands back the recovery copy of the master key,
// which only the real recovery code can open.
func (a *App) vaultRecoverOpen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		RCAuth   string `json:"rc_auth"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	_, wrapped, _, ok := a.recoverCheck(w, r, in.Username, in.RCAuth)
	if !ok {
		return
	}
	jsonOut(w, 200, map[string]any{"wrapped_rc": wrapped})
}

// vaultRecoverFinish sets a new password and a new recovery code in one step, and signs every device out.
// It only works once per recovery code: the old proof stops matching as soon as it succeeds.
func (a *App) vaultRecoverFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username  string `json:"username"`
		RCAuth    string `json:"rc_auth"`
		Password  string `json:"new_password"`
		Iter      int    `json:"iter"`
		Salt      string `json:"salt"`
		WrappedPW string `json:"wrapped_pw"`
		WrappedRC string `json:"wrapped_rc"`
		NewRCAuth string `json:"new_rc_auth"`
		RCDisplay string `json:"rc_display"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	next := vaultIn{in.Iter, in.Salt, in.WrappedPW, in.WrappedRC, in.NewRCAuth, in.RCDisplay}
	if len(in.Password) < 8 || len(in.Password) > 200 {
		fail(w, 400, "Password needs 8 to 200 characters")
		return
	}
	if !next.ok() {
		fail(w, 400, vaultBad)
		return
	}
	id, _, old, ok := a.recoverCheck(w, r, in.Username, in.RCAuth)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.fail500(w, "recover begin", err)
		return
	}
	defer tx.Rollback(ctx)
	// The old proof hash is part of the condition, so a repeat of this request after it succeeded changes nothing.
	ct, err := tx.Exec(ctx, `UPDATE vaults SET kdf_iter=$2, kdf_salt=$3, wrapped_pw=$4, wrapped_rc=$5, rc_auth_hash=$6, rc_display=$7, version=version+1, updated_at=now() WHERE owner_id=$1 AND rc_auth_hash=$8`,
		id, next.Iter, next.Salt, next.WrappedPW, next.WrappedRC, sha256hex(next.RCAuth), next.RCDisplay, old)
	if err != nil {
		a.fail500(w, "recover vault", err)
		return
	}
	if ct.RowsAffected() == 0 {
		fail(w, 401, recoverFail)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE owners SET pass=$2 WHERE id=$1`, id, hashPass(in.Password)); err != nil {
		a.fail500(w, "recover password", err)
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE owner_id=$1`, id); err != nil {
		a.fail500(w, "recover sessions", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail500(w, "recover commit", err)
		return
	}
	user := strings.ToLower(clean(in.Username))
	a.fails.reset("rcp:"+user+"|"+clientIP(r), "rca:"+user, "p:"+user+"|"+clientIP(r), "a:"+user)
	a.cache.delPrefix("s:")
	a.audit(ctx, "account recovered", "owner #"+strconv.FormatInt(id, 10))
	jsonOut(w, 200, map[string]any{"ok": true})
}

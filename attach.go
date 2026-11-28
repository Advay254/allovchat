package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Encrypted attachments (images, files, voice notes).
//
// The sender's browser or app encrypts the file with the chat key before uploading, so the server stores an opaque blob.
// It never learns the file name, type or contents, only the size and the kind the client declares ("image", "file" or
// "voice", used for size limits). The message that carries the attachment holds the details, encrypted like any message.
// A blob is only served to the two people in the chat, and it is deleted with the message, the chat, or an hour after
// an upload that never got attached to a message.
//
// Where blobs live is set with environment variables. Either:
//   ATTACH_DIR                                     a folder on a disk that persists (not available on free Render)
//   S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY, S3_REGION   any S3-compatible bucket, such as Cloudflare R2
// With neither set, attachments are off. ATTACH_MAX_MB (default 8, at most 25) is the largest upload.

type blobStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

var (
	attachOverride blobStore // set by tests
	attachOnce     sync.Once
	attachChosen   blobStore
	attachKeyRe    = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

func attachBackend() blobStore {
	if attachOverride != nil {
		return attachOverride
	}
	attachOnce.Do(func() {
		if dir := os.Getenv("ATTACH_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err == nil {
				attachChosen = &diskStore{dir: dir}
			} else {
				logErr("attachments folder", err)
			}
			return
		}
		ep, bucket, ak, sk := os.Getenv("S3_ENDPOINT"), os.Getenv("S3_BUCKET"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY")
		if ep != "" && bucket != "" && ak != "" && sk != "" {
			region := os.Getenv("S3_REGION")
			if region == "" {
				region = "auto"
			}
			attachChosen = &s3Store{endpoint: strings.TrimRight(ep, "/"), region: region, bucket: bucket, access: ak, secret: sk, client: &http.Client{Timeout: 60 * time.Second}}
		}
	})
	return attachChosen
}

func attachMax() int {
	n, err := strconv.Atoi(os.Getenv("ATTACH_MAX_MB"))
	if err != nil || n < 1 {
		n = 8
	}
	return min(n, 25) << 20
}

func attachLimit(kind string) int {
	if kind == "voice" {
		return min(attachMax(), 4<<20)
	}
	return attachMax()
}

// ---------- storage on a disk ----------

type diskStore struct{ dir string }

func (d *diskStore) path(key string) (string, error) {
	if !attachKeyRe.MatchString(key) {
		return "", errors.New("bad key")
	}
	return filepath.Join(d.dir, key[:2], key), nil
}

func (d *diskStore) Put(_ context.Context, key string, data []byte) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (d *diskStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (d *diskStore) Delete(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---------- storage in an S3-compatible bucket ----------

type s3Store struct {
	endpoint, region, bucket, access, secret string
	client                                   *http.Client
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// sigV4 signs a request the way AWS Signature Version 4 describes. extra holds any further headers to sign (lowercase names).
func sigV4(method, host, path, query string, extra map[string]string, payloadHash, amzDate, region, service, access, secret string) string {
	hdrs := map[string]string{"host": host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate}
	for k, v := range extra {
		hdrs[k] = v
	}
	names := make([]string, 0, len(hdrs))
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + strings.TrimSpace(hdrs[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	creq := strings.Join([]string{method, path, query, canon.String(), signed, payloadHash}, "\n")
	day := amzDate[:8]
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(creq))
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	return "AWS4-HMAC-SHA256 Credential=" + access + "/" + scope + ",SignedHeaders=" + signed + ",Signature=" + sig
}

func (s *s3Store) do(ctx context.Context, method, key string, body []byte) (*http.Response, error) {
	if !attachKeyRe.MatchString(key) {
		return nil, errors.New("bad key")
	}
	u, err := url.Parse(s.endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("bad S3_ENDPOINT")
	}
	path := "/" + s.bucket + "/" + key
	hash := sha256Hex(body)
	date := time.Now().UTC().Format("20060102T150405Z")
	req, err := http.NewRequestWithContext(ctx, method, u.Scheme+"://"+u.Host+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", date)
	req.Header.Set("x-amz-content-sha256", hash)
	req.Header.Set("Authorization", sigV4(method, u.Host, path, "", nil, hash, date, s.region, "s3", s.access, s.secret))
	return s.client.Do(req)
}

func (s *s3Store) Put(ctx context.Context, key string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPut, key, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("bucket answered %d", resp.StatusCode)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("bucket answered %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 && resp.StatusCode != 404 {
		return fmt.Errorf("bucket answered %d", resp.StatusCode)
	}
	return nil
}

// ---------- endpoints ----------

func (a *App) attachConfig(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"enabled": attachBackend() != nil, "max_bytes": map[string]int{"image": attachLimit("image"), "file": attachLimit("file"), "voice": attachLimit("voice")}})
}

// attachUpload takes one encrypted blob (base64 inside JSON, like every other request) for a chat the caller is in.
func (a *App) attachUpload(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	st := attachBackend()
	if st == nil {
		fail(w, 503, "Attachments aren't turned on for this server")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(attachMax())*4/3+4096)
	var in struct {
		Kind string `json:"kind"`
		Data string `json:"data"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		var mx *http.MaxBytesError
		if errors.As(err, &mx) {
			fail(w, 413, "That file is too large")
		} else {
			fail(w, 400, "That upload couldn't be read")
		}
		return
	}
	if in.Kind != "image" && in.Kind != "file" && in.Kind != "voice" {
		fail(w, 400, "That upload couldn't be read")
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) < 29 { // an encrypted blob is at least an IV plus a tag
		fail(w, 400, "That upload couldn't be read")
		return
	}
	if len(data) > attachLimit(in.Kind) {
		fail(w, 413, "That file is too large")
		return
	}
	if !a.rl.allow("up:"+strconv.FormatInt(tid, 10)+side, 12, 10*time.Minute) {
		fail(w, 429, "Slow down a little")
		return
	}
	ctx := r.Context()
	var secure bool
	var status string
	var used int64
	if err := a.db.QueryRow(ctx, `SELECT t.e2e, t.status, (SELECT COALESCE(sum(size),0) FROM attachments WHERE thread_id=t.id) FROM threads t WHERE t.id=$1`, tid).Scan(&secure, &status, &used); err != nil {
		fail(w, 404, "Not found")
		return
	}
	if status != "open" {
		fail(w, 403, "This chat isn't open")
		return
	}
	if !secure {
		fail(w, 400, "Attachments need an encrypted chat")
		return
	}
	if used+int64(len(data)) > int64(attachMax())*6 {
		fail(w, 403, "This chat has reached its attachment limit")
		return
	}
	id := randHex(16)
	if err := st.Put(ctx, id, data); err != nil {
		a.fail500(w, "attachment store", err)
		return
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO attachments(id, thread_id, side, kind, size) VALUES($1,$2,$3,$4,$5)`, id, tid, side, in.Kind, len(data)); err != nil {
		st.Delete(ctx, id)
		a.fail500(w, "attachment row", err)
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "size": len(data)})
}

// attachDownload serves a blob to the two people in the chat. Whoever uploaded it can fetch it before it is attached.
func (a *App) attachDownload(w http.ResponseWriter, r *http.Request, tid int64, side string) {
	st := attachBackend()
	id := r.PathValue("aid")
	if st == nil || !attachKeyRe.MatchString(id) {
		fail(w, 404, "Not found")
		return
	}
	var size int
	if a.db.QueryRow(r.Context(), `SELECT size FROM attachments WHERE id=$1 AND thread_id=$2 AND (attached OR side=$3)`, id, tid, side).Scan(&size) != nil {
		fail(w, 404, "Not found")
		return
	}
	rc, err := st.Get(r.Context(), id)
	if err != nil {
		fail(w, 404, "Not found")
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.Itoa(size))
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	h.Set("Content-Disposition", "attachment")
	io.Copy(w, rc)
}

// sweepAttachments removes uploads that never got attached to a message, then deletes the stored blobs of every
// attachment row that is gone (deleted message, expired message, ended chat).
func (a *App) sweepAttachments(ctx context.Context) {
	a.exec(ctx, "orphan uploads", `DELETE FROM attachments WHERE NOT attached AND created_at < now() - interval '1 hour'`)
	st := attachBackend()
	if st == nil {
		return
	}
	rows, err := a.db.Query(ctx, `SELECT id FROM attach_trash ORDER BY created_at LIMIT 200`)
	if err != nil {
		logErr("attachment trash", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := st.Delete(ctx, id); err != nil {
			logErr("attachment delete", err)
			continue
		}
		a.exec(ctx, "attachment trash row", `DELETE FROM attach_trash WHERE id=$1`, id)
	}
}

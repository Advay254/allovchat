# Messages, privacy settings and attachments

Everything here works the same in all three chat doors: `/api/t/{id}` for the link owner, `/api/l/{slug}` for a guest, and `/api/m/{id}` for a guest who saved the chat to an account. Everything also reaches other devices through [sync](sync.md).

In an encrypted chat the server stores ciphertext. What it can see is minimal metadata: which message a reply or reaction points at, who sent what and when, the chat's disappear timer, and the size and kind of an attachment.

## Replies

`POST .../send` accepts `reply`, the id of a message in the same chat. The quoted text travels inside the encrypted message, so the server only stores the pointer. A reply to a message in another chat is refused.

## Reactions

`POST .../react` with `{"mid": 12, "body": "..."}`. Each person has one reaction per message, and sending the same one again changes nothing. An empty `body` removes it. In an encrypted chat the body is ciphertext. In a plain chat it is a short emoji (up to 8 characters). Reactions come back on the message as `reactions: [{side, body}]`.

## Delete for everyone

`POST .../unsend` with `{"mid": 12}`. Only the sender can do it. The text, the reactions and any attachment are erased on the server, and what stays is a marker (`deleted: true`) so every device removes the message. Repeating it changes nothing.

## Disappearing messages

`POST .../disappear` with `{"secs": N}`, where N is one of 0 (off), 300, 3600, 86400 or 604800. Either person can change it. It only affects messages sent afterwards, and the timer starts when a message is sent. Each message carries its own `expires_at`. Expired messages are never returned, and a cleanup that runs every ten minutes deletes them with their attachments.

## Settings that belong to a person

`GET /api/prefs` and `POST /api/prefs`, all exact states:

| Setting | When off |
|---|---|
| `typing` | The other person never sees you typing |
| `receipts` | The other person is not told when you read their messages. This also hides it on messages you read before |
| `online` | The other person never sees that you are online |
| `notif_private` | Stored for the apps (default on). Push events never contain message text or names in any case |

The existing per-chat privacy mode still works, and either one hides the signal. These settings follow the account to every device.

## Pin and mute

`POST /api/chats/{id}/prefs` with `pinned` and/or `muted`. Both are private to the person who sets them, and the other person never learns about them. A muted chat sends no push or app notification to that person.

## Attachments

Images, files and voice notes. The app encrypts the file with the chat key first, so the server stores an opaque blob and never learns its name, type or contents.

1. `POST .../attach` with `{"kind": "image" | "file" | "voice", "data": "<base64 of the encrypted bytes>"}` returns `{"id", "size"}`.
2. Send a message with `attach: "<id>"`. The message body carries the details (name, type, nonce), encrypted like any message.
3. The other person fetches it with `GET .../attach/{id}`. It returns the encrypted bytes with `Content-Type: application/octet-stream`.

Rules: only encrypted chats can carry attachments. A blob is served only to the two people in the chat, and only once it is attached to a message (the uploader can fetch it earlier). An upload can be attached once, and only by the person who uploaded it. The default limit is 8 MB (`ATTACH_MAX_MB`, at most 25), voice notes are capped at 4 MB, a chat can hold at most six times the limit in total, and uploads are rate limited. An upload that is never attached is deleted after an hour. A blob is also deleted with its message (delete for everyone or a timer) and with its chat.

The upload is base64 inside JSON because every write request on this server must be JSON. That costs about a third more data than raw bytes, which is fine for compressed images and voice notes.

### Where the files live

Set one of these on the server. With neither set, attachments are off, and `GET /api/attach/config` says so.

- `ATTACH_DIR`: a folder on a disk that survives restarts. Free Render web services have no such disk.
- An S3-compatible bucket: `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` and optionally `S3_REGION` (default `auto`). Cloudflare R2 works and has a free tier. The server signs its own requests, so no extra library is needed.

The signing code is checked against the example in the AWS documentation, and the full upload, download and delete flow is tested against a fake bucket that rejects any request whose signature doesn't match. It has not been tested against a real bucket.

## Notifications

Push and app events carry a kind and an opaque tag, never message text, names or chat labels. A test checks this.

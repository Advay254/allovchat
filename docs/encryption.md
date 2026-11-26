# Encryption and the key vault

This document explains how allovchat keeps chats private, how a person can use the same account on more than one device, and where the protection stops. The server side of the key vault is in place. The browser and app code that uses it arrives in phase A1, so until then the web interface still keeps keys in one browser only, as the README describes.

## What protects a chat

Each link has a key pair made in the owner's browser. When a guest opens the chat, their browser makes its own key pair. Both sides combine their private key with the other side's public key (ECDH, P-256) and derive a shared AES-256-GCM key. Messages and shared contact details are sealed with that key before they leave the device. The server stores and relays ciphertext.

There are two kinds of chat keys, and the vault treats them differently:

- **Link key.** One per link, held by the link's owner. It decrypts every chat on that link.
- **Guest chat key.** One per chat, held by a guest. It only matters to a guest who saved the chat to an account. A guest without an account keeps it in one browser and loses the chat if that browser's storage is cleared. That is allowed, and the guest is told so.

## The problem the vault solves

Chat keys used to live only in the browser that made them. Sign in somewhere else, reinstall the app or clear site data, and the chats were unreadable. The vault stores those keys on the server in a form the server cannot open.

## How it works

Every account has one **master key (MK)**: 256 random bits, made in the client. The server never receives it in plain form.

```
password ──PBKDF2-SHA256──► key ──seals──► wrapped_pw ─┐
                                                       ├─ MK ──seals──► each chat key (opaque blob)
recovery code ──HKDF──────► key ──seals──► wrapped_rc ─┘
```

- `wrapped_pw`: MK sealed with a key stretched from the account password. The salt is random per account and the iteration count is stored with the record, so it can be raised later without locking anyone out.
- `wrapped_rc`: MK sealed with a key derived from the **recovery code**, a random code of at least 128 bits shown once when the account is created. Because the code is random and long, it is derived with HKDF and does not need password-style stretching.
- Each chat key is sealed with MK and stored as an opaque blob tied to the link or chat it belongs to.
- The recovery code also derives a second value, `rc_auth`, that proves the person holds the code. The server keeps only a hash of it. The sealing key and `rc_auth` come from different HKDF labels, so having one tells you nothing about the other.
- The recovery code itself is stored sealed with MK (`rc_display`), so Settings can show it again after the person re-enters their password.

### Sealed format

Every sealed value is `k1.<iv>.<ciphertext>`, base64url, AES-256-GCM with a fresh 12 byte IV. The `k1` prefix is distinct from the `e1` prefix on chat messages. The server checks the shape and size and nothing else, because it cannot see inside.

The additional authenticated data (AAD) binds each blob to its place, so the server cannot hand a blob to a different account, link or chat:

| Blob | AAD string |
|---|---|
| MK under the password | `allovchat-vault-v1\|mk-pw\|<account id>` |
| MK under the recovery code | `allovchat-vault-v1\|mk-rc\|<account id>` |
| Recovery code, sealed with MK | `allovchat-vault-v1\|rc-display\|<account id>` |
| A chat key | `allovchat-vault-v1\|key\|<account id>\|<link or chat>\|<id>` |

The account id comes from `GET /api/vault`. `test/vault.mjs` is a complete working reference for all of this.

### Password change

The client unwraps MK with the old password, wraps it again with the new one and uploads the new copy together with the new password. Nothing else changes, so no chat is lost. The request carries the vault version it started from, so two devices changing the password at once cannot overwrite each other. Every other signed-in device is signed out.

### Recovery

For someone who forgot the password but kept the code:

1. The client derives the sealing key and `rc_auth` from the code and sends `rc_auth` to `recover/open`. The server returns `wrapped_rc` only if the proof matches.
2. The client opens MK, then sends `recover/finish` with a new password, a new password copy, a **new recovery code** and its proof. The server replaces everything in one transaction and signs every device out.
3. A used recovery code stops working immediately. The person then signs in with the new password.

A wrong code and an unknown username get the same answer, and attempts are limited like sign-in (5 failures per account and address, 20 per address).

### Guest to account

When a signed-in guest saves a chat, the client sends the chat key sealed with MK in the same request (`POST /api/l/{slug}/claim` with `{"key": "k1..."}`). The chat is attached and its key stored in one transaction, so a chat is never saved to an account while its key exists in one browser only. Removing a chat from the account deletes its sealed key. Deleting a link or a chat, or letting it expire, deletes its sealed keys too.

### Key states

For every link and saved chat a client can be in one of three states:

| State | Meaning | What the app shows |
|---|---|---|
| present | The key is in this browser | Normal chat |
| restorable | Not here, but a sealed copy is in the vault | A "Restore keys" action |
| missing | Neither | An explanation that the chat cannot be read on this device |

`GET /api/vault/status` lists the link and chat ids that have a sealed key, without sending the keys, so a client can work this out cheaply. Chats made before the account had a vault are *missing* until the original browser uploads their keys, which the client does on its next sign-in.

## The server's API

All of these need a signed-in account, except the two recovery calls. Only GET and POST are used, because the Android WebView is allowed only those across origins.

| Endpoint | Purpose |
|---|---|
| `GET /api/vault` | The account id, whether a vault exists, the password copy, the KDF settings and the suggested iteration count |
| `POST /api/vault/create` | Create the vault. Sending the same vault again succeeds, a different one is refused |
| `POST /api/vault/password` | Change the password and the password copy together. Needs the current password |
| `GET /api/vault/status` | Which links and chats have a sealed key |
| `GET /api/vault/keys` | The sealed keys, for restoring on a new device |
| `POST /api/vault/keys` | Store up to 20 sealed keys, all or nothing. Only for links the account owns or chats saved to it |
| `POST /api/vault/recover/open` | Check the recovery proof, return the recovery copy |
| `POST /api/vault/recover/finish` | Set a new password and a new recovery code |

Limits: 2 KB per sealed blob, 20 per request, 500 per account. The password iteration count must be between 200,000 and 5,000,000. New vaults use 600,000 unless the server sets `VAULT_KDF_ITER`. That default has not been measured on a mid-range Android WebView yet. It will be measured in phase A1 and adjusted, and because each vault stores its own count, changing it later is safe.

## Where the protection stops

Be honest with yourself about these.

- **The server sees your password at sign-in.** The password copy of MK is sealed with a key stretched from that same password. So the vault protects against a **leaked database**: an attacker with the data still has to guess the password, and the stretching makes that slow. It does **not** protect against a **malicious or compromised server operator**, who could capture the password as it arrives and then open the vault. A strong, unique password matters.
- **The recovery code is the stronger path.** It never reaches the server, and a 128 bit random value cannot be guessed. Someone who keeps the code safe and never needs it has a vault that depends on it only in an emergency. The code is stored sealed under MK, so a stolen database plus a cracked password also reveals it.
- **A possible later option** is a separate chat passphrase, entered only in the client and never sent to the server, that seals MK instead of the account password. It would protect against a malicious operator at the cost of one more thing to remember. It is not built.
- **No forward secrecy.** A link's key opens every chat on that link. Chats last at most 7 days.
- **The server could swap public keys.** "Verify encryption" compares a safety code on both screens to catch that.
- **Metadata is visible.** Who talks to which link, when, and how large the messages are.
- **Moderation.** Encrypted chats cannot be read or filtered by the server.

## Rolling out

The new tables are additive, so a server running the older code keeps working against the same database. The only change to an existing endpoint is that `claim` now also accepts an optional `key` and returns the chat id.

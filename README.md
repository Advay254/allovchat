# allovchat

Meet without revealing. Connect without pressure.

allovchat gives you a link that opens a private chat. Post it in a group, a bio or a listing, and anyone who opens it can write to you without an account. You decide who gets in. If you both want to move to WhatsApp or somewhere else, you each agree and the details show up once.

It's open source (MIT), written in Go, and stores everything in Postgres.

## What it does

- **End-to-end encryption.** Messages and shared contact details are encrypted in the browser before they're sent. The server only stores ciphertext. See below for how it works and where it stops.
- **Links with a lifespan.** One link per post or group. Chats on a link last 1 hour to 7 days. Seven days is a hard limit, and everyone is warned 12 hours before a chat is deleted.
- **Fresh names.** Every new chat gives each person a new temporary name.
- **You approve every chat.** Guests send a request. You accept or block. A blocked guest can't reach any of your links.
- **Reveal only if both agree.** Contact details appear once and disappear after 24 hours.
- **Easy exits.** A Leave button ends a chat for both people instantly. Either person can also shorten how long a chat lasts.
- **Privacy mode.** Hide typing indicators, read receipts and online status.
- **Optional accounts.** Guests don't need one. If a guest wants to keep a chat, they can save it to an account.
- **A dashboard for you.** Stats, links, chats, and a separate admin area at a path you choose.

## How the encryption works

Each link has a key pair made in the owner's browser. The public half is published with the link. When a guest starts a chat, their browser makes its own key pair, combines its private key with the link's public key (ECDH, P-256) and derives a shared AES-256-GCM key. Every message is sealed with that key before it leaves the device. The owner's browser derives the same key from the link's private key and the guest's public key.

What the server can see: who talks to which link, when, how big messages are, and the ciphertext. What it can't see: message text or contact details.

Where it stops, honestly:

- **Keys live in the browser.** Clear the site data and you lose the key to that chat. Owners can back up a link's key in the dashboard. Guests can copy a return link, which carries the chat key after the `#` (browsers never send that part to a server).
- **There's no forward secrecy yet.** Someone who gets a link's private key can read every chat on that link. Chats last at most 7 days.
- **The server could swap keys.** Use "Verify encryption" to compare the safety code on both screens.
- **Chats from before this feature** stay plain text until they expire, and are marked as such.
- **Moderation.** Encrypted chats can't be filtered or read by moderators. When someone reports a chat, their browser can attach the recent messages as evidence.
- **Metadata isn't hidden.** Timing, sizes and connection details are still visible to the server operator.

## Run it

You need a Postgres database. Tables are created on start.

```bash
cp .env.example .env   # fill it in
set -a; . ./.env; set +a
go run .
```

Or with Docker: `docker build -t allovchat . && docker run --env-file .env -p 8080:8080 allovchat`.

It needs a normal always-on server (a VM, Docker host, Render, Railway, Fly.io). Serverless platforms such as Vercel don't suit it.

## Settings you need

| Variable | Required | What it does |
|---|---|---|
| `DATABASE_URL` | yes | Postgres connection string |
| `ADMIN_USERNAME` | yes | Admin username |
| `ADMIN_PASSWORD` | yes | Admin password |
| `ADMIN_PATH` | yes | Your admin slug, 4 to 64 characters (`a-z A-Z 0-9 - _`). The dashboard lives at `/<ADMIN_PATH>/` |
| `BASE_URL` | yes | Your public URL, no trailing slash |
| `PORT` | no | Defaults to `8080` |
| `TRUSTED_PROXY_HOPS` | no | How many proxies sit in front of the app. Defaults to `1` |
| `DB_MAX_CONNS` | no | Database connections to open, 1 to 20. Defaults to `6`. Keep it low on small databases |

Everything else (site name, tagline, sign-ups, maintenance mode, limits, banned words, Cloudflare Turnstile keys) is changed in the admin dashboard.

## Health check

`GET /health` returns `200` when the app and database are fine. Point an uptime monitor at it every 5 minutes to keep a free host awake.

## Contributing

Bug reports, ideas and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT

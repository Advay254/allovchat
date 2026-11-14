# allovchat

Meet without revealing. Connect without pressure.

allovchat gives you a link that opens a private chat. Post it in a group, a bio or a listing, and anyone who opens it can write to you without an account. You decide who gets in. If you both want to move to WhatsApp or somewhere else, you each agree and the details show up once.

It's open source (MIT), written in Go, and stores everything in Postgres.

## What it does

- **Links with a lifespan.** One link per post or group. Chats on a link last 1 hour to 7 days. Seven days is a hard limit, and everyone is warned 12 hours before a chat is deleted.
- **Fresh names.** Every new chat gives each person a new temporary name.
- **You approve every chat.** Guests send a request. You accept or block. A blocked guest can't reach any of your links.
- **Reveal only if both agree.** Contact details appear once and disappear after 24 hours.
- **Easy exits.** A Leave button ends a chat for both people instantly. Either person can also shorten how long a chat lasts.
- **Privacy mode.** Hide typing indicators, read receipts and online status.
- **Optional accounts.** Guests don't need one. If a guest wants to keep a chat across devices, they can save it to an account.
- **A dashboard for you.** Stats, links, chats, and a separate admin area at a path you choose.

## What it doesn't do

Chats are encrypted in transit (HTTPS) but they are **not end-to-end encrypted**, and the server can read them until they're deleted. Being anonymous to the person you're talking to isn't the same as being untraceable. If you run this for other people, tell them.

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

Everything else (site name, tagline, sign-ups, maintenance mode, limits, banned words, Cloudflare Turnstile keys) is changed in the admin dashboard.

## Health check

`GET /health` returns `200` when the app and database are fine. Point an uptime monitor at it every 5 minutes to keep a free host awake.

## Contributing

Bug reports, ideas and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT

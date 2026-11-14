# allovchat

Anonymous chat on a link. Share a link anywhere; people who open it can talk to you privately without either side exposing real social accounts. When you both trust each other, you reveal contacts by mutual agreement and move to WhatsApp or anywhere else.

Open source (MIT). Go + Postgres. One small binary, no front-end build step.

## Features

- Shareable links, one per topic or post
- Visitors need no account; owners approve every chat request (accept or block)
- WhatsApp-style real-time chat with typing indicators and read ticks (Server-Sent Events)
- Random aliases per chat
- Mutual contact reveal: both sides must agree, shown once, removed after 24 hours
- Report, block and delete chats
- Landing page and a master admin dashboard at a secret path (owners, links, reports, settings)
- `/health` endpoint for uptime monitors
- Auto-delete of inactive chats

## Run it

You need a Postgres database (Aiven, Supabase, Neon, your own...). Tables are created automatically on start. Run it on a host that keeps a long-lived process (a VM, Docker host, Render, Railway, Fly.io...). Serverless platforms such as Vercel are a poor fit.

```bash
cp .env.example .env   # fill it in
go mod tidy            # only if go.sum is missing
set -a; . ./.env; set +a
go run .
```

Or with Docker: `docker build -t allovchat . && docker run --env-file .env -p 8080:8080 allovchat`.

On any host that deploys from a GitHub URL or a Dockerfile: point it at this repo, set the environment variables below, deploy.

## Environment variables

| Variable | Required | Purpose |
|---|---|---|
| `DATABASE_URL` | yes | Postgres connection string |
| `ADMIN_USERNAME` | yes | Master admin username |
| `ADMIN_PASSWORD` | yes | Master admin password |
| `ADMIN_PATH` | yes | Your admin slug, 4-64 chars (`a-z A-Z 0-9 - _`), not `health` or `static`. Dashboard is at `/<ADMIN_PATH>/` |
| `BASE_URL` | yes | Public URL, no trailing slash. `https://` enables Secure cookies |
| `PORT` | no | Default `8080` |
| `SESSION_SECRET` | no | Cookie signing key; derived from the admin password if empty |

Short slugs are easier to guess, so the admin login (rate limited) and a strong `ADMIN_PASSWORD` are what really protect the dashboard.

Everything else (site name, tagline, announcement banner, sign-ups on/off, maintenance mode, message length, retention days, link limits, rate limits) is changed live in the admin dashboard.

## Health check

`GET /health` returns `200 {"status":"ok","db":"up"}`, or `503` if the database is unreachable. Point UptimeRobot (or similar) at `BASE_URL/health` every 5 minutes to keep free-tier hosts awake.

## Privacy and security notes

- Anonymous to the other person, not to the server operator. Chats are **not** end-to-end encrypted, and IP addresses reach the server. Say so to your users.
- The admin can only read chats that were reported.
- Rate limiting is in memory and keyed by client IP (first `X-Forwarded-For` value, so run behind a proxy you trust). Use a single instance, or move limits and the event hub to a shared store before scaling out.
- Cloudflare Turnstile / CAPTCHA is not included yet.

## Roadmap ideas

Turnstile, image messages, push notifications, email recovery for owners, multi-instance support (Postgres LISTEN/NOTIFY), translations, themes.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Issues and pull requests are welcome.

## License

MIT

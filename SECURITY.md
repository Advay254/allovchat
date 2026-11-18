# Security policy

## Reporting a vulnerability

Please report security problems privately through [GitHub security advisories](https://github.com/Advay254/allovchat/security/advisories/new). Please do not open a public issue for a vulnerability.

Include what you found, how to reproduce it, and what an attacker could do with it. I aim to reply within a week and to fix confirmed problems as quickly as I can. If you want credit, say so in your report.

The same contact is published at `/.well-known/security.txt` (RFC 9116).

## Supported versions

Only the latest release and `main` get fixes.

## Design limits

These are documented design limits, not vulnerabilities:

- End-to-end encryption has no forward secrecy yet. A stolen key can read that chat.
- Metadata is visible to the server: chat existence, timing, message sizes, temporary names and link names.
- The server delivers the JavaScript that does the encryption, so a compromised server could change it. The safety code in each chat helps people check that keys match.
- Keys live in the browser's local storage.
- Rate limits are held in memory, so they reset on restart and are not shared between instances.
- Because messages are encrypted in the browser, the word filter runs there too. A modified client can skip it.

## Scope

In scope: this repository and the code it runs. Out of scope: third-party services (hosting, database, Cloudflare), social engineering, and denial of service through raw traffic volume.

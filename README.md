# Mail Console — self-hosted, safety-first bulk email

Internal multi-brand email marketing on Amazon SES. See [PLAN.md](PLAN.md) for the full
build plan and `design.fig` (OpenPencil) for the screens.

| Folder | What |
|---|---|
| `api/` | Go backend — one binary: `-mode=api`, `-mode=worker`, `-mode=all`, `-mode=migrate` |
| `web/` | React + Vite + react-router (library mode), deployed to Cloudflare |
| `infra/aws/` | OpenTofu: SES event queue (EventBridge → SQS), IAM policy for the app |

## Run locally

Needs Go 1.26+, Node 24+, and Postgres + Redis. By default it uses the ones already
running on this machine (DBngin): **Postgres on `localhost:54321`** (databases `klavio`
and `klavio_test`, user `postgres`) and **Redis on `localhost:6379`**.

```bash
make migrate       # app + job-queue tables
make run           # API + worker on :8080
```

In another terminal:

```bash
cp web/.env.example web/.env.local
make web-install
make web-dev       # http://localhost:5173
```

The top bar shows **API dev** in green when the frontend can reach the backend.

**First sign-in.** Accounts are invite-only and use passkeys. Create the first owner:

```bash
cd api && DATABASE_URL=postgres://postgres@localhost:54321/klavio?sslmode=disable REDIS_URL=redis://localhost:6379/0 \
  go run ./cmd/app -mode=bootstrap -email you@company.com -name "Your Name"
```

Open the printed link and create a passkey (Touch ID, Windows Hello or your phone).
Running bootstrap again before anyone has joined cancels the earlier link. After that,
owners invite people from **Team & access**.

**Demo data.** Once you've signed in, `make seed` adds three brands (Northwind
Outdoors, Lumen Skincare, Harbor Books), about 20,000 contacts and half a year of sent
campaigns with their bounces, opens, clicks and unsubscribes, so every screen has
something to show. Restart `make run` afterwards so health scores fill in. Demo
addresses and domains end in `.test`, which can never receive email, and it refuses to
run unless `APP_ENV=development` and `SES_MODE=fake`. `make seed RESET=1` replaces it.

**Postgres/Redis in Docker instead** (kept for other machines and CI-like runs):
`make docker-deps` starts them on ports `55432` / `56379`, then pass the URLs it prints.
`docker compose up --build` runs the backend itself in Docker against your local DBs.

## AI agents (MCP)

AI agents such as Claude Code, Codex and OpenCode can use the console through MCP at
**`<API address>/mcp`** (locally `http://localhost:8080/mcp`). Each person makes a token for
their agent in **Settings → AI agents**. The page shows the token once, with ready-to-paste
setup for each agent. Tokens last at most 90 days and can be revoked there.

- The agent acts as that person, with at most their role. It can read (brands, health,
  campaigns and reports, templates, segments, contacts, approvals) and prepare (draft
  campaigns, templates and segments, add a contact with proof of consent).
- It never passes a passkey check, so it can't send, approve, delete, unsubscribe, block,
  export or change settings. To send, it calls `request_send`, which files an approval. The
  person whose agent it is may approve that themselves with their passkey; large or risky
  sends still need a second admin, as for people.
- Every action is in the audit log, marked "via AI agent <token>". There is a limit of 120
  calls a minute per token.
- In production, `/mcp` must be reachable on the public API address, not behind Cloudflare
  Access: agents can't do the Access login. The token is the lock.

Connecting Claude (claude.ai and Desktop) needs an OAuth sign-in, which is the next step.

## Tests

```bash
make check             # Go vet + unit tests, web typecheck + tests
make test-integration  # also runs the tests that need Postgres + Redis
```

CI (`.github/workflows/ci.yml`) runs all of it against a real Postgres, smoke-tests the
binary, builds the Docker image and validates the OpenTofu config.

## Backend endpoints so far

| Path | Purpose |
|---|---|
| `GET /healthz`, `GET /readyz` | Liveness; Postgres + Redis reachable |
| `GET /admin/v1/meta` | Version + env |
| `GET /admin/v1/me` | Signed-in user, brand roles, step-up window |
| `POST /admin/v1/auth/login/{begin,finish}` | Passkey sign-in |
| `POST /admin/v1/auth/stepup/{begin,finish}` | Re-confirm with passkey (5 min) for sensitive actions |
| `POST /admin/v1/auth/logout` | |
| `GET /admin/v1/invites/{token}`, `POST …/{begin,finish}` | Accept an invite by creating a passkey |
| `GET/POST /admin/v1/brands`, `GET/PATCH /admin/v1/brands/{slug}` | Brands; creating one sets up SES and lists DNS records |
| `POST /admin/v1/brands/{slug}/dns-check` | Re-check DNS + SES status (also runs daily) |
| `GET /admin/v1/team`, `POST /admin/v1/team/invites`, `PUT /admin/v1/team/members/{id}/roles` | Team & access |
| `GET /admin/v1/audit` | Recent audit log (owners, approvers) |
| `GET /admin/v1/brands/{slug}/contacts` | Contacts with tier, consent proof; filters `tier`, `status`, `tag`, `q`, paging `after` (editor+) |
| `GET …/contacts/stats` | Tier counts, quarantined, suppressed (every role) |
| `POST …/contacts`, `GET …/contacts/{id}` | Add one contact with a consent source (brand admin); detail + consent history |
| `POST …/contacts/{id}/unsubscribe`, `…/suppress` | Honour a request (editor+); block an address for every brand (brand admin) |
| `GET/POST …/topics` | What people subscribe to; unsubscribing is per topic |
| `POST …/imports` (multipart), `GET/PUT …/imports/{id}` | Upload CSV; save column mapping + consent answers + attestation |
| `POST …/imports/{id}/check`, `…/commit`, `…/cancel` | Background safety check; import (passkey confirmation); cancel |

| `GET /admin/v1/approvals?view=decide\|mine\|recent` | Two-person rule (S15) |
| `POST /admin/v1/approvals/{id}/approve` (passkey) · `/reject` (note) · `/cancel` (requester) | Decide; the approved action runs in the same transaction |
| `GET/PUT /admin/v1/safety-limits` | S17 limits: tighten now, loosen via approval, never past the hard limit |
| `POST /admin/v1/brands/{slug}/exports`, `GET /admin/v1/exports`, `GET /admin/v1/exports/{id}/download` | Approved, watermarked exports; requester-only download for 24 h |
| `GET /admin/v1/alerts`, `POST …/alerts/{id}/acknowledge`, `POST /admin/v1/team/members/{id}/unfreeze` | Anomaly alerts and frozen accounts |
| `GET /admin/v1/audit?brand=&actor=&action=&since=&until=&before=` | Audit log with filters and paging (S16) |

Every write needs `Content-Type: application/json` from an allowed origin (CSRF guard).

**Governance (M5).** Admin changes go through `internal/approvals`: a feature `Register`s a kind with an
`Execute` function and calls `Request` inside its own transaction; another eligible admin approves (with a
passkey) and the action runs atomically with the decision. Nobody approves their own request (checked in code
and by a database constraint). In a company with only one admin, admin changes apply directly and are audited
as `no_second_admin`. Anomalies (new sign-in country → frozen unless owner; new passkey → alert; 3rd export or
5,000+ rows in a day → frozen; huge import → alert) show in the bell for owners and approvers.

**Public pages** (no login; served on each brand's link domain, e.g. `go.brand.com`; signed links from `internal/unsubscribe`):

| Path | Purpose |
|---|---|
| `GET /u/{token}` | P4: "Unsubscribe from <topic>?" — read-only, so mail scanners can't unsubscribe anyone |
| `POST /u/{token}` | The page's confirm button, or the RFC 8058 one-click POST (`List-Unsubscribe=One-Click`, form or multipart) |
| `GET/POST /p/{token}` | P3 preference center: per-topic choices, unsubscribe from all; suppressed addresses can only remove |

Tokens are HMAC-signed (brand, contact, optional campaign and topic) and never expire. POSTs are rate-limited
per IP and per token; logs show `/u/[token]`.

## Configuration (`api/.env.example`)

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `REDIS_URL` | — | required |
| `APP_ENV` | `development` | `development` or `production` |
| `HTTP_ADDR` | `:8080` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` (dev), `json` (prod) | |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | comma-separated web app origins (CORS + passkeys) |
| `APP_URL` | `http://localhost:5173` | base URL for invite links |
| `WEBAUTHN_RP_ID` | `localhost` | required in production: your domain, e.g. `yourco.com` |
| `CF_ACCESS_TEAM_DOMAIN`, `CF_ACCESS_AUD` | — | required in production |
| `SES_MODE` | `fake` (dev), `aws` (prod) | `aws` also needs `SES_REGION`, `SES_EVENT_BUS_ARN` |
| `DNS_RESOLVER` | `1.1.1.1:53` | DNS server for brand checks |
| `UNSUBSCRIBE_SECRET` | dev-only default | required in production, 32+ random characters; signs unsubscribe and double opt-in confirmation links |
| `UNSUBSCRIBE_SECRET_OLD` | — | previous secret during rotation (old links keep working) |
| `BLOCKLIST_RESOLVER` | system resolver | `host:port` of your own recursive DNS resolver for blocklist lookups (public resolvers are refused); `off` disables them |
| `BLOCKLIST_CHECK_INTERVAL` | `6h` | how often blocklists are re-checked (at least `1h`) |
| `SPAMHAUS_DQS_KEY` | — | optional Spamhaus Data Query Service key |
| `POSTMASTER_CREDENTIALS_FILE` | — | Google service-account JSON for Postmaster Tools (Gmail spam rate). Mount as a secret; never commit |
| `SES_EVENTS_QUEUE_URL` | — | required in production: the SQS queue of SES events (`ses_events_queue_url` output); bounces and complaints arrive there |
| `TRACKING_SCANNER_CIDRS` | — | comma-separated ranges of mail security scanners; their clicks count as bots |
| `TRACKING_HONEYPOT` | `false` | `true` adds a hidden link only scanners follow (can look spammy to filters) |
| `DEBUG` | `true` (dev), `false` (prod) | `true` serves the admin API reference at `/admin/v1/docs` (spec: `/admin/v1/openapi.json`) to signed-in members, linked from Settings. Allowed in production, with a warning at startup; off, both routes don't exist (404) |
| `PUBLIC_BASE_URL_OVERRIDE` | — | development only, e.g. `http://localhost:8080` instead of `https://<link domain>` |
| `TRUST_CLOUDFLARE_HEADERS` | `false` (dev), `true` (prod) | believe `CF-Connecting-IP` / `CF-IPCountry` from `TRUSTED_PROXY_CIDRS` |
| `TRUSTED_PROXY_CIDRS` | `127.0.0.1/32,::1/128` | comma-separated IPs/CIDRs of the `cloudflared` sidecar (docker-compose: `10.210.250.10/32`) |

### Client IP and country

The client IP feeds the per-IP rate limits on the public unsubscribe pages, consent records, sessions
and the audit log; the country feeds the "sign-in from a new country → freeze" anomaly rule. Both come
from Cloudflare's `CF-Connecting-IP` and `CF-IPCountry` headers — but any client can send those, so the
API only believes them when `TRUST_CLOUDFLARE_HEADERS=true` **and** the TCP connection comes from an
address in `TRUSTED_PROXY_CIDRS`. Otherwise the IP is the socket address and the country is unknown.

- Behind Cloudflare Tunnel the API's peer is `cloudflared`, not a Cloudflare edge IP. List exactly the
  sidecar's address: `127.0.0.1`/`::1` when it shares the host network, or a fixed container IP
  (`docker-compose.yml` gives it `10.210.250.10` on the `edge` network). Don't list a whole Docker subnet:
  its gateway address is where traffic to published ports comes from.
- If you ever put the origin directly behind Cloudflare's proxy instead of a tunnel, list
  [Cloudflare's IP ranges](https://www.cloudflare.com/ips/) — and firewall everything else.
- A sign-in without a trusted country is never "same country": with trust on (or after earlier sign-ins
  had a country) it raises an `unknown_country` warning. If you see those for every sign-in, or the log
  line `ignoring Cloudflare headers from a peer outside TRUSTED_PROXY_CIDRS`, fix `TRUSTED_PROXY_CIDRS`.

## Deploying (M0 checklist — manual steps)

1. **AWS SES production access** — request it in the SES console now; it can take days.
2. **Infra**: `cd infra/aws && tofu init && tofu apply` (add an S3 backend first).
   Attach the `app_policy_arn` output to the role the container runs as.
3. **Database**: managed Postgres 17 + Redis (e.g. RDS `db.t4g.micro`). Run the image once with `-mode=migrate`.
4. **Backend**: run the image with `-mode=all` (or one `api` + one `worker`) and a
   `cloudflared` sidecar using a Cloudflare Tunnel token (see `docker-compose.yml`, profile `tunnel`).
   Route `admin-api.<your-domain>` → `http://app:8080` in the tunnel. Set `TRUSTED_PROXY_CIDRS` to the
   sidecar's address (see [Client IP and country](#client-ip-and-country)); don't expose the API port otherwise.
5. **Frontend**: `cd web && VITE_API_URL=https://admin-api.<your-domain> npm run build && npx wrangler deploy`.
6. **Cloudflare Access**: protect `app.<your-domain>` and `admin-api.<your-domain>` with your Google Workspace.

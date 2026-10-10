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

**Postgres/Redis in Docker instead** (kept for other machines and CI-like runs):
`make docker-deps` starts them on ports `55432` / `56379`, then pass the URLs it prints.
`docker compose up --build` runs the backend itself in Docker against your local DBs.

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

Every write needs `Content-Type: application/json` from an allowed origin (CSRF guard).

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

## Deploying (M0 checklist — manual steps)

1. **AWS SES production access** — request it in the SES console now; it can take days.
2. **Infra**: `cd infra/aws && tofu init && tofu apply` (add an S3 backend first).
   Attach the `app_policy_arn` output to the role the container runs as.
3. **Database**: managed Postgres 17 + Redis (e.g. RDS `db.t4g.micro`). Run the image once with `-mode=migrate`.
4. **Backend**: run the image with `-mode=all` (or one `api` + one `worker`) and a
   `cloudflared` sidecar using a Cloudflare Tunnel token (see `docker-compose.yml`, profile `tunnel`).
   Route `admin-api.<your-domain>` → `http://app:8080` in the tunnel.
5. **Frontend**: `cd web && VITE_API_URL=https://admin-api.<your-domain> npm run build && npx wrangler deploy`.
6. **Cloudflare Access**: protect `app.<your-domain>` and `admin-api.<your-domain>` with your Google Workspace.

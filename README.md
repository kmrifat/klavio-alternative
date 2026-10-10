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
| `GET /healthz` | Process is alive |
| `GET /readyz` | Postgres and Redis reachable (503 if not) |
| `GET /admin/v1/meta` | Version + env (used by the web app) |

## Configuration (`api/.env.example`)

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `REDIS_URL` | — | required |
| `APP_ENV` | `development` | `development` or `production` |
| `HTTP_ADDR` | `:8080` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` (dev), `json` (prod) | |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | comma-separated, for the admin API |

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

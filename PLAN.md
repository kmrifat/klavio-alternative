# Build plan — self-hosted, safety-first bulk email

Internal tool for Acme Group to send marketing email for several brands
through Amazon SES, without getting the brands marked as spam.
Built from the screens in `design.fig` (v2 · Safety-first).

---

## 1. Goals and non-goals

**Goals**
- Send campaigns for several brands from one app, each brand on its own sending domain.
- Keep every brand out of the spam folder: consent only, engagement-based audiences,
  test slice first, automatic pause when bounces or complaints go up.
- Measure what really happened: delivery, bounces, complaints, human clicks (bots filtered).
- Cheap to run: one small container plus Postgres. SES costs ≈ $0.10 per 1,000 emails.

**Not in v1** (already left out in the design's "Archive (out of scope)" page)
- Automations / flows (welcome series, abandoned cart emails)
- Signup forms builder
- A/B tests

---

## 2. Architecture

```
                         ┌──────────────────────── Cloudflare ────────────────────────┐
  Staff browser ──────►  │  Access (Google login, managed devices)                    │
                         │    app.yourco.com    → React app (static, Cloudflare)      │
                         │    admin-api.yourco.com ─┐                                 │
                         │                          │                                 │
  Recipients ─────────►  │  go.<brand>.com  (links, unsubscribe, P1–P4 pages) ─┐      │
  Brand websites ─────►  │  api.yourco.com  (Contacts API, API keys) ──────────┤      │
                         └─────────────────────────────────────────────────────┼──────┘
                                                                Cloudflare Tunnel
                                                                               │
                ┌──────────────────────── One Go binary ───────────────────────▼──┐
                │  mode=api     HTTP: admin API, public API, redirects, pages     │
                │  mode=worker  jobs: render + send, read SES events, brakes,     │
                │               health checks, imports, re-permission emails      │
                └───────────────┬───────────────────────────┬─────────────────────┘
                                │                           │
                          PostgreSQL                 Amazon SES (send)
                    (data + job queue "river")              │
                                ▲                    events per brand
                                │                    (configuration set)
                                │                           ▼
                                └──── worker polls ◄── EventBridge → SQS
```

Why this shape:
- **One Go binary, two modes.** Same image, run as 1× `api` and 1× `worker`. Small and simple.
- **Postgres is also the job queue** (`river`). No Redis, no extra service to pay for.
- **SES events go to SQS.** The worker pulls them. Nothing on the internet has to call us.
- **Cloudflare Tunnel** connects Cloudflare to the container, so the server has no open
  ports and no AWS load balancer (saves ≈ $16+/month).
- **Three kinds of hostnames**, because they need different protection:
  - `app` + `admin-api` → behind Cloudflare Access (staff only)
  - `api` → public, API keys (brand websites call it)
  - `go.<brand>` → public (people click links from email)

---

## 3. Tech stack

| Part | Choice | Why |
|---|---|---|
| Language | **Go 1.23+** | I/O-bound work, great AWS SDK, tiny static binary |
| HTTP | `net/http` (Go 1.22 routing) or `chi` | Standard, few dependencies |
| DB | **PostgreSQL 17** + `pgx` (hand-written queries in `internal/store`) | Relational data, SQL for segments; `sqlc` can be added later |
| Migrations | `goose` or `atlas` | Plain SQL files |
| Jobs / queue | **`river`** (Postgres-based) | Retries, scheduling, no extra infra |
| AWS | `aws-sdk-go-v2` (`sesv2`, `sqs`) | Official |
| Passkeys | `go-webauthn/webauthn` | Design requires passkey sign-in |
| Email HTML | MJML compiled **in the browser** (`mjml-browser`) on save; Go `html/template` for merge tags | No Node.js in the container |
| API contract | **OpenAPI 3** → `oapi-codegen` (Go) + `openapi-typescript` (React) | One source of truth for types |
| Frontend | **React + Vite + react-router (library mode, `createBrowserRouter`)** | As requested |
| Data fetching | TanStack Query | Caching, loading states |
| UI | Tailwind + shadcn/ui (or Radix) | Matches the clean look in the design |
| Charts | Recharts | Bars/lines in S7, S11 |
| Infra as code | OpenTofu/Terraform (AWS + Cloudflare providers) | Many DNS records and SES settings per brand |
| Container | `gcr.io/distroless/static` | ≈ 20 MB image |

---

## 4. Repository layout

```
klavio-alternative/
├── PLAN.md
├── design.fig
├── api/                      # Go
│   ├── cmd/app/main.go       # --mode=api | worker | migrate
│   ├── internal/
│   │   ├── auth/             # passkeys, sessions, Cloudflare Access JWT check, roles
│   │   ├── brands/           # brands, domains, DNS checks
│   │   ├── contacts/         # contacts, consent, topics, tiers, suppression
│   │   ├── imports/          # CSV upload, validation, quarantine
│   │   ├── segments/         # rule JSON → SQL
│   │   ├── templates/        # layouts, templates, checks (links, alt text…)
│   │   ├── campaigns/        # campaign state machine, audience snapshot
│   │   ├── sending/          # render, rate limit, SES client, test slice
│   │   ├── events/           # SQS reader, SES event mapping
│   │   ├── tracking/         # link tokens, redirect, bot filter
│   │   ├── safety/           # brakes, health score, frequency cap, limits
│   │   ├── approvals/        # two-person rule
│   │   ├── audit/            # append-only log
│   │   ├── publicapi/        # Contacts API, API keys, double opt-in
│   │   └── pages/            # P1–P4 server-rendered pages
│   ├── migrations/
│   ├── openapi.yaml
│   └── Dockerfile
├── web/                      # React + Vite
│   ├── src/routes/           # one folder per screen S1–S17
│   ├── src/api/              # generated types + TanStack Query hooks
│   └── src/components/
├── infra/                    # OpenTofu: SES, SQS, EventBridge, IAM, Cloudflare DNS
└── docker-compose.yml        # local Postgres (+ LocalStack for SQS if wanted)
```

---

## 5. Data model (core tables)

Every brand-owned table has `brand_id`. Every query is scoped by it.

**Org and people**
- `brands` — name, color, sending domain (`news.brightside.co`), link domain (`go.brightside.co`),
  postal address, SES configuration set, SES tenant, warm-up state, brake state
  (`healthy | watch | slowed | paused`)
- `users`, `passkeys`, `sessions`
- `memberships` — user × brand × role (`viewer | editor | sender | brand_admin`),
  plus company roles (`owner | approver`), optional `expires_at` (agency access)

**Contacts and consent**
- `contacts` — brand_id, email (lower-cased), name, timezone, country,
  `status` (`pending_confirmation | subscribed | quarantined | unsubscribed`),
  `last_click_at`, `tracking_allowed` (EU without tracking consent = false)
- `consent_records` — append-only: contact, wording key + exact text version, method,
  page URL, IP, time, source (`api | import | manual`), import_id
- `consent_wordings` — brand, key (`brightside-signup-v3`), text, status (`in_use | retired`)
- `topics`, `contact_topics` — "Product updates" etc. Unsubscribe is per topic.
- `suppressions` — **company-wide**, keyed by `sha256(email)`, reason
  (`hard_bounce | complaint | manual | erasure`). Survives GDPR deletion.
- `tags`, `contact_tags`

**Engagement tier** is computed, not stored by hand:
`active` (click ≤ 30d) · `warm` (31–90d) · `at_risk` (91–180d) · `sunset` (181d+ or never).
Keep it in a materialized column updated by a nightly job + on each click.

**Content**
- `layouts` — per brand, locked header/footer (changes need a 2nd admin)
- `templates` — mjml source, compiled html, plain text, check results
- `link_allowlist` — per brand allowed domains

**Campaigns and sending**
- `campaigns` — brand, topic, template, subject, preheader, segment, `include_at_risk`,
  state (see §6), test slice %, hold minutes, scheduled_at, approver
- `campaign_audience` — **snapshot** of who gets it, one row per contact, with
  `excluded_reason` (suppressed, sunset, quarantined, freq_cap, …) or `null`
- `sends` — one row per email: campaign, contact, `stage` (`test | rest`),
  `send_after` (quiet hours), `ses_message_id`, status, timestamps.
  **`UNIQUE (campaign_id, contact_id)`** — never send twice.
- `email_events` — delivered, bounce (hard/soft), complaint, reject, delay, open, click.
  **Partition by month.**
- `links`, `clicks` — link id, send id, ip, user agent, `is_bot`, bot reason
- `campaign_stats` — rolled-up numbers for fast reports

**Safety and governance**
- `safety_limits` — key, current value, hard limit, changed_by (S17)
- `safety_events` — what the brakes did (S2 "Recent safety events")
- `approvals` — type (`large_send | risky_audience | export | admin_change | resume_brand | …`),
  requester, approver, payload, status. DB check: `approver_id <> requester_id`.
- `audit_log` — append-only (no UPDATE/DELETE grants for the app user), who, what,
  brand, before → after, IP, device
- `imports` — file, source, attestation (who signed + text), counts per check result
- `api_keys` — brand, hashed key, scopes, IP allowlist, expires_at, last_used_at
- `api_calls` — 30-day log for S14 "Recent calls" (emails masked)

---

## 6. Key flows

### 6.1 Campaign state machine

```
draft ─► needs_approval ─► approved ─► scheduled ─(cancel window 10 min)─►
  test_slice ─► holding (45 min) ─► sending ─► done
                    │                   │
                    └──── paused ◄──────┘   (brake or admin)  ─► resumed / cancelled
```

- Any edit after approval → back to `draft` (approval resets).
- Approval is needed when: audience > 10,000 or > 2× usual size, At-risk included,
  first send to an imported list, brand in warm-up (from S9 / S17).

### 6.2 Sending (worker)

1. **At schedule time** build the audience snapshot. Remove: suppressed (any brand),
   unsubscribed from the topic, sunset, quarantined, unconfirmed double opt-in,
   at-risk (unless toggled), frequency cap. Store the reason for each removal (S5 list).
2. **Pick the test slice**: random 5% (`ORDER BY random()`), stage = `test`.
3. **Quiet hours**: set `send_after` so nothing lands 21:00–08:00 recipient time.
4. **Send jobs** in batches of 50. Before each batch, re-check: campaign still running?
   brand not paused? frequency cap still ok? Then render → `SendEmail` (SES v2) with:
   - From/Reply-To from the brand (never from the campaign)
   - `List-Unsubscribe: <https://go.brand.com/u/{token}>`
   - `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
   - Configuration set of the brand, email tags `campaign_id`, `send_id`
   - All links rewritten to `https://go.brand.com/c/{token}`
5. **Rate limit**: one in-process token bucket at the SES max rate (e.g. 14/s).
   On `Throttling` errors → back off and retry. Run **one** worker replica in v1.
6. **Hold** after the test slice for 45 min, then the brake check decides: continue or pause.

### 6.3 SES events → safety

- Per brand: configuration set → EventBridge → **SQS** (+ dead-letter queue).
- Worker long-polls SQS, maps `ses_message_id` → `sends` row, writes `email_events`.
- **Hard bounce or complaint → add to company-wide suppression immediately.**
- Soft bounces: retry is SES's job; after 3 soft bounces in 30 days → suppress.

### 6.4 Brakes (from S7, S2, S17)

Evaluated every 30 s while sending and after every 500 deliveries:

| Level | Rule (defaults) | Action |
|---|---|---|
| Campaign | bounces ≥ 2% · complaints ≥ 0.08% (min 2) · unsubscribes ≥ 1% | Pause campaign |
| Brand → Watch | 7d bounces ≥ 1% or complaints ≥ 0.05% | Alert, block inactive audiences |
| Brand → Slowed | ≥ 2% / ≥ 0.07% | Half send rate, every send needs approval |
| Brand → Paused | ≥ 3% / ≥ 0.08% or Gmail spam ≥ 0.2% | Stop all brand marketing → S8 recovery |
| Account | complaints (all brands) ≥ 0.08% | Stop everything |
| SES | tenant paused / deferrals > 5% | Pause |

Rules: tighten automatically, **loosen only with a second admin**. Never past the hard limit.

### 6.5 Tracking

- **Clicks (main signal).** Token = `base64(send_id, link_id) + HMAC`. Handler verifies,
  redirects with 302 at once, writes the click in the background.
- **Bot filter** (mark `is_bot`, never delete): click < 10 s after delivery, all links
  clicked within seconds, hidden honeypot link clicked, known scanner IP ranges / user agents,
  HEAD requests.
- **Opens**: 1×1 pixel, shown only as "Human opens (est.)" — exclude Apple Mail Privacy
  Protection (Apple proxy IPs). Never use opens for tiers or brakes.
- Contacts with `tracking_allowed = false` (EU): links still work, nothing is recorded.

### 6.6 Contacts API + double opt-in (S14, P1, P2)

`POST /api/v1/contacts` → check key, brand, IP allowlist, rate limits, consent wording →
create contact `pending_confirmation` → send confirmation email (P2) → person clicks →
P1 page → `subscribed`. Suppressed emails get the same `202 pending_confirmation` as
anyone else and nothing changes (deliberately: the answer mustn't reveal that an address
bounced or complained at another brand). Adding topics to an existing subscriber needs a
confirmation too; omitted topics leave an existing contact's topics alone.
Idempotency-Key header supported.

### 6.7 Import (S4)

Upload CSV → map columns → validate (syntax, MX lookup, disposable domains, role
addresses, suppression) → source + attestation required → gate (invalid ≤ 2%,
role+disposable ≤ 5%) → import. No activity in 12+ months → **quarantine**
(re-permission email only, 200/day). First campaign to new imports: capped at 10% of daily
volume + approval.

---

## 7. Milestones

Estimates assume **one full-time developer** who knows Go and React. Adjust for your team.

**Status (2026-10-11):** M0–M7 are built and merged. What's left is going live: the
open items in M0 and the SES simulator run in M3. Since M7 the app also has Settings →
Sending providers, General and API keys; GDPR erasure (Settings → Data & privacy);
13-month retention of raw SES events with campaign totals kept; email masking in logs;
and demo data (`make seed`). See §8.

### M0 — Foundations (≈ 1 week)
- [ ] AWS: request **SES production access** (do this on day 1, it can take days) — *not yet*
- [ ] Pick SES region (e.g. `us-east-1`), set sending quota target — *not yet; `us-east-1` is only the default*
- [ ] OpenTofu: SQS + DLQ, EventBridge rule, IAM role for the app — *written in `infra/aws` (with an IAM policy; the role comes with hosting), not applied yet*
- [x] Repo skeleton: `api/`, `web/`, `infra/`, docker-compose Postgres, CI (lint, test, build)
- [x] Go: config, logging (`slog`), health endpoint, migrations, `river` set up
- [x] React: Vite + router + layout shell (sidebar, brand switcher, top bar)
- [ ] Deploy: container + Postgres + Cloudflare Tunnel + Cloudflare Pages for `web` — *not yet: `api/Dockerfile` and `web/wrangler.jsonc` exist; waiting on the hosting decision (§9)*

**Done when:** an empty app shell is live behind Cloudflare Access and the API answers `/healthz`.

### M1 — Brands, auth, roles (≈ 2 weeks) · screens S9, S17 (Brands & domains)
- [x] Passkey sign-up/sign-in, sessions, step-up re-confirm for sensitive actions
- [x] Verify Cloudflare Access JWT on admin-api
- [x] Brands CRUD; per brand: create SES identity, Easy DKIM, custom MAIL FROM,
      configuration set, event destination (via API or OpenTofu)
- [x] DNS checker job: DKIM, SPF, DMARC, MAIL FROM, link domain → "Sending blocked until DNS is right"
- [x] Memberships + role checks on every endpoint
- [x] Audit log (append-only) — wire it in from day one

**Done when:** you can add "Brightside", see its DNS records turn green, invite a teammate with a role.

### M2 — Contacts and consent (≈ 2 weeks) · S3, S4
- [x] Contacts, topics, tags, consent records, consent wordings
- [x] Company-wide hashed suppression list
- [x] CSV import with all checks, attestation, quarantine
- [x] Engagement tier calculation
- [x] Contacts list with filters (tier, consent, tag)

**Done when:** a Mailchimp export can be imported and you see correct Active/Warm/… counts.

### M3 — First real send (≈ 3 weeks) · S10, S13, S5, S6, P3, P4
- [x] Brand layouts (locked) + templates (MJML editor, compiled on save)
- [x] Template checks: link allowlist, no shorteners, alt text, unsubscribe + address present
- [x] Campaign editor, audience snapshot with exclusion reasons
- [x] Send test to seed list
- [x] Sending worker: batches, rate limit, retries, quiet hours, test slice + hold
- [x] `List-Unsubscribe` + one-click POST, unsubscribe page (P4), preference center (P3)
- [x] Type-to-confirm screen (S6) and 10-min cancel window
- [ ] Test with the **SES mailbox simulator** (`success@`, `bounce@`, `complaint@simulator.amazonses.com`) — *test written (`SES_SIMULATOR_TEST`), not run yet: needs AWS credentials*

**Done when:** a real campaign goes to an internal list, unsubscribe works from Gmail's button.

### M4 — Feedback loop and brakes (≈ 2 weeks) · S7, S11, S8
- [x] SQS event reader, event → send mapping, auto-suppression
- [x] Click redirect + bot filter, open pixel
- [x] Brake evaluator (campaign, brand, account levels) + `safety_events`
- [x] Live send monitor (S7) — poll every 5–10 s
- [x] Campaign report (S11): delivered, human clicks, bots filtered, top links, by provider
- [x] Brand paused → guided recovery (S8)

**Done when:** sending to `complaint@simulator…` addresses pauses the campaign automatically.

### M5 — Governance (≈ 2 weeks) · S15, S16, S17, S9
- [x] Approvals engine (all types from S9), no self-approval
- [x] Safety limits settings: tighten instantly, loosen with approval, hard limits
- [x] Audit log screen with filters
- [x] Exports: approval, 24 h expiring link, watermark
- [x] Anomaly alerts (new country/device, volume spike, mass export → freeze + alert)

### M6 — Health and overview (≈ 2 weeks) · S1, S2
- [x] Brand health score (auth, reputation, list quality, engagement) recomputed hourly
- [x] Blocklist checks (Spamhaus DBL, SURBL, URIBL) for sending + link domains
- [x] Google Postmaster Tools API (Gmail spam rate per domain)
- [x] Warm-up schedule per new brand (daily caps rising over ~21 days)
- [x] Safety center (S1): all brands, shared SES account meters, "Needs attention"

### M7 — Segments, Contacts API, frequency cap (≈ 3 weeks) · S12, S14, P1, P2
- [x] Segment builder: rule JSON → SQL (ALL / ANY groups), live count
- [x] Contacts API: keys (hashed, scopes, IP allowlist, 90-day expiry), rate limits,
      double opt-in, idempotency, OpenAPI spec page
- [x] Confirmation email (P2) + confirm page (P1)
- [x] Cross-brand frequency cap (2/day, 6/week, 16 h same brand)
- [x] Re-permission emails for quarantined imports (200/day)

### Later (only if needed)
- Order data (Shopify or your store) → LTV, "Bought in last 90 days", cart abandoners,
  **revenue** in S11. This is a separate integration — S11 and S12 show it, so decide early.
- Postmark as backup sender / for confirmation emails on `notify.<brand>`
- Move `go.<brand>` redirects to a Cloudflare Worker if click traffic grows

**Total: ≈ 17 weeks for one developer** to reach the full design.
A useful internal version (M0–M4) is ≈ 10 weeks.

---

## 8. Cross-cutting

**Security**
- Passkeys only, Cloudflare Access in front, step-up for sensitive actions
- Secrets in AWS Secrets Manager / SSM; the app uses an IAM role, no static AWS keys
- API keys and link tokens: hashed / HMAC-signed, constant-time compare
- Postgres role for the app cannot UPDATE/DELETE `audit_log`
- Mask emails in logs

**Testing**
- Go unit tests for: brake math, tier calculation, segment → SQL, link token, bot filter
- Integration tests with `testcontainers-go` (real Postgres)
- `Sender` interface with a fake for tests; SES mailbox simulator for end-to-end
- Seed script with 3 brands and ~20k fake contacts to try the UI

**Operations**
- Daily Postgres backups (managed snapshots or `pg_dump` → S3), test a restore once
- Metrics: send rate, queue depth, SQS lag, brake trips → alert to Slack/email
- Keep raw `email_events` 13 months; keep `campaign_stats` forever

**Compliance**
- Postal address + unsubscribe in every email (enforced by the locked layout)
- Consent proof stored per contact (wording version, time, IP, source)
- GDPR erasure: delete contact data, keep only the hashed suppression entry
- DMARC: start `p=none`, move to `p=quarantine` once reports are clean (S1 reminds you)

**Monthly cost estimate (before SES)**
| Item | ≈ / month |
|---|---|
| ECS Fargate ARM, 2 small tasks (api + worker) | $10–15 |
| RDS Postgres `db.t4g.micro` + 20 GB | $15–20 |
| SQS, EventBridge, Secrets | < $2 |
| Cloudflare (Pages, Tunnel, Access ≤ 50 users) | $0 |
| **Total** | **≈ $30–40** |
| SES | $0.10 per 1,000 emails (+ $24.95/mo per dedicated IP, only if needed) |

A single VPS with Postgres on the same machine can bring this to ≈ $10/month,
but you lose IAM roles and managed backups.

---

## 9. Decisions to confirm

1. **Hosting:** AWS Fargate + RDS (recommended) or one VPS?
2. **SES region** and **shared vs dedicated IPs** (shared is fine under ~100k emails/month).
3. **Confirmation emails (P2):** SES with a separate configuration set, or Postmark as in the design?
4. **Store / order data:** which platform? Needed for revenue, LTV and purchase segments.
5. **EU contacts without tracking consent:** they never get clicks recorded, so how should
   their engagement tier be decided? (Suggestion: treat as Warm for 90 days after the last
   confirmed signup or preference change.)
6. **Brand DNS:** are all brand domains in your Cloudflare account? (Needed for `go.<brand>` and DNS checks.)
7. **Team size and deadline** — to turn the estimates above into dates.

---

## 10. Biggest risks

| Risk | Mitigation |
|---|---|
| SES production access delayed or denied | Apply in week 1, describe consent + bounce handling clearly |
| A bad import burns a brand's reputation | Import gate, quarantine, test slice, brand-level brakes |
| Double sends after a crash | `UNIQUE (campaign_id, contact_id)` + idempotent jobs |
| Bot clicks make stats and tiers wrong | Bot filter; tiers use human clicks only |
| `email_events` table grows too big | Monthly partitions, roll-ups, retention |
| Scope creep (21 screens) | Ship M0–M4 first, use it for real, then continue |

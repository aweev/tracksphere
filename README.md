# TrackSphere

Multi-tenant global shipment tracking platform — carrier webhook ingestion, an
exception engine that runs on a clock, and a branded customer tracking portal.

**Backend:** Go (single binary, API + worker) · **Data:** PostgreSQL only (schema,
RLS, job queue, read model) · **Frontend:** Next.js 15 + React 19 + TypeScript
**Hosting:** plain Docker Compose on Hetzner / DigitalOcean — no k8s, no Redis,
no Kafka, no ClickHouse.

The positioning, the wedge, and what we are deliberately **not** building are in
[`docs/strategy.md`](docs/strategy.md). How it works is in
[`docs/architecture.md`](docs/architecture.md). Why it is built this way is in
[`docs/adr/`](docs/adr/).

---

## Quick start (local, 4 commands)

```powershell
./scripts/dev.ps1 db-up      # Postgres 16 in Docker (port 5433)
./scripts/dev.ps1 migrate    # apply embedded migrations
./scripts/dev.ps1 seed       # demo tenant + shipments + exceptions
./scripts/dev.ps1 stack      # api :8080, worker, web :3100
```

Open <http://localhost:3100> and sign in as **demo@tracksphere.dev** / **DemoPassw0rd!**

Verify everything works:

```powershell
./scripts/dev.ps1 e2e        # full-stack assertions
./scripts/dev.ps1 rls-test   # tenant-isolation proof at the database layer
make prod-smoke             # health + status smoke check against the running API
```

> macOS/Linux: every task also exists as a `make` target — `make help`.

---

## What this product actually is

Not a map. A **map is table stakes** — every competitor ships one. This is:

1. **An exception engine that runs on a clock.** The most expensive failure in
   freight is *nothing happening*: a container sits at a terminal for six days
   because the vessel missed a transhipment connection. There is no event, so an
   event-driven system sees nothing, ever. Time-based detection and reconciliation
   are the core — [ADR 0007](docs/adr/0007-time-based-exception-detection.md).

2. **A read model that ranks by consequence.** `shipment_current` computes a
   0–100 risk score once, server-side, so the list is a to-do list ordered by
   consequence rather than a database view ordered by status enum —
   [ADR 0008](docs/adr/0008-shipment-current-read-model.md).

3. **A notification layer with a hard budget.** An operator may never receive
   more than N interrupting notifications an hour. Alert fatigue is the
   documented number-one reason ops teams abandon visibility platforms. Consent is
   captured, metered channels are double opt-in, and everything not interrupted is
   batched into a digest — [ADR 0009](docs/adr/0009-notification-consent-and-interrupt-budget.md).

4. **A portal the forwarder's own customer sees.** Tenant branding, live updates,
   and an unsubscribe that always works without an account.

## Capability status

| Capability | Status |
|---|---|
| Tenant isolation (`FORCE` RLS + non-superuser role), all tenant tables | verified, CI-gated |
| Out-of-order carrier events cannot regress a delivered shipment | tested |
| Exception rules as data: declarative, per-tenant, versioned, disable-able | shipped |
| Time-based detection (staleness, dwell vs lane norm, ETA slip) on a leased sweep | shipped |
| Alert reconciliation — exceptions auto-close when the condition clears | shipped |
| Exception ownership: assign, snooze, acknowledge, SLA countdown, escalation | shipped |
| Root-cause taxonomy + operator notes; closure recorded for carrier scorecards | shipped |
| Interrupt budget, severity routing, quiet hours, grouped digests | shipped, tested |
| Consent: double opt-in for SMS/WhatsApp, caps, audit ledger, one-click opt-out | shipped |
| Risk-scored shipments and a read model powering every list view | shipped |
| Live fleet map, risk-sorted lists, exception triage queue | shipped |
| Public portal: live SSE, tenant branding, per-shipment stream isolation | shipped |
| Carrier webhooks (HMAC) + poll adapter framework + simulator | shipped |
| Outbound webhooks with signed delivery log | shipped |
| API keys, RBAC (owner/admin/member), audit log, MFA, OIDC SSO | shipped |
| Entitlements, quotas, Stripe checkout | shipped, not yet enforced end-to-end |
| Native carrier API integrations (Maersk, DHL, …) | **not built** — webhooks + polling only |
| ML ETA model | **not built** — heuristic with a lane-learned fallback, labelled as such |
| SOC 2, multi-region, carbon reporting, cold-chain IoT | roadmap |

The old feature sheet claimed native carrier integrations, ML ETA, a 99.9% SLA
and GDPR deletion inside 72 hours. None were true and all were removed. See
[`docs/strategy.md`](docs/strategy.md#honest-state-of-the-claims).

---

## Repository layout

```
cmd/            api · worker · migrate · seed · keygen  (one image, two entrypoints)
internal/
  auth/         argon2id, session tokens, TOTP, AES-GCM sealing
  carriers/     internal-only carrier adapter framework (no third-party SDKs)
  config/       TRACKSPHERE_* loading + validation, trusted-proxy CIDRs
  db/           pool, tenant/system/public RLS contexts, embedded migrations
  httpapi/      router, middleware, handlers, SSE, rate limiting
  intel/        lane statistics, disruption zones
  model/        domain types + wire contracts
  notify/       channel providers (smtp / twilio / webhook / log)
  queue/        Postgres-backed job queue (outbox + SKIP LOCKED + dedup)
  readmodel/    shipment_current maintenance + risk scoring
  realtime/     SSE hub fed by LISTEN/NOTIFY, tenant- and shipment-scoped
  shipments/    repository, ingestion transaction, status transition guard
  workers/      event rules, sweep + reconciliation, notification router, poller
web/            Next.js app (App Router, Tailwind v4, TanStack Query, MapLibre)
deploy/         Dockerfiles, docker-compose.yml, .env.example
scripts/        dev.ps1 (workflow), e2e.ps1, rls_test.sql
docs/           strategy.md, architecture.md, adr/, api/openapi.yaml
```

---

## Four things worth understanding before touching the code

1. **Postgres is the whole data tier.** Tenant isolation (RLS), the job queue
   (`FOR UPDATE SKIP LOCKED` + transactional outbox), realtime fan-out
   (`LISTEN/NOTIFY`) and the read model all live in one database. That removes
   Redis, Kafka and a broker from the ops surface — ADR 0002, 0003, 0005.

2. **The app connects as a non-superuser role.** `FORCE ROW LEVEL SECURITY` is
   meaningless for a superuser, and Docker's `POSTGRES_USER` *is* one. The app
   uses `tracksphere_app`; only migrations use the owner. Without this, every
   isolation policy silently does nothing — ADR 0004.

3. **Nothing bypasses the tenant context by accident.** `db.WithTenant` is the
   only door for tenant queries. Three narrow escape hatches exist and are named
   explicitly: `SetSystem` (bootstrap, webhook and portal resolution),
   `SetPublicAccess` (public portal reads), and `ClearSystem`. All are
   transaction-local.

4. **No transaction may span network I/O.** The carrier poller was the one place
   that broke this: it held a pooled connection across outbound HTTPS calls,
   consuming two connections per job and letting a slow carrier abort the
   transaction via `statement_timeout`. Read → commit → call → ingest.
   See `internal/workers/poll.go`.

---

## Configuration

Copy `.env.example` → `.env`. Required: `TRACKSPHERE_DATABASE_URL`,
`TRACKSPHERE_SECRET_KEY` (generate with `go run ./cmd/keygen`),
`TRACKSPHERE_CARRIER_WEBHOOK_SECRET`.

Two DSNs are used **on purpose**:

```ini
# runtime role — restricted, RLS applies
TRACKSPHERE_DATABASE_URL=postgres://tracksphere_app:...@db:5432/tracksphere
# owner role — migrations only
TRACKSPHERE_MIGRATIONS_URL=postgres://tracksphere:...@db:5432/tracksphere
```

### Two settings that will bite you in production

```ini
# REQUIRED behind Cloudflare or any reverse proxy. Without it every request
# keys on the edge node's IP: one customer's traffic rate-limits every other
# customer's, and forwarding headers are ignored (which is the safe default,
# but it will look like the limiter is broken).
TRACKSPHERE_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12,127.0.0.1/32
TRACKSPHERE_TRUST_CLOUDFLARE_HEADERS=true

# Off by default. /metrics is reconnaissance.
TRACKSPHERE_EXPOSE_METRICS=false

# Externally visible origin, used for customer confirmation links.
TRACKSPHERE_PUBLIC_URL=https://track.acme.com
```

Also set `TRACKSPHERE_REGION` and `TRACKSPHERE_NOTIFY_*` / `SMTP_*` /
`TWILIO_*` — see `.env.example` for the full list.

## Testing

```bash
go test ./...                        # unit: crypto, TOTP, status guard, risk score, routing, rules
make check                           # vet + tests + web typecheck + build
pwsh scripts/e2e.ps1                 # full stack against real Postgres
pwsh scripts/dev.ps1 rls-test        # cross-tenant isolation proof
```

The load-bearing tests are the ones that encode product decisions, not coverage:
the status transition guard (`internal/shipments/status_test.go`), risk scoring and
its bounds (`internal/readmodel/readmodel_test.go`), and the notification budget
including the midnight-wrapping quiet window and the zero-cap case
(`internal/workers/notify_router_test.go`).

## Deployment

```bash
cd deploy && cp .env.example .env     # fill in secrets
docker compose up -d --build
docker compose exec api /app/seed     # optional demo data
```

Fits a **CPX21/CPX31 (2 vCPU / 4 GB)**. TLS, WAF, and DDoS protection come from
Cloudflare in front — see `deploy/` and `docs/architecture.md`. Backups must be
restore-tested; see `docs/adr/0002-postgres-only.md`.

## Demo credentials

| Email | Password | Role |
|---|---|---|
| demo@tracksphere.dev | DemoPassw0rd! | owner |
| agent@tracksphere.dev | AgentPassw0rd! | member |

Demo data is dev-only. Never seed in production.
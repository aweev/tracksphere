# TrackSphere

Multi-tenant global shipment tracking platform — carrier webhook ingestion,
live control-tower dashboard, public customer tracking portal.

**Backend:** Go (single binary, API + worker) · **Data:** PostgreSQL only (schema,
RLS, job queue) · **Frontend:** Next.js 15 + React 19 + TypeScript
**Hosting:** plain Docker Compose on Hetzner / DigitalOcean — no k8s, no Redis,
no Kafka, no ClickHouse.

---

## Quick start (local, 4 commands)

```powershell
./scripts/dev.ps1 db-up      # Postgres 16 in Docker (port 5433)
./scripts/dev.ps1 migrate    # apply embedded migrations
./scripts/dev.ps1 seed       # demo tenant + 6 shipments + alerts
./scripts/dev.ps1 stack      # api :8080, worker, web :3100
```

Open <http://localhost:3100> and sign in as **demo@tracksphere.dev** / **DemoPassw0rd!**

Verify everything works:

```powershell
./scripts/dev.ps1 e2e        # 42 assertions across the whole system
./scripts/dev.ps1 rls-test   # tenant-isolation proof at the database layer
```

> macOS/Linux: every task also exists as a `make` target — `make help`.

---

## What actually works today

| Capability | Status | Where |
|---|---|---|
| Multi-tenant isolation (FORCE RLS + non-superuser role) | ✅ verified | `internal/db/migrations/000002_rls.sql`, `scripts/rls_test.sql` |
| Email/password auth, argon2id, sessions | ✅ | `internal/auth`, `internal/httpapi/auth_*.go` |
| TOTP MFA (enroll → enable → challenge on login) | ✅ | `internal/auth/totp.go`, `auth_mfa.go` |
| Shipment CRUD + timeline | ✅ | `internal/shipments` |
| HMAC-signed carrier webhooks, idempotent replay, audit inbox | ✅ | `internal/httpapi/webhook_handlers.go` |
| Transactional job queue (outbox, SKIP LOCKED, backoff, reap) | ✅ | `internal/queue/queue.go` |
| Exception rules engine (customs hold, congestion, SLA breach) | ✅ | `internal/workers/rules.go` |
| NOTIFY notifications + ETA heuristic | ✅ | `internal/workers/notify_eta.go` |
| Realtime SSE (tenant-scoped, `LISTEN/NOTIFY` fan-out) | ✅ | `internal/realtime/hub.go` |
| Public portal with safe field projection | ✅ | `internal/httpapi/public_handlers.go` |
| Next.js dashboard, shipments grid, detail + map, public tracker | ✅ | `web/src/app` |
| Compose stack, multi-stage images, CI | ✅ | `deploy/`, `.github/workflows` |

Deliberately **not** implemented (documented, with interfaces in place):
real carrier API clients, Stripe billing, WhatsApp/SMS delivery, white-label
branding, SSO/SAML, IoT cold-chain ingest, ML ETA, ClickHouse analytics.
See `docs/architecture.md` for the phasing.

---

## Repository layout

```
cmd/            api · worker · migrate · seed · keygen  (one image, two entrypoints)
internal/
  auth/         argon2id, session tokens, TOTP, AES-GCM sealing
  config/       TRACKSPHERE_* environment loading + validation
  db/           pool, tenant/system/public RLS contexts, embedded migrations
  httpapi/      router, middleware, handlers, SSE
  model/        domain types + wire contracts
  queue/        Postgres-backed job queue (outbox + SKIP LOCKED)
  realtime/     SSE hub fed by LISTEN/NOTIFY
  shipments/    repository + ingestion transaction
  workers/      rules engine, notifier, ETA
web/            Next.js app (App Router, Tailwind v4, TanStack Query, MapLibre)
deploy/         Dockerfiles, docker-compose.yml, .env.example
scripts/        dev.ps1 (workflow), e2e.ps1 (42 assertions), rls_test.sql
docs/           architecture.md, adr/, api/openapi.yaml
design/         original HTML prototypes + brand references
```

---

## The three ideas worth understanding first

1. **Postgres is the whole data tier.** Tenant isolation (RLS), the job queue
   (`FOR UPDATE SKIP LOCKED` + transactional outbox), and realtime fan-out
   (`LISTEN/NOTIFY`) all live in one database. That removes Redis, Kafka, and a
   broker from the ops surface — see `docs/adr/0002`, `0003`, `0005`.

2. **The application connects as a non-superuser role.** `FORCE ROW LEVEL
   SECURITY` is meaningless for a superuser, and Docker's `POSTGRES_USER` *is*
   one. The app uses `tracksphere_app`; only migrations use the owner. Without
   this, every isolation policy silently does nothing (`docs/adr/0004`).

3. **Nothing bypasses the tenant context by accident.** `db.WithTenant` is the
   only door for tenant queries. Two narrow escape hatches exist and are named
   explicitly: `SetSystem` (bootstrap + webhook resolver) and `SetPublicAccess`
   (tracking portal), both transaction-local, both cleared/never-mixed with a
   tenant context.

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

## Testing

```bash
go test ./...                        # unit: crypto, TOTP, rules, ETA, signatures
make check                           # vet + tests + web build
pwsh scripts/e2e.ps1                 # full stack, 42 assertions
```

E2E proved: signed webhook acceptance + replay idempotency + signature
rejection, cross-tenant 404s (no enumeration), worker-derived alerts and
notifications, SSE delivery of both event types through the Next proxy, and a
clean 200 on `/login`.

## Deployment

```bash
cd deploy && cp .env.example .env     # fill in secrets
docker compose up -d --build
docker compose exec api /app/seed     # optional demo data
```

Fits a **CPX21/CPX31 (2 vCPU / 4 GB)**. TLS, WAF, and DDoS protection come from
Cloudflare in front — see `deploy/` and `docs/architecture.md`.

## Demo credentials

| Email | Password | Role |
|---|---|---|
| demo@tracksphere.dev | DemoPassw0rd! | owner |
| agent@tracksphere.dev | AgentPassw0rd! | member |

Demo data is dev-only. Never seed in production.

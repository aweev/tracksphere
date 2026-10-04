# Staging — harden-then-ship runbook (P0 + P1)

Target: one CPX21/CPX31 (2 vCPU / 4 GB) behind Cloudflare (TLS/WAF/DDoS).
Source of truth for config: `deploy/.env.example`. This doc is the checklist;
`scripts/e2e.ps1` is the proof.

## 0. Pre-flight (local, must be green)

```bash
go vet ./... && go test ./... -count=1
(cd web && npm run typecheck)
go run ./cmd/migrate --version   # expect 000011_billing_trial (or later)
```

## 1. Secrets (never reuse dev defaults)

```bash
go run ./cmd/keygen                                  # TRACKSPHERE_SECRET_KEY (≥32 chars)
openssl rand -base64 32                              # TRACKSPHERE_CARRIER_WEBHOOK_SECRET (≥32)
openssl rand -base64 24                              # POSTGRES_PASSWORD
openssl rand -base64 24                              # APP_DB_PASSWORD
```

Production guards refuse to boot on weak secrets (`config.Load`) — that is
the point. Per-carrier rotation without breakage:
`TRACKSPHERE_CARRIER_WEBHOOK_SECRETS="maersk:s1,dhl:s2"`.

## 2. Boot

```bash
cd deploy && cp .env.example .env   # fill in §1 + APP_VERSION=$(git rev-parse --short HEAD)
docker compose up -d --build
docker compose ps                    # all healthy (api/worker wait for postgres)
docker compose logs -f --tail=50 api worker
```

What happens: `api` applies embedded migrations under an advisory lock
(`000001…000011`), `worker` starts with `AUTO_MIGRATE=0`, `web` proxies
`/api/*` to `api:8080`. Version lands in `/api/v1/health` via `APP_VERSION`.

## 3. Prove isolation + behavior (staging host or via SSH tunnel)

```powershell
# RLS proof as the restricted role (vacuous as superuser — always app role)
docker compose exec -T postgres psql -U tracksphere_app -d tracksphere `
  -v ON_ERROR_STOP=1 -f /dev/stdin < ../../scripts/rls_test.sql

# Full E2E: 50+ assertions (auth, isolation, CRUD, webhooks, workers,
# portal masking, SSE, proxy, batch, API keys, RBAC, team)
pwsh ../../scripts/e2e.ps1 -ApiBase https://staging.example.com `
  -WebBase https://staging.example.com -WebhookSecret $env:STAGING_WEBHOOK_SECRET
```

Ship/no-ship: **0 failures required**. Known-good signal includes
`same tracking reusable across tenants`, `private by default`, `429` under
flood, `member → 403` on admin surface.

## 4. Operate

- Metrics: `GET /api/v1/metrics` (queue depth, DLQ, SSE, pool) — scrape it.
  Alert on `tracksphere_jobs_dead > 0` and `pending` growth.
- DLQ: `GET /api/v1/jobs/dead` (admin) → fix cause → `POST .../replay`.
- Retention: worker archives `done>30d` / `dead>90d` daily; `webhook_inbox`
  + `notifications` + `auth_events` are append-only audit — snapshot before
  pruning (out of scope for staging).
- Logs: JSON in production, one line per request with `request_id`
  (also returned as `X-Request-Id` by chi). No PII in logs.
- Backups: nightly `pg_dump` off-host before any migration change; rollback
  = previous image tag + `APP_VERSION` (migrations are forward-only).

## 5. Cloudflare + exposure notes

- Compose binds `127.0.0.1:8080/3000/5433` — nothing public except via the
  edge. TLS/WAF/DDoS = Cloudflare in front.
- Rate limits trust `RemoteAddr` post-`RealIP`: safe behind CF (loopback
  binds), re-evaluate if you expose the API directly.
- Security headers ship from Next (`nosniff`, `DENY` framing,
  locked-down CSP incl. OSM tiles, `Permissions-Policy`). HSTS lives at the
  edge — enable it in Cloudflare, not here.
- Never run `seed` (or `seed --reset`, which refuses prod anyway) against
  staging: create the org via `/register` and exercise the trial/billing
  surface in Settings.

## 6. Staging exit criteria for production

- [ ] e2e green against the staging URL (this file, §3)
- [ ] `/metrics` scraped, DLQ alert fires on a canary dead job
- [ ] Trial/billing card shows `14d left` on a fresh org (register-copy truth)
- [ ] SSE toast + exception badge observed on a carrier webhook
- [ ] Backup/restore rehearsed once

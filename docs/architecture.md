# TrackSphere Architecture

> Scope: what exists in this repository, why it is shaped this way, and what is
> intentionally deferred. Decisions with lasting consequences are recorded as
> ADRs in `docs/adr/`.

## 1. System shape

```
                    +--------------- Cloudflare (TLS / WAF / DDoS) --------------+
                    |                                                             |
  Carriers --HMAC-->|  POST /api/v1/webhooks/carriers/{carrier}                   |
                    |        |                                                    |
  Customers ------->|  GET  /api/v1/track/{trackingNumber}   (public portal)      |
                    |        |                                                    |
  Ops users ------->|  Next.js :3000 --/api/* rewrite--> Go API :8080             |
                    |        |                              |                     |
                    |        +-- EventSource /api/v1/stream <+ SSE (LISTEN/NOTIFY)|
                    +--------------------------------------+----------------------+
                                                           |
                                             +-------------v--------------+
                                             | PostgreSQL 16              |
                                             |  . business tables (RLS)   |
                                             |  . jobs   (outbox queue)   |
                                             |  . NOTIFY 'tracksphere'    |
                                             +-------------^--------------+
                                                           | SKIP LOCKED claims
                                             +-------------+--------------+
                                             | Go worker(s)               |
                                             |  rules . notify . ETA      |
                                             +----------------------------+
```

One Go image, two entrypoints (`api`, `worker`). Both are stateless and scale
horizontally; the worker uses `FOR UPDATE SKIP LOCKED` so N replicas never
double-process a job.

## 2. Request-to-event path (the core flow)

1. **Carrier webhook** -> signature verified (HMAC-SHA256 over the raw body,
   constant-time compare) -> the delivery is written to `webhook_inbox` **before**
   validation, so rejected payloads are still auditable and replayable.
2. **Ingestion transaction** (`shipments.service.IngestEvent`):
   - resolve the shipment by tracking number via the `app.system` read branch
     (tenant unknown at this point; `FOR UPDATE` serialises concurrent
     deliveries for the same shipment),
   - pin `app.tenant_id`, then clear the system flag (least privilege),
   - insert the timeline event with a `(tenant, dedup_key)` unique constraint ->
     **replays are idempotent**,
   - advance `status`/`eta`/`shipped_at`/`delivered_at`,
   - enqueue `shipment.evaluate`, `shipment.notify` and (when an ETA arrives)
     `shipment.eta` jobs **in the same transaction** - the transactional outbox
     guarantees no event is lost between commit and dispatch,
   - `pg_notify('tracksphere', ...)` which fires **on commit**.
3. **Worker** claims jobs, runs the pure rules engine, writes notifications,
   recalculates ETA, emits further notifications.
4. **SSE** - each API instance LISTENs on the channel, filters by `tenant_id`,
   and pushes to that tenant's subscribers only. Contract: SSE is a hint,
   queries are truth — `Last-Event-ID` resumes a per-connection sequence,
   missed events converge by refetch (the browser invalidates on every
   frame), not by replay. Do not build exactly-once flows on SSE.
5. **Browser** invalidates the affected TanStack Query keys
   (`shipment.updated`, `alert.changed`). One-click triage sends
   (`POST /shipments/{id}/notify|email-carrier`) bypass the interrupt budget
   by design: an explicit human action, not an automated rule.

## 3. Multi-tenancy model

| Layer | Mechanism |
|---|---|
| Database | `tenant_id` on every business table + `FORCE ROW LEVEL SECURITY` |
| Connection role | `tracksphere_app` - no SUPERUSER, no BYPASSRLS, not the owner |
| Tenant context | `db.WithTenant()` pins `app.tenant_id` transaction-locally |
| Portal reads | `db.SetPublicAccess()` - separate flag, only `is_public = true` |
| Bootstrap/webhook | `db.SetSystem()` - transactional, cleared after pinning |
| Auth tables | Deliberately not RLS'd; accessed only by unique key |

Read branches are mutually exclusive by construction: portal queries never set a
tenant, tenant queries never set the portal flag. Anything else fails **closed**
(zero rows). `scripts/rls_test.sql` asserts nine properties (cross-tenant
invisibility, WITH CHECK rejection, fail-closed reads, portal scoping, portal
write rejection, full-table fail-closed sweep, unpinned-write rejection, DLQ
tenant backfill) and must be run as `tracksphere_app` - as a superuser it
would pass vacuously.

## 4. Data model

`tenants / users / sessions / shipments / shipment_events / alerts / jobs /
webhook_inbox / notifications`

Notable choices:
- `shipment_events.dedup_key = "<carrier>:<carrier_event_id>"` with
  `UNIQUE (tenant_id, dedup_key)` -> idempotency at the storage layer.
- `alerts` has a partial unique index `(shipment_id, kind) WHERE status='open'`
  -> one open alert per kind per shipment, so replayed webhooks cannot cause
  alert storms.
- `webhook_inbox.shipment_id` has **no FK** - the audit trail must outlive
  shipments and stay writable for payloads that never resolve.
- `jobs` is both the work queue and the schedule (delay via `run_at`).

## 5. Security posture

- argon2id (m=64 MiB, t=1, p=4) hashing; uniform auth errors (no user
  enumeration); sessions store only a SHA-256 token hash; HttpOnly + SameSite=Lax
  cookie, `Secure` in production.
- MFA: TOTP secrets sealed with AES-256-GCM; login returns a 5-minute challenge
  token (`mfa_pending`) never attached to a cookie, deleted on use.
- Webhooks: HMAC-SHA256, constant-time comparison, 1 MiB body cap, full audit.
- Requests logged with method/path/status/duration/request-id; no PII in logs.
- Rate limiting is two-layered on the public routes: a source-address budget and
  an independent subject budget keyed on a fingerprint of the tracking number.
  Forwarding headers are believed **only** from `TRACKSPHERE_TRUSTED_PROXIES`;
  chi's `RealIP` is removed because it trusts `X-Forwarded-For` from any peer,
  which let a caller mint a fresh rate-limit bucket per request. See
  [ADR 0010](adr/0010-abuse-posture.md).
- Subscription consent: double opt-in for metered channels, per-shipment and
  per-recipient caps enforced inside the transaction, an append-only audit
  ledger, and an opt-out that never confirms whether an address was subscribed.
- CORS allows `Authorization`; without it browser-based API-key calls failed
  preflight while server-side clients worked.
- CSP: no `unsafe-eval` in production, https-only `frame-ancestors` (so the
  embeddable widget works and `DENY` does not block it), tenant logo origins
  allowed in `img-src`, HSTS set.
- Known gaps (documented, not hidden): no CSRF token (mitigated by
  `SameSite=Lax` + JSON-only parsing); `unsafe-inline` remains in the Next
  `script-src` pending nonce support (the Go API already emits per-request
  nonces). Key rotation exists: `go run ./cmd/keygen rotate` (multi-key
  `TRACKSPHERE_SECRET_KEYS`) + per-carrier webhook secrets; see
  `docs/SECURITY.md` and `scripts/expire_sessions.sql` for the leak procedure.

## 6. Frontend

Next.js App Router, React 19, TypeScript `strict`, Tailwind v4 (brand tokens in
`globals.css` mirror the original Control Tower kit), TanStack Query for server
state, MapLibre GL (CARTO light basemap, no key, overridable via
`NEXT_PUBLIC_TILE_URL`) for the route map. `/api/*` is
rewritten to the Go service so there is **one origin**: cookies work without CORS
and the SSE stream is untouched. MapLibre loads via `next/dynamic` `ssr:false`
because it touches `window` at import.

Performance budget (enforced by Lighthouse CI on `/track/[n]`): portal route
JS <80kB, LCP p75 <2.5s on throttled 3G, map chunk deferred until the status
sentence renders. Ops routes <120kB. `TriagePanel`/`CommandPalette`/
`CreateShipmentForm` load via `next/dynamic`.

## 7. Phasing

| Phase | Contents | Status |
|---|---|---|
| **1 - foundation** | schema+RLS, auth+MFA, ingestion, queue, rules, SSE, dashboard, portal, compose, CI | **done and verified** |
| **2 - growth** | generic HTTPS carrier polling clients, SMS+WhatsApp via Twilio (log fallback), exception-queue UI, tenant webhooks out (HMAC), Shopify/WooCommerce HMAC webhooks, Stripe billing (stdlib-only) | **shipped, needs hardening** (DLQ scoping, flood caps, billing idempotency proof) |
| **3 - enterprise** | OIDC SSO (Google, Entra), lane-stats ETA learning + digest/analytics, white-label branding, SOC 2 evidence pack | **shipped, gated by config** (hidden unless `SSO_*`/`STRIPE_*` set; no SAML, no TimescaleDB, no ClickHouse — all deferred) |

Non-goals for phase 1: k8s, Redis, Kafka/NATS, ClickHouse, microservices. Each is
revisited only against a measured trigger (ADR 0002/0003/0005).

## 8. Operational notes

- Migrations are embedded in the binary and applied under an advisory lock; the
  API runs them on boot unless `TRACKSPHERE_AUTO_MIGRATE=0`.
- Worker `reapStuck` returns jobs from crashed replicas after 5 minutes; retries
  use exponential backoff capped at 5 minutes, then `status='dead'`.
- Status codes: 400 validation, 401 unauthenticated/bad signature, 404 unknown
  **or not-visible** resources (no cross-tenant enumeration), 409 duplicate
  tracking number, 500 unexpected.

## 9. Verified behaviour (evidence)

`scripts/e2e.ps1` runs ~60 assertions against a live stack (§§1–11, incl. triage
routes, MCP gate, recovery codes, notify prefs, metered-SMS refusal, backup
freshness),
including: replay idempotency, tampered-signature
rejection, cross-tenant 404 on both shipment and timeline endpoints, worker-raised
alerts, SSE delivery of `shipment.updated` **and** `alert.changed` through the
Next proxy, and a 200 on `/login`. `scripts/rls_test.sql` independently proves
the six database-level isolation properties as the restricted role.
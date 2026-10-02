# TrackSphere Unicorn Blueprint — From Scaffold to Control-Tower OS

> Status: APPROVED vision. Source of truth for all future implementation.
> Stack: Go (single binary api+worker) · PostgreSQL 16 only · Next.js 15 + React 19 + TS · Docker Compose.
> Last updated: 2026-10-02

## 0. TL;DR

Stop selling "shipment tracking". Ship this:

**TrackSphere is the Control-Tower OS for 3PLs, forwarders, and high-growth DTC brands too big for AfterShip and too small for project44.**

Three promises — everything else serves them:

1. **Connect any carrier in hours, not weeks.**
2. **Never miss an exception** — every delay/hold/breach in ≤2 clicks with owner, SLA countdown, customer message ready.
3. **Make every delivery sell the next order** — branded portal + proactive comms that cut WISMO 40%+.

Personas: Ops Agent (daily) · Ops Manager (KPIs) · End Customer (anxiety) · Tenant Admin (billing/security).

North-star metrics: `time-to-first-carrier <2h` · `exception MTTR -50%` · `WISMO -40%` · `tracking quality >98%` · `ETA ±6h accuracy >90%`.

---

## 1. Audit verdict

| Dimension | Score | Verdict |
|---|---|---|
| Backend / multi-tenancy | 8/10 | `FORCE RLS + tracksphere_app + WithTenant/SetSystem/SetPublicAccess` is correct and rare. Keep this religion. See `internal/db/db.go`, `internal/db/migrations/000002_rls.sql`, `000004_app_role.sql`. |
| Reliability / scale | 4/10 | Poll queue, no DLQ, global `tracking_number UNIQUE`, inbox breaks on non-JSON, no rate-limit, pool exhaustion risk. Falls over ~10k/day. |
| Security / compliance | 5/10 | argon2id + TOTP + HMAC good. But: single global webhook secret, `is_public=true` default, no RBAC enforcement, no reset/recovery, weak dev defaults ship, no SOC2/GDPR story. Enterprise RFP = fail. |
| Frontend UX | 3/10 | 3-link sidebar, `Loading…`, dashed `Empty`, color-only pills, no mobile, missing labels, silent SSE, map with no route, raw `CUSTOMS_HOLD` leaked to customers. |
| Psychology / behavioral | 2/10 | Zero urgency, progress, loss-aversion, habit loops. Silent `live.ts` invalidation is anti-control-tower. |
| Industry fit | 4/10 | Solves "store a shipment". Market buys "prevent WISMO / kill check-calls / prove on-time / white-label". |
| Moat | 2/10 | Generic webhook + counts = weekend clone. No network effect, no normalization graph. |
| Operability | 3/10 | `.github/workflows/` empty, no `/metrics`/tracing/audit log, `seed --reset` can wipe prod. |

**Keep:** Postgres-as-everything, one Go image, one-origin Next proxy (`/api/*` rewrite), RLS discipline.
**Kill:** `is_public default true`, global webhook secret, global tracking uniqueness, demo creds in UI, silent updates.
**Build:** Exception-first OS, not tracking CRUD.

---

## 2. Ultimate IA (navigation)

```
Control Tower  (NEW: live map + exceptions + KPIs + digest)
Shipments      (filters, saved views, bulk, ⌘K, pagination)
Exceptions     (NEW — THE core page: severity/age/assignee/SLA, badge count)
Analytics      (NEW: on-time, carrier benchmark, lane heat, exports)
Customers      (NEW: portal preview, brand, notify-me, history)
Integrations   (NEW: carriers, Shopify/Woo, API keys, outbound webhooks)
Team & Settings(NEW: RBAC, MFA UI, audit log)
Billing        (NEW: plan, usage, trial enforcement)
Track          (public, separate layout, white-labeled)
```

Rules: every number drills to a pre-filtered view. Exceptions actionable in ≤2 clicks. Never `Loading…` or dashed box without a next step.

---

## 3. UI/UX master spec

### 3.1 Visual / interaction laws
1. `StatTile` clickable → filtered view. Add deltas/sparklines (`+4.2% WoW`, `96.8% on-time`).
2. Relative time everywhere: `in 2d (82% conf)`, `3d overdue · $210 risk`. Never bare `toLocaleString()`.
3. Route progress bar: `Origin━━●━━Destination` + milestones (Booked→Departed→Customs→Out-for-delivery).
4. Freshness + provenance: `updated 5m ago · Maersk verified · source: webhook`.
5. Toast + badge + `aria-live` on `shipment.updated` / `alert.changed`. Nav badge = heartbeat. Sound toggle.
6. Dual-vocab timeline: ops `CUSTOMS_HOLD — held at Rotterdam, docs needed` vs customer `Clearance in progress — +2 days`. Never leak internal codes.
7. Map: origin/destination pins + dashed polyline + moving dot + geofence + legend. Fix `center flash`, respect `prefers-reduced-motion`.
8. Empty states with CTA: `No exceptions 🎉 | Import CSV | Connect carrier | Load demo`. Loading = skeletons. Errors = retry + request-access.
9. Responsive: collapsible sidebar, `overflow-x-auto` tables, `p-4 md:p-8`. 375px must work — warehouse phones.
10. A11y P0: `htmlFor/id` on all labels (`login`, `register`, `CreateShipmentForm`), `aria-current` nav, focus trap, `autocomplete="one-time-code"` on MFA, icon+label pills (not color-only), text accent `#d95a00` (contrast), skip-link, per-route titles.

### 3.2 Trust chrome (closes deals)
Carrier badge, SLA pill, audit trail, copy-link/QR/share, `Notify me` subscribe, white-label logo/color/domain per tenant, terms/GDPR copy, status page link.

### 3.3 Key file targets
- `web/src/components/Shell.tsx` — 8-item nav + badges + collapse
- `web/src/lib/live.ts` — toasts + badge counts (currently silent invalidate)
- `web/src/components/ui.tsx` — drillable `StatTile`, accessible `StatusPill`, actionable `Empty`, skeletons
- `web/src/components/Timeline.tsx` — humanized dual-vocab
- `web/src/components/ShipmentMap.tsx` — route + pins + legend
- `web/src/app/page.tsx`, `shipments/page.tsx`, `shipments/[id]/page.tsx` — drill-downs, filters, pagination, publish toggle
- `web/src/app/track/` — white-label, subscribe, no `/` logo loop
- `web/src/app/login/page.tsx` — remove demo creds from prod, forgot-password stub

---

## 4. Psychology / behavioral spec

- **Loss aversion:** `$1,240 at risk · 3 breach in 6h` > `3 warnings`.
- **Goal gradient:** onboarding checklist `Connect carrier (1/4) → Import 10 → Invite teammate → Go live`.
- **Zeigarnik:** persistent exception badge + SLA countdowns + `2 need review`.
- **Reciprocity:** auto-drafted proactive message `We're on it — docs filed, new ETA Thu` — one-click send.
- **Celebration:** `Delivered 🎉 On-time 96.8%` + weekly digest `You saved 11 check-calls`.
- **Authority:** `Maersk verified`, `98.2% tracking quality`, carrier scorecards.
- **Defaults:** `is_public=false`, `Notify on exception=ON`, smart assignee.
- **Habit:** 9am digest + inbox-zero loop = daily open reason.

---

## 5. Industry / moat / GTM

**DTC:** branded portal, AI EDD hour-level, SMS/WhatsApp + Klaviyo, checkout promise, Apple Wallet, returns. Price $99–499/mo.
**3PL:** multi-shipper API, visibility without booking, eBOL/docs, client exception reports, white-label. Wedge: `carrier webhooks in hours` + guided connect UI (Connection Accelerator clone).
**Forwarder:** FCL/LCL+air+drayage+customs in ONE record, multi-leg, duty/tariff modeling, doc retrieval, timestamp reconciliation.

**Moat ladder:** 1) normalization + code-map + quality scoring → 2) lane-history EDD → 3) carrier graph + benchmarking → 4) embedded analytics API + Shopify app + MCP-native API. Data graph, not UI.

**Pricing wedge (2026):** flat platform + included events vs $0.03/track + $0.05/label. Publish tiers like AfterShip Essentials/Premium. Land mid-market, expand to enterprise platform fee.

**Compliance = sales weapon:** SOC2-II + ISO27001 roadmap, GDPR DPA + EU residency option + export/erase, PII minimization (tokenized public links, no precise lat/lng anon), RBAC, immutable audit log. Advertise like FourKites or lose RFPs.

---

## 6. Architecture ultimate

Keep soul: Postgres-only, one image two entrypoints, RLS, outbox (`SKIP LOCKED`), SSE (`LISTEN/NOTIFY`). No Redis/Kafka/ClickHouse until measured triggers (ADR 0002/0003/0005).

### P0 — prod blockers (do first)
| # | Fix | Files |
|---|---|---|
| P0-1 | `tracking_number UNIQUE` → `UNIQUE(tenant_id,tracking_number)`; 409→404 on cross-tenant | `000001_init.sql:58`, `repository.go:102` |
| P0-2 | Webhook error mapping: `ErrNotFound→404`, else `500+retry`; error taxonomy | `webhook_handlers.go:70-77` |
| P0-3 | Inbox tolerates non-JSON (`text` + `jsonb` generated) | `webhook_handlers.go:49-53` |
| P0-4 | Manual EventID uuid, `source='manual'` | `webhook_handlers.go:128` |
| P0-5 | Validate `limit/offset/status/carrier`; cursor pagination | `shipments_handlers.go:58-80` |
| P0-6 | Per-carrier/per-tenant secrets + timestamp window + allow-list | `config.go:70`, `webhook_handlers.go:45` |
| P0-7 | `is_public DEFAULT false` + `PATCH /shipments/{id}` publish | `000001_init.sql:71` |
| P0-8 | Rate-limit login/MFA/webhook/track | `auth_login.go`, `public_handlers.go` |
| P0-9 | Enforce `owner/admin/member` RBAC | `server.go`, `middleware.go` |
| P0-10 | Restore CI (`vet+test+web-build+rls+e2e`) | `.github/workflows/` (empty today) |
| P0-11 | Prod guards: secret entropy, `seed --reset` refuse in prod, stamp version | `config.go`, `seed/main.go`, `Dockerfile.api` |
| P0-12 | Password reset, email verify, session revoke, TOTP recovery, login audit | `internal/auth`, `internal/httpapi` |

### P1 — scale (next)
Queue `LISTEN jobs_added` + poll fallback, archive `done>30d/dead>90d`, DLQ UI + replay, per-poller workerID, single reaper, graceful drain, send outside TX. DB: `statement_timeout`, tunable pool, dedicated LISTEN pool, `security_invoker` view, `pg_trgm` GIN, `tenant_id`-leading indexes. API: bulk `:batch` + `Idempotency-Key`, `GET /webhooks/inbox`, `GET /jobs/dead`, outbound webhooks, API keys. Obs: `/metrics`, OTEL, audit table.

### P2 — moat
`internal/carriers/{maersk,dhl,fedex,ups}` `Track()` + sealed per-tenant creds + `shipment.poll` job + code-map. Stripe + quotas (`402`) + usage emitter. CDC → ClickHouse at 50M events. Shopify/Woo, Go/TS SDKs, MCP API.

---

## 7. Phased roadmap

**Phase 0 — Stop the bleeding (1–2 wks):** all P0 + a11y + responsive + skeletons + empties + remove demo creds + fix logo loop + SSE toasts + humanized timeline + route polyline + publish toggle + copy-link.

**Phase 1 — Exception-first (3–6 wks):** `/exceptions` queue + drillables + filters/pagination/⌘K/saved views + assign/note/resolve + SLA countdowns + notify draft + notification center + `/settings` (MFA UI, team, keys, webhooks-out).

**Phase 2 — Trust & money (6–12 wks):** branded portal + subscribe + history + docs (S3) + multi-leg + Shopify/Woo + Stripe + audit log + GDPR export/erase + retention.

**Phase 3 — Intelligence (3–6 mo):** AI carrier-detect + AI EDD + anomaly/tariff flags on tenant lane history + scorecards + scheduled reports + SSO + EU region + SOC2 pack + status page.

---

## 8. References
- `docs/architecture.md` — current system shape + phasing
- `docs/adr/0001–0006` — language, postgres-only, queue, RLS, SSE, compose
- `docs/api/openapi.yaml` — wire contracts
- `design/tracksphere_feature_list.html` — 42-feature source list
- `design/prototypes/` — Control Tower kit (6-item nav vision)
- Backend audit: `internal/*`, `cmd/*`, `deploy/`, `scripts/*`
- Frontend audit: `web/src/app`, `web/src/components`, `web/src/lib`

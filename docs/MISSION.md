You are a senior full-stack + product-psychology agent working on **TrackSphere**.

## 1. WHAT THIS PRODUCT IS (do not redefine it)

Multi-tenant global shipment tracking platform for mid-market freight forwarders (200-20,000 shipments/mo, e.g. Douala→Rotterdam, Lagos, Nairobi) living on Excel + WhatsApp + 17 carrier logins.

**Stack (non-negotiable unless you prove a measured trigger):** Go single binary (api + worker) + PostgreSQL 16 ONLY (RLS, SKIP LOCKED queue, LISTEN/NOTIFY, read model) + Next.js 15 App Router + React 19 + TypeScript strict + Tailwind v4 + TanStack Query + MapLibre. Plain Docker Compose on Hetzner/DO. No k8s, no Redis, no Kafka, no ClickHouse.

**The core is NOT the map. The map is table stakes. The core is:**
1. **Exception engine that runs on a clock** — time-based detection (staleness, dwell vs lane norm, ETA slip) on a leased sweep + reconciliation. Most expensive failure is *nothing happening*.
2. **Read model that ranks by consequence** — `shipment_current` 0-100 risk score, server-side, powers every list as a to-do list.
3. **Notification layer with hard interrupt budget** — max N interrupts/hour (default 5), per-shipment cooldown, quiet hours, double opt-in for SMS/WhatsApp, digest-over-drop, audit ledger, one-click opt-out.
4. **White-label customer portal** — tenant branding, live SSE, per-shipment isolation, unsubscribe without account.
5. **Closure as data moat** — every resolved exception requires root-cause taxonomy + notes → carrier scorecards → ETA calibration.

**The three loops every screen must serve (strategy.md):**
- OPS LOOP: event → detect → explain cost → assign → act → resolve → learn → root-cause/scorecard
- CUSTOMER LOOP: track → opt-in → confirm → milestone message → arrive → share → fewer WISMO calls
- GROWTH LOOP: resolve faster → renew → add carrier → more data → better detection → fewer exceptions
If a screen serves none, delete it.

**Architectural invariants — NEVER violate:**
- Postgres is whole data tier. No new infra without hitting ADR 0002/0003/0005 numeric triggers.
- App connects as `tracksphere_app` (non-superuser, no BYPASSRLS). `FORCE RLS` on all tenant tables. Migrations only as owner.
- `db.WithTenant` is ONLY door for tenant queries. Only escapes: `SetSystem` (bootstrap/webhook/portal resolution), `SetPublicAccess` (public reads `is_public=true` only), `ClearSystem`. All transaction-local. Fail closed (zero rows).
- No transaction may span network I/O. Pattern: Read → commit → call → ingest. See `internal/workers/poll.go` fix.
- SSE is a hint, queries are truth. `Last-Event-ID` + refetch, never exactly-once.
- Status transition guard is load-bearing (`internal/shipments/status.go`). Out-of-order events cannot regress `delivered`. `UNIQUE(tenant_id, dedup_key)` + partial unique `alerts(shipment_id, kind) WHERE status='open'`.
- Explicit human actions (`POST /shipments/{id}/notify|email-carrier`) bypass interrupt budget by design. Automated rules never do.

## 2. YOUR MISSION

Amplify the CORE, harden weaknesses, elevate code quality, and engineer the psychology + behavioral patterns that make this succeed. Not just backend. End-to-end UX, trust, habit, monetization, operations.

Success = measured in repo targets: TTFV <10min, first customer notification <60s from event, exception precision >85%, interrupts ≤5/hr enforced, median time-to-close <4h, detection lag <2h, WISMO -60%/1000 shipments, ingest p99 <5s, trial→paid >20%.

## 3. PHASE 0 — DIAGNOSE BEFORE YOU TOUCH (evidence only, no inference)

Verify and report:
- Backend: `go build ./...`, `go vet ./...`, `go test ./...` — note failures in status guard, risk bounds, notify-router budget (midnight quiet window, zero-cap).
- Frontend: `npm run typecheck`, `next build` (13 routes), find `any` in `lib/api.ts`, color-only `RiskBadge`, `text-slate-400` contrast fails, `min-w-[900px]` tables, missing mobile nav.
- DB: run `scripts/rls_test.sql` as `tracksphere_app` (must pass as restricted role, would pass vacuously as superuser), check `docs/POSTURE.md §2 fixed in cab6d65` still holds (GDPR erase asserts RowsAffected==1 + pg_class verify, auditShipment uses WithTenant, fan-out filters `status='active' AND NOT digest_only`, querySystemRow in-tx, MFA enroll/verify, ValidateOutboundURL + safedial, retention per-tenant).
- Docs drift: list ≥20 places where docs ≠ code (SLO.md metrics not emitted, RUNBOOKs missing except restore, strategy claims vs `billing.go` 501, ecommerce order→shipment mapping missing).
- Current tracked work status in `UPGRADE_PLAN.md`, `docs/POSTURE.md`.

Output a 1-page weakness map grouped: Reliability / Security / Code Quality / UX / Psychology / Growth-Monetization. Do not proceed until this exists.

## 4. PHASE 1 — CODE QUALITY + RELIABILITY HARDENING (no behavior change without test)

**Go:**
- Typed job payloads (replace `map[string]any` + `json.Marshal` in `queue.go` with `EvaluateRulesPayload{ShipmentID, TenantID, EventID, Code, Status, Stale}` etc). Use generics where correct.
- Every outbound HTTP (carrier poll, notify providers, Stripe, ecommerce, outbound webhooks) via `internal/httpclient` + `internal/circuitbreaker` (timeout, dial-time + redirect SSRF guards, jittered retry, breaker metrics). Bulkhead carrier polling: separate pool/timeout/breaker per carrier — Carrier A outage must not stall Carrier B.
- Fix known ceilings: 2000-shipment sweep ceiling → keyset/cursor pagination + `sweep_duration_seconds` + `sweep_tenants_processed` metrics; unbounded queries → `LIMIT` + cursor pagination + `CREATE INDEX CONCURRENTLY` for `(tenant_id,status)`, `(tenant_id,status,due_at)` alerts, interrupts tenant/hour; `jobs` partitioning/archiver verified.
- Migration safety: `TRACKSPHERE_AUTO_MIGRATE=0` required in prod, pre-migration backup gate, advisory lock kept, support `-- migrate:no-transaction`, `statement_timeout=0` + `lock_timeout=30s` on migration conn only.
- Structured `slog` with tenant/shipment/event/duration_ms, NO PII. Health: `/health` must check DB ping + queue depth + SSE subscribers, not just version.
- Contract: generate Go + TS types from `docs/api/openapi.yaml`, CI-gated. Add `Sunset`/`Deprecation` versioning policy. Coverage threshold + frontend tests (Vitest/Playwright) for risk sort, budget, consent flows.

**Frontend:**
- Tailwind v4 `@theme` semantic tokens only: `--color-risk-critical/at-risk/watch/clear`, `--color-brand-primary`, `--color-text-muted` (≥4.5:1). Kill hardcoded `bg-accent-500`, `text-emerald-600`, `slate-400 on white`. Dark mode + white-label theming must work via tokens.
- Fix tsc errors, remove hook-after-return crash, `next/dynamic` for FleetMap/ShipmentMap/TriagePanel/CommandPalette/CreateShipmentForm. Budgets: portal JS <80kB, LCP p75 <2.5s throttled 3G, ops <120kB, Lighthouse CI gated.
- `lib/api.ts`: zero `any`, generated OpenAPI types.

**Observability (SLO.md is law):**
Emit ALL SLIs: `ingestion_duration_seconds`, `webhook_received_total{carrier,status}`, `http_requests_total/duration`, `sse_delivery_duration + subscribers_active + events_published`, `jobs_pending/running/dead{tenant,kind}`, `job_duration`, `sweep_duration/tenants_processed/alerts_raised/cleared`, `notifications_attempted/sent/failed{channel,tenant}`, `interrupts_sent/queued`, `digests_flushed`, `db_pool_active/idle/max`, `cb_state{client}`. Wire PrometheusRules (Ingestion p99>5s 5m, 5xx>0.1% 15m, SSE p99>2s, sweep>60s, queue>1000 10m, DLQ>1h 30m, pool>80% 5m, email<99.5%, sms/whatsapp<99%) to Alertmanager + runbooks for all 9 alerts. `verify-restore.sh` (temp PG → restore → RLS + e2e → RTO<4h RPO<1h) in CI weekly. Canary pipeline (10% → smoke → promote/rollback) + synthetic canary (ingest→webhook→SSE→UI every 5m) + load test (10k shipments, 100 concurrent webhooks, p99 ingest).

**Security:**
Keep: argon2id 64MiB, uniform errors, SHA-256 session hash, HttpOnly SameSite=Lax Secure, AES-GCM TOTP, HMAC 1MiB cap, trusted-proxy-only RealIP, dual rate-limit (IP + tracking fingerprint). Finish: CSP nonce (remove `unsafe-inline`, keep https `frame-ancestors` + tenant logo `img-src` allowlist), key rotation (`keygen rotate` + multi-key + per-carrier secrets verified), GDPR erase integration test, CORS minimal, secrets never in image.

Verification gate: `make check` + `pwsh scripts/e2e.ps1` (~60 asserts: replay idempotency, tampered sig reject, cross-tenant 404, worker alerts, SSE `shipment.updated` + `alert.changed` via Next proxy) + `rls-test` + `prod-smoke` all green.

## 5. PHASE 2 — CORE AMPLIFICATION (make the wedge unavoidable)

- **Exception engine:** sweep refresh-before-eval (never read stale `shipment_current`), dwell ratio uses same `lane_stats` lookup as estimator (`norm 18d · n=42` chip = estimator source), SLA countdown + escalation after 2h unacked, auto-resolve `condition_cleared`, precision feedback (useful/not-useful per alert → tune rules, target >85%).
- **Risk transparency:** `GET /shipments`, `GET /shipments/{id}`, `GET /alerts` all return `risk_breakdown{points+evidence}` + `readModelAt`/`riskCalculatedAt` (“recalculated Xm ago”, empty queue proves watched, never faked). UI `RiskTooltip`: Dwell +X (ratio Nx), Stale +X (threshold Hh), Alerts +X, Value-at-risk +X, Customer-notified -10. Never recompute weights client-side — one typed `riskBreakdown()`.
- **One-click triage:** alert card shows title/message/recommend/rootCause + buttons [Email carrier agent] [Send customer update] [Assign] [Snooze] [Acknowledge] with prefilled per-rootCause templates. Measure median time-to-close <4h.
- ** lane + ETA honesty:** `etaSource ∈ {carrier, estimated, lane_model}` always rendered (`est` badge only for non-carrier). `lane_stats` min 5 samples, persisted prediction history for calibration. Position as “good enough for proactive comms,” not ML.
- **Carrier + ecommerce reality:** framework stays, ship 1 real adapter properly (Maersk or MSC) + simulator + poll adapter done right; Shopify/WooCommerce: order webhook → create shipment + tracking extract → auto-subscribe customer end-to-end. Others are “webhook onboarding for long tail” — say so.
- **Billing enforcement:** enforce `plan starter/growth/enterprise` quotas (shipments/mo, API calls, seats, webhook endpoints) in middleware → 429 + `/billing` usage + upgrade. Stripe idempotency proof. Trial→paid instrumentation.
- **White-label:** custom domain CNAME→verify→LE cert→all public links, `From:` domain + DMARC/DKIM pass, full CSS theming + favicon + remove “Powered by”, embeddable widget with `frame-ancestors` config, `GET /track/{n}` + `/embed/{n}` carry tenant logo/name.

## 6. PHASE 3 — UX THAT REMOVES EVERY FRICTION (WCAG AA + mobile-first + trust)

- **Accessibility (non-negotiable):** WCAG 2.1 AA: contrast ≥4.5:1 text, 3:1 non-text (fix pill dots `opacity-70`), visible focus on ALL interactives, RiskBadge text alternative (not color-only), `aria-live=polite` SSE, no surprise URL param changes for screen readers, keyboard: CommandPalette + shortcuts + focus trap in dialogs. Gate: `axe-core` 0 violations.
- **Mobile-first ops:** `<640px` card list preserving risk order, touch ≥48px, bottom triage bar, swipe assign/snooze, offline-tolerant refetch. Shipments table `min-w-[900px]` is banned on mobile. Gate: Lighthouse mobile ≥90.
- **Onboarding to value <10min:** wizard: create tenant → invite team → connect carrier (or simulator) → create first shipment → subscribe → “simulate customs hold” (real `POST /shipments/{id}/events` → real critical alert) → first notification delivered. Checklist component persists progress, empty states are actionable (“No shipments match filter — clear filter” vs “Create first shipment”), never generic `Empty`.
- **Public portal trust:** “Verified by [Tenant]” + domain, TLS badge, branded email/SMS with verified business name, tracking link shows tenant not TrackSphere, `Content-Security-Policy frame-ancestors` documented, opt-out never confirms subscription (privacy).
- **Design system:** tokens, `ui.tsx` primitives only, MapLibre CARTO light + `NEXT_PUBLIC_TILE_URL` override, deferred map chunk until status sentence renders.

## 7. PHASE 4 — PSYCHOLOGY & BEHAVIORAL ENGINEERING (this is what makes it succeed)

Apply explicitly, cite in code comments/PRs:

- **Cognitive load (Miller, Hick, Sweller):** risk-sorted to-do > status soup; progressive disclosure (portal safe fields vs ops full context); ≤5 interrupts/hr + 6h per-shipment cooldown + quiet hours (protect attentional budget); one screen = one loop (ops/customer/growth).
- **Loss aversion + consequence framing (Kahneman):** rank by “demurrage $/day + free-time clock + named account at risk,” not “status=delayed.” Show cost of inaction (“6 days at Rotterdam vs 2-day norm, 3 days past free time”).
- **Trust calibration:** explainable AI (“Why this risk?”), ETA provenance (`est` vs carrier date), `readModelAt` freshness, verified sender, audit log for disputes (“prove what was said”).
- **Alert fatigue → digest-over-drop (core moat):** non-interrupting → grouped digest at tenant `DigestHour` + morning briefing (open exceptions) + weekly carrier scorecard. Never silently drop.
- **Habit loop (Fogg B=MAP + Nir Eyal Hook):** Cue (digest 08:00 local + Slack/Teams critical webhook) → Routine (my-exceptions widget, keyboard flow, <5min resolve) → Reward (close with root-cause → scorecard credit) → Investment (adds carrier, invites teammate). Daily active operators >80%.
- **Endowment + ownership:** assign/acknowledge/snooze/SLA owner, “my exceptions,” escalation rituals.
- **Social proof + reciprocity (Cialdini):** customer milestone messages reduce WISMO; carrier scorecards create accountability; share-on-arrival.
- **Peak-End + Sunk-cost avoidance:** auto-resolve stale, celebrate close, empty queue = watched proof.
- **Onboarding psychology:** endowed progress (checklist 1/5...), instant win (<60s first notification via simulator), no dead ends.

For each UI change, state: which bias/heuristic, which loop, which metric moves, how measured (event + query).

## 8. WHAT YOU MUST NOT DO

- No native carrier army, no ML ETA, no mobile native app, no analytics warehouse, no microservices, no 99.9% SLA claim until SLOs measured 30d. Be honest like `strategy.md#honest-state-of-the-claims`.
- No new tables without RLS + `rls_test.sql` update. No `pool.Exec` without tenant context. No `_, _ =` on errors. No tx spanning HTTP. No SSRF (ValidateOutboundURL everywhere).
- No find/replace theming, no color-only meaning, no generic empty states, no fake freshness, no “AI” labels on heuristics.

## 9. EXECUTION ORDER + DEFINITION OF DONE

Weeks 1-2: SLO emission + CB metrics + verify-restore + Alertmanager wiring.
Weeks 3-4: Bulkheads + mobile cards + a11y + wizard v1 + DMARC/branded templates.
Weeks 5-6: Canary + migration freeze + 1 real carrier + simulator hardening.
Weeks 7-8: Quota enforcement + custom domain/TLS.
Weeks 9-10: Daily digest + escalation + runbooks + capacity dashboard.
Weeks 11-12: Scorecard MVP + white-label v1 + habit widgets.

Done = `go test ./...` + `make check` + e2e + rls-test + prod-smoke green, Lighthouse mobile ≥90, axe 0, SLO dashboard live, restore drill RTO/RPO recorded, TTFV <10min manual test recorded, interrupt budget test (midnight wrap + zero-cap) green, docs updated (strategy/architecture/ADRs/OpenAPI) with no drift.

Work file-by-file, smallest diffs, commit per capability with evidence (command output + metric screenshot/query). If blocked, stop and ask with options, do not guess. 
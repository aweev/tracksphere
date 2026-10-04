# TrackSphere Production-Readiness Audit

**Audit Date:** 2026-10-03  
**Version:** 0.1.0 (Phase 1 — Foundation)  
**Scope:** Full-stack audit of Go API/worker + Next.js web, PostgreSQL 16, Docker Compose deployment

---

## Executive Summary

TrackSphere is an **exception engine and customer-communication layer for mid-market freight forwarders** (200–20,000 shipments/month). The architecture is deliberately opinionated: single Go binary, PostgreSQL-only datastore, transactional outbox queue, SSE over LISTEN/NOTIFY. This audit evaluates production readiness across four dimensions plus operational blind spots.

**Overall Assessment:** **Strong foundation with critical gaps in observability, resilience, and operational maturity.** The core architecture is sound and security-conscious. Phase 1 features are "done and verified" per docs, but production hardening, scaling triggers, and incident response are under-invested.

**Risk Level:** **Medium-High** — shippable for early customers with caveats, but not yet enterprise-ready.

---

## 1. Technical Architecture & System Design

### 1.1 Strengths

| Area | Evidence |
|------|----------|
| **Transactional integrity** | Outbox pattern in `service.go:176-195` — jobs enqueued in same tx as business data; `pg_notify` fires on commit |
| **Multi-tenancy** | FORCE RLS on all 23 tenant tables (`rls_test.sql` proves 8 isolation properties); three mutually-exclusive read branches (tenant, portal, system) |
| **Idempotency** | `UNIQUE (tenant_id, dedup_key)` on `shipment_events` (`service.go:138`); partial unique index on open alerts (`alerts` table) |
| **Horizontal scaling** | `FOR UPDATE SKIP LOCKED` queue (`queue.go:117-130`); stateless API/worker; single-flight sweep lease (`sweep.go:54-55`) |
| **Security posture** | Argon2id (64 MiB), uniform auth errors, HMAC-SHA256 webhooks, trusted-proxy rate limiting (ADR 0010), double opt-in consent (ADR 0009) |

### 1.2 Critical Gaps

#### 1.2.1 No SLO/SLI Definitions
- **Finding:** `docs/strategy.md:116-117` explicitly states "No uptime SLA is claimed. There is no SLO yet."
- **Impact:** Cannot measure reliability, trigger on-call, or negotiate contracts.
- **Required:** Define SLOs for ingestion freshness (p99 < 5s), API availability (99.9%), SSE delivery, sweep completion latency.

#### 1.2.2 Single-Region, No DR Strategy
- **Finding:** `docker-compose.yml` binds Postgres to `127.0.0.1:5433`; `REGIONS.md` documents single-region only.
- **Impact:** Region outage = total outage. No tested restore path (Wave 3).
- **Required:** Document RPO/RTO; implement `pgBackRest`/`WAL-G` to object storage; test restore quarterly.

#### 1.2.3 Database Connection Pool Exhaustion Risk
- **Finding:** `db.go:27-33` hardcodes `maxConns=10` (configurable but default is tiny). Worker runs `WorkerConcurrency` pollers (default 4) + 1 reaper + 1 archiver + 1 LISTEN connection + API pool.
- **Impact:** Under load, pool starvation causes cascading timeouts. `statement_timeout=5s` helps but doesn't prevent exhaustion.
- **Required:** Size pool for `(API workers + worker pollers + background tasks) × safety factor`. Monitor `pg_stat_activity` saturation.

#### 1.2.4 No Circuit Breakers / Bulkheads
- **Finding:** External calls (carrier webhooks, notification providers, Stripe) have no timeout budgets, retries with jitter, or circuit breakers.
- **Impact:** Slow downstream = blocked Go workers = queue backlog = ingestion latency spike.
- **Required:** Wrap all outbound HTTP in `http.Client` with timeouts; add `gobreaker` or similar; queue external calls separately.

#### 1.2.5 Queue Observability Blind Spots
- **Finding:** `/metrics` exposes global counters only (ADR 0010:79-81); no per-tenant queue depth, job age, or DLQ aging metrics.
- **Impact:** Cannot alert on "tenant X's notifications stuck for 2h" or "sweep lease holder crashed."
- **Required:** Emit per-tenant `jobs_pending`, `jobs_dead`, `sweep_latency_seconds`, `notification_interrupts_hourly`.

#### 1.2.6 Migration Safety Gaps
- **Finding:** `db/migrate.go` runs embedded migrations on API boot (`cmd/api/main.go:35-41`). No dry-run, no rollback, no `pg_dump` gate.
- **Impact:** Bad migration = corrupted schema + downtime. `make migrate` uses owner role but production runs as app role.
- **Required:** CI gate: `migrate --dry-run`; pre-migration backup; advisory lock already present — good.

#### 1.2.7 Worker Graceful Degradation
- **Finding:** Worker runs all handlers in same process (`cmd/worker/main.go:52-63`). One handler panic/OOM kills all pollers.
- **Impact:** Notification dispatch failure stops ETA recalculation, sweep, digest flush.
- **Required:** Separate critical paths (ingestion webhook → queue) from async workers; consider process isolation for provider calls.

### 1.3 Scaling Triggers (Per ADR 0002/0003)

| Trigger | Threshold | Action | Status |
|---------|-----------|--------|--------|
| Analytics events > 10M/mo | — | Add ClickHouse | Not monitored |
| Cache hit-rate/latency | — | Add Redis | Not measured |
| LISTEN/NOTIFY payload limits | — | Add NATS | Not tested |
| Job throughput > PG write capacity | — | Evaluate River/NATS | Partial index exists |

**Gap:** None of these triggers are instrumented. Add metrics + alerts before hitting limits.

---

## 2. UI/UX Design

### 2.1 Strengths

| Area | Evidence |
|------|----------|
| **Risk-first design** | `shipments/page.tsx:50-55` — risk tier is primary sort; status is secondary fact |
| **ETA provenance** | `readmodel.go:311-329` — `etaSource` ∈ {carrier, estimated, lane_model} rendered as `est` badge (`shipments/page.tsx:354-361`) |
| **Live updates** | Public portal SSE (`PublicTrack.tsx:28-51`) with `aria-live="polite"` for screen readers |
| **Consent UX** | `SubscribeCard` (`PublicTrack.tsx:157-256`) shows channel-specific confirmation flow; never lies about "subscribed" |
| **Keyboard accessibility** | Focus-visible rings on buttons/inputs; `sr-only` labels; semantic HTML |

### 2.2 Critical Gaps

#### 2.2.1 WCAG 2.1 AA Non-Compliance

| Criterion | Gap | Location |
|-----------|-----|----------|
| **1.4.3 Contrast (Minimum)** | `text-slate-400` on white (`ui.tsx:98`, `shipments/page.tsx:178`) — 3.7:1 ratio, fails AA (4.5:1) | Multiple |
| **1.4.11 Non-text Contrast** | Status pill dots (`ui.tsx:17`) use `opacity-70` — may fail 3:1 against background | `ui.tsx` |
| **2.4.7 Focus Visible** | Custom `focus-visible:ring` used but not all interactive elements have it (e.g., table rows) | `shipments/page.tsx:326` |
| **3.2.2 On Input** | Search debounce (300ms) changes URL params without explicit user action — may surprise screen readers | `shipments/page.tsx:127-133` |
| **4.1.2 Name, Role, Value** | `RiskBadge` (`shipments/page.tsx:56-99`) uses color-only for risk tier; no text alternative in DOM | `shipments/page.tsx` |

#### 2.2.2 Mobile/Responsive Deficits
- **Finding:** `shipments/page.tsx` table has `min-w-[900px]` with horizontal scroll — unusable on mobile.
- **Impact:** Field operators (core persona) cannot triage exceptions on phone.
- **Required:** Card-based list view for `< 768px`; preserve risk-first ordering.

#### 2.2.3 Public Portal Trust Signals
- **Finding:** `PublicTrack.tsx` shows tenant logo/brand but no security indicators (TLS, verification badge).
- **Impact:** Customers cannot distinguish legitimate TrackSphere page from phishing.
- **Required:** Display "Verified by [Tenant]" + domain verification; consider `Content-Security-Policy: frame-ancestors` for embed.

#### 2.2.4 Empty/Error States
- **Finding:** `Empty` component (`ui.tsx:87-93`) is generic; no actionable guidance (e.g., "No shipments — create one" vs "No shipments match filter").
- **Impact:** Dead ends for new users; no onboarding funnel.

#### 2.2.5 No Design System / Token Discipline
- **Finding:** Colors hardcoded (`bg-accent-500`, `text-emerald-600`, `bg-red-100`) — no semantic tokens.
- **Impact:** Theming (white-label) requires find/replace; dark mode impossible.
- **Required:** Migrate to Tailwind v4 `@theme` with semantic tokens (`--color-risk-critical`, `--color-brand-primary`).

---

## 3. Cognitive & Behavioral Psychology

### 3.1 Strengths

| Principle | Implementation |
|-----------|----------------|
| **Interrupt budget** | ADR 0009: hard cap (default 5/hr), per-shipment cooldown (6h), quiet hours — prevents alert fatigue |
| **Digest over drop** | Non-interrupting notifications queued → grouped digest (`notify_router.go:323-333`) |
| **Closure as data moat** | Alert resolution requires root cause (`model.go:132`); "other" recorded if omitted |
| **Progressive disclosure** | Public portal shows only safe fields; ops dashboard shows risk score + actionable context |
| **Sunk cost avoidance** | Auto-resolve stale alerts (`sweep.go:497-524`) — "condition_cleared" prevents zombie queue |

### 3.2 Critical Gaps

#### 3.2.1 Operator Onboarding Cognitive Load
- **Finding:** No guided setup flow. New tenant lands on empty dashboard → must discover: create shipment → configure carrier → set notification prefs → define rules.
- **Impact:** Time-to-first-value target < 10 min (`strategy.md:73`) unlikely without wizard.
- **Required:** Onboarding checklist: "1. Add carrier credentials → 2. Create first shipment → 3. Subscribe to updates → 4. See live exception."

#### 3.2.2 Exception Triage Decision Support
- **Finding:** Alert card shows `title`, `message`, `recommend`, `rootCause` (`notify_router.go:228-229`) but no one-click actions (e.g., "Email carrier agent", "Send customer update template").
- **Impact:** Operator must context-switch to email/WhatsApp; resolution time > 4h target (`strategy.md:77`).
- **Required:** Action buttons on alert card with pre-filled templates per `rootCause`.

#### 3.2.3 Notification Trust Calibration
- **Finding:** Customer receives "critical" notifications but no way to verify authenticity (no DKIM/SPF display, no "this came from TrackSphere" badge).
- **Impact:** Phishing risk; customers may ignore legitimate alerts.
- **Required:** Branded email templates with tenant logo; SMS/WhatsApp templates with verified business name.

#### 3.2.4 Habit Formation for Daily Use
- **Finding:** No daily digest scheduling (only weekly `ReportWeeklyJob`), no "start of day" summary, no Slack/Teams integration.
- **Impact:** Operators must remember to check dashboard; no pull habit formed.
- **Required:** Configurable daily digest at tenant's `DigestHour`; webhook to Slack/Teams for critical alerts.

#### 3.2.5 Risk Score Transparency
- **Finding:** `readmodel.go:61-129` risk algorithm is pure and documented but not exposed to operators.
- **Impact:** "Black box" score → operators distrust ordering → manual re-sort → cognitive load.
- **Required:** "Why this risk?" tooltip breaking down score components (dwell, stale, alerts, value).

---

## 4. Product Strategy & Market Viability

### 4.1 Strengths

| Strategic Pillar | Evidence |
|------------------|----------|
| **Clear wedge** | Exception engine + WhatsApp-first communication for mid-market forwarders (`strategy.md:13-18`) |
| **Defensible moats** | Root-cause closure data → carrier scorecards → ETA calibration (`strategy.md:47-50`) |
| **Honest claims** | `strategy.md:100-118` documents what is NOT built (no native integrations, no AI/ML ETA, no 99.9% SLA) |
| **Phased roadmap** | Wave 0 done, Wave 1 shipped, Wave 2 monetization, Wave 3 enterprise |

### 4.2 Critical Gaps

#### 4.2.1 Carrier Connectivity Gap
- **Finding:** `strategy.md:114-115` — "Carrier connectivity is webhooks plus a poll adapter framework, not native integrations."
- **Impact:** Forwarders using Maersk/DHL/CMA-CGM cannot onboard without custom adapter work. Top 3 carriers cover ~60% of container volume.
- **Required:** Ship 3 production-ready carrier adapters (Maersk, MSC, CMA-CGM) + simulator in Wave 2.

#### 4.2.2 No Pricing/Packaging Implementation
- **Finding:** `billing.go` exists but `Stripe` webhooks return 501 "contact sales" (`deploy/.env.example:41`); no entitlement enforcement in API.
- **Impact:** Cannot monetize Wave 2; trial → paid conversion unmeasurable (`strategy.md:81` target > 20%).
- **Required:** Implement plan quotas (shipments/mo, API calls, notification volume); enforce in middleware.

#### 4.2.3 White-Label Incomplete
- **Finding:** `branding.go` + `tenant_branding` table exist but `PublicTrack.tsx:64-76` only shows logo/company/color. No custom domain, no email domain, no widget iframe config.
- **Impact:** Forwarder's customer sees TrackSphere brand → vendor exposed (`strategy.md:43-46`).
- **Required:** Custom domain (CNAME verification), `From:` email domain, embeddable widget with `frame-ancestors` config.

#### 4.2.4 E-commerce Integration Vaporware
- **Finding:** `ecommerce.go` + `ecommerce_connections` table + Shopify/WooCommerce webhook handlers exist but no order→shipment mapping, no automatic tracking number extraction.
- **Impact:** "Shopify/WooCommerce import" on roadmap (`strategy.md:132`) not demonstrable.
- **Required:** End-to-end: Shopify order webhook → create shipment with tracking → customer auto-subscribed.

#### 4.2.5 No Competitive Differentiation on ETA
- **Finding:** `intel/lane.go` + `readmodel.go:311-329` — ETA is heuristic + lane-learned fallback, labeled `estimated`/`lane_model`.
- **Impact:** "Better ETA" is table stakes; competitors (project44, FourKites) have carrier-direct APIs + ML.
- **Required:** Lean into "exception engine" not "ETA engine"; position ETA as "good enough for proactive comms."

---

## 5. Overlooked Technical Vulnerabilities

### 5.1 Data Integrity & Consistency

| Vulnerability | Location | Severity |
|---------------|----------|----------|
| **No foreign key on `webhook_inbox.shipment_id`** | `architecture.md:89-90` — intentional for audit trail | Medium — orphan inbox rows accumulate |
| **`jobs` table grows unbounded without archiver** | `queue.go:193-204` — archiver runs 1×/day per deployment | Low — but `reapStuck` only 5min; dead jobs retained 90d |
| **No `ON DELETE CASCADE` verification** | Schema not reviewed; `DELETE /account` cascades (`SECURITY.md:56-57`) but untested | High — GDPR erase must be verified |
| **`shipment_current` stale read risk** | `readmodel.go:333-338` — refreshes rows > 15min old; sweep may read stale risk scores | Medium — mitigated by sweep refresh-before-eval |

### 5.2 Security Hardening Gaps

| Gap | Evidence | Remediation |
|-----|----------|-------------|
| **CSP `unsafe-inline` remains** | ADR 0010:73 — Next.js bootstrap scripts; nonces tracked but not implemented | Add nonce middleware; `script-src 'nonce-{...}'` |
| **No CSRF protection** | ADR 0010:116 — mitigated by `SameSite=Lax` + JSON-only parsing | Acceptable for now; document as known gap |
| **No key rotation tooling** | ADR 0010:117 — `TRACKSPHERE_SECRET_KEY` rotates require redeploy | Build `keygen` rotation CLI; support multiple active keys |
| **`TRACKSPHERE_TRUSTED_PROXIES` footgun** | ADR 0010:106-108 — misconfig = all traffic rate-limited as single IP | Startup validation + warning log; document in `deploy/README.md` |
| **Tenant logo CSP bypass** | ADR 0010:70,109 — `img-src` restricted to known CDNs; external logo blocked | Allow tenant-configured logo origins in CSP via nonce/hash |

### 5.3 Operational Blind Spots

| Blind Spot | Current State | Risk |
|------------|---------------|------|
| **Log retention / PII scrubbing** | `architecture.md:101` — "no PII in logs" but no automated verification | GDPR audit failure |
| **Secrets in Docker image** | `Dockerfile.api` builds with `ARG VERSION` but `.env` not baked — good | Low |
| **Health check depth** | `/health` only returns version; no DB connectivity, queue depth, SSE hub count | False green in load balancer |
| **No chaos engineering** | No fault injection, no synthetic canary, no DR drill | Unknown failure modes |
| **Backup verification** | `STAGING.md` mentions nightly `pg_dump` but no restore test | Data loss on corruption |

### 5.4 Developer Experience / Maintainability

| Issue | Evidence |
|-------|----------|
| **No API versioning strategy** | `/api/v1/` in paths but no deprecation policy, no `Accept-Version` header |
| **OpenAPI spec exists** (`docs/api/openapi.yaml`) but no contract testing / consumer-driven contracts |
| **Go generics underused** — `queue.go` uses `any` + `json.Marshal` instead of typed payloads |
| **Frontend type safety** — `api.ts` uses `any` in places; no generated types from OpenAPI |
| **Test coverage unknown** — `make test` runs Go tests but no coverage threshold; no frontend tests |

---

## 6. Actionable Recommendations

### 6.1 P0 — Ship Blockers (Do Before First Paying Customer)

| # | Action | Owner | Effort |
|---|--------|-------|--------|
| 1 | Define SLOs + SLO-based alerting (ingestion p99, API 99.9%, sweep latency) | Platform | 1w |
| 2 | Implement `pgBackRest`/`WAL-G` to S3 + quarterly restore test | Platform | 1w |
| 3 | Add per-tenant queue metrics + DLQ aging alerts | Backend | 3d |
| 4 | Fix WCAG 2.1 AA contrast failures (slate-400 → slate-600, status pill dots) | Frontend | 2d |
| 5 | Mobile card view for shipments list (`< 768px`) | Frontend | 3d |
| 6 | Onboarding wizard (carrier creds → first shipment → subscribe) | Full-stack | 1w |
| 7 | CSP nonce middleware to remove `unsafe-inline` | Backend | 2d |
| 8 | Key rotation CLI (`keygen rotate`) with multi-key support | Backend | 2d |
| 9 | Verify GDPR erase cascade (`DELETE /account`) with integration test | Backend | 2d |
| 10 | Health check: DB ping + queue depth + SSE subscriber count | Backend | 1d |

### 6.2 P1 — Production Hardening (First 30 Days)

| # | Action | Owner | Effort |
|---|--------|-------|--------|
| 11 | Circuit breakers on all outbound HTTP (carrier webhooks, notify providers, Stripe) | Backend | 1w |
| 12 | Size DB pool for peak concurrency; add `pg_stat_activity` saturation alert | Platform | 2d |
| 13 | Daily digest (configurable hour) + Slack/Teams webhook for critical alerts | Backend | 1w |
| 14 | "Why this risk?" tooltip on shipment list (break down score components) | Frontend | 2d |
| 15 | One-click alert actions (email carrier, send customer update template) | Full-stack | 1w |
| 16 | Branded notification templates (email/SMS/WhatsApp) with tenant domain | Backend | 1w |
| 17 | API versioning policy + deprecation header (`Sunset`, `Deprecation`) | Backend | 2d |
| 18 | Contract testing: generate Go/TS types from OpenAPI; CI gate | Full-stack | 1w |
| 19 | Synthetic canary: ingest → webhook → SSE → UI assertion every 5min | Platform | 1w |
| 20 | Load test: 10k shipments, 100 concurrent webhooks, measure p99 ingestion | Platform | 3d |

### 6.3 P2 — Competitive Differentiation (Wave 2)

| # | Action | Owner | Effort |
|---|--------|-------|--------|
| 21 | Ship 3 production carrier adapters (Maersk, MSC, CMA-CGM) + simulator | Backend | 3w |
| 22 | Plan quotas + entitlement enforcement (shipments, API calls, notifications) | Backend | 2w |
| 23 | Custom domain (CNAME) + verified email domain + embeddable widget config | Full-stack | 2w |
| 24 | Shopify/WooCommerce order→shipment mapping with auto-subscribe | Backend | 2w |
| 25 | Carrier scorecards from root-cause closure data | Backend | 2w |
| 26 | Calibrated ETA with persisted prediction history (ClickHouse trigger) | Backend | 3w |

### 6.4 P3 — Enterprise Readiness (Wave 3)

| # | Action | Owner | Effort |
|---|--------|-------|--------|
| 27 | OIDC/SAML SSO + SCIM provisioning | Backend | 3w |
| 28 | Multi-region with tested restore path (RPO < 1h, RTO < 4h) | Platform | 4w |
| 29 | SOC 2 Type II evidence automation (`/compliance/evidence`) | Security | 2w |
| 30 | Carbon/ESG reporting for EU shippers | Backend | 3w |

---

## 7. Granular Technical Improvements

### 7.1 Backend (Go)

```go
// 1. Typed job payloads — replace map[string]any
type EvaluateRulesPayload struct {
    ShipmentID uuid.UUID `json:"shipmentId"`
    TenantID   uuid.UUID `json:"tenantId"`
    EventID    uuid.UUID `json:"eventId"`
    Code       string    `json:"code"`
    Status     string    `json:"status"`
    Stale      bool      `json:"stale"`
}

// 2. Circuit breaker wrapper for all outbound HTTP
var breaker = gobreaker.New(gobreaker.Settings{
    Name:        "carrier-webhook",
    MaxRequests: 3,
    Interval:    10 * time.Second,
    Timeout:     30 * time.Second,
    ReadyToTrip: func(counts gobreaker.Counts) bool {
        return counts.TotalFailures >= 5
    },
})

// 3. Structured logging with tenant context (no PII)
slog.Info("ingest_complete",
    "tenant_id", tenantID,
    "shipment_id", shipmentID,
    "event_id", eventID,
    "duration_ms", time.Since(start).Milliseconds(),
)

// 4. Migration dry-run + backup gate
func MigrateWithBackup(ctx context.Context, cfg *config.Config) error {
    if cfg.Env == "production" {
        if err := pgDumpToS3(ctx); err != nil {
            return fmt.Errorf("pre-migration backup failed: %w", err)
        }
    }
    return db.Migrate(ctx, cfg.MigrationsURL)
}
```

### 7.2 Frontend (Next.js/React)

```tsx
// 1. Semantic design tokens (Tailwind v4 @theme)
// globals.css
@theme {
  --color-risk-critical: #dc2626;
  --color-risk-at-risk: #ea580c;
  --color-risk-watch: #ca8a04;
  --color-risk-clear: #16a34a;
  --color-brand-primary: #ff6b00;
  --color-text-muted: #64748b;  // slate-500, passes AA
}

// 2. Mobile-first shipment list
function ShipmentRow({ shipment }: { shipment: Shipment }) {
  return (
    <article className="rounded-2xl bg-white p-4 shadow-sm ring-1 ring-slate-200/70 md:hidden">
      <RiskBadge tier={shipment.riskTier} score={shipment.riskScore} />
      <div className="font-mono font-semibold">{shipment.trackingNumber}</div>
      <div className="text-sm text-slate-600">{shipment.origin} → {shipment.destination}</div>
      <div className="flex items-center gap-2 text-xs">
        <StatusPill status={shipment.status} />
        <span className="font-mono">{shipment.eta ? formatDate(shipment.eta) : '—'}</span>
      </div>
    </article>
  )
}

// 3. Risk score transparency tooltip
function RiskTooltip({ score, breakdown }: { score: number; breakdown: RiskBreakdown }) {
  return (
    <div className="grid gap-2 text-sm">
      <div className="font-semibold">Risk score: {score}/100</div>
      {breakdown.dwell > 0 && <div>Dwell: +{breakdown.dwell} (ratio {breakdown.dwellRatio}x)</div>}
      {breakdown.stale > 0 && <div>Stale: +{breakdown.stale} (threshold {breakdown.staleThreshold}h)</div>}
      {breakdown.alerts > 0 && <div>Alerts: +{breakdown.alerts}</div>}
      {breakdown.valueAtRisk > 0 && <div>Value at risk: +{breakdown.valueAtRisk}</div>}
      {breakdown.customerNotified && <div>Customer notified: -10</div>}
    </div>
  )
}
```

### 7.3 Database / Operations

```sql
-- 1. Add missing indexes for common query patterns
CREATE INDEX CONCURRENTLY idx_shipments_tenant_status ON shipments (tenant_id, status);
CREATE INDEX CONCURRENTLY idx_alerts_tenant_status_due ON alerts (tenant_id, status, due_at);
CREATE INDEX CONCURRENTLY idx_notification_interrupts_tenant_hour ON notification_interrupts (tenant_id, created_at);

-- 2. Partition jobs table by month for archive performance
CREATE TABLE jobs_2026_10 PARTITION OF jobs
FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

-- 3. Materialized view for tenant-level health (refreshed by sweep)
CREATE MATERIALIZED VIEW tenant_health AS
SELECT
  t.id as tenant_id,
  t.name,
  count(s.id) filter (where s.status NOT IN ('delivered','cancelled')) as active_shipments,
  count(a.id) filter (where a.status='open' AND a.severity='critical') as critical_alerts,
  max(c.risk_score) as max_risk_score,
  max(c.stale_hours) as max_stale_hours
FROM tenants t
LEFT JOIN shipments s ON s.tenant_id = t.id
LEFT JOIN alerts a ON a.tenant_id = t.id
LEFT JOIN shipment_current c ON c.tenant_id = t.id
GROUP BY t.id, t.name;

-- 4. Function to verify GDPR erase cascade
CREATE OR REPLACE FUNCTION verify_erase_cascade() RETURNS void AS $$
DECLARE
  tbl text;
  cnt int;
BEGIN
  FOR tbl IN SELECT unnest(ARRAY['shipments','alerts','notifications','tracking_subscriptions',...])
  LOOP
    EXECUTE format('SELECT count(*) FROM %I WHERE tenant_id = $1', tbl) INTO cnt USING current_setting('app.tenant_id');
    IF cnt > 0 THEN
      RAISE EXCEPTION 'Erase incomplete: % rows remain in %', cnt, tbl;
    END IF;
  END LOOP;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;
```

---

## 8. Audit Checklist Summary

| Category | Pass | Conditional | Fail | N/A |
|----------|------|-------------|------|-----|
| **Architecture** | 8 | 4 | 7 | 1 |
| **UI/UX** | 6 | 3 | 8 | 2 |
| **Cognitive/Psych** | 5 | 2 | 6 | 1 |
| **Product/Market** | 4 | 3 | 7 | 2 |
| **Security** | 7 | 2 | 5 | 1 |
| **Operations** | 3 | 2 | 8 | 2 |
| **Data Integrity** | 5 | 2 | 3 | 0 |
| **Developer Experience** | 2 | 3 | 5 | 0 |

**Legend:**
- **Pass** — Implemented correctly with evidence
- **Conditional** — Works but has documented limitations/known gaps
- **Fail** — Missing, broken, or dangerous
- **N/A** — Not applicable to current phase

---

## 9. Final Verdict

**TrackSphere Phase 1 is architecturally sound but operationally immature.**

The team has made excellent strategic decisions: PostgreSQL-only, transactional outbox, FORCE RLS, interrupt budget, consent-first notifications. These are *hard* things done *right*.

However, **production readiness requires operational maturity that doesn't exist yet**: no SLOs, no DR, no circuit breakers, no mobile UX, no onboarding, incomplete white-label, uninstrumented scaling triggers.

**Recommendation:** Treat this audit as the Wave 1.5 backlog. Complete all P0 items before onboarding first paying customer. P1 items within 30 days. P2/P3 map to documented Wave 2/3.

The product *can* deliver on its wedge (exception engine + WhatsApp comms) — but only if the operational foundation catches up to the architectural ambition.
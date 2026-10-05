# TrackSphere Production-Readiness Remediation Backlog

**Audit Date:** 2026-10-04  
**Target:** Enterprise-readiness within 90 days  
**Owner:** Engineering Lead

---

## Priority Definitions

| Priority | Definition | SLA |
|----------|------------|-----|
| **P0** | Blocks enterprise contracts, causes data loss, or creates security exposure | Fix within 2 weeks |
| **P1** | Degrades reliability at scale, limits growth, or creates operational toil | Fix within 6 weeks |
| **P2** | Polish, differentiation, or long-term technical debt | Fix within 18 weeks |

---

## P0: Critical — Must Ship Before Enterprise Sales

| ID | Item | Domain | Owner | Effort | Acceptance Criteria |
|----|------|--------|-------|--------|---------------------|
| P0-1 | **SLO metric emission** — Emit all SLIs from SLO.md in workers (ingestion_duration, webhook_received, sweep_duration, notification_attempted/sent/failed, interrupt budgets) | Observability | Backend Eng | 1w | `/metrics?per_tenant=true` shows all SLIs with tenant labels; PrometheusRule alerts fire in staging |
| P0-2 | **Circuit breaker metrics** — Expose CB state (open/half-open/closed), failure counts, trip events for all outbound clients (carrier, notification, stripe, ecommerce, webhook) | Reliability | Backend Eng | 3d | `/metrics` includes `tracksphere_cb_state{client="..."}`, `tracksphere_cb_failures_total{client="..."}`; alert on `state=open > 1m` |
| P0-3 | **Restore drill automation** — `scripts/verify-restore.sh` that provisions temp PG, restores latest backup, runs RLS/schema/e2e tests, measures RTO/RPO | Platform | Platform Eng | 1w | Runs in CI weekly; outputs RTO/RPO; fails if RTO > 4h or RPO > 1h |
| P0-4 | **Per-tenant queue/DLQ alerting** — Integrate existing `TenantQueueDepthHigh` and `TenantDLQAging` PrometheusRules with Alertmanager; route to `#tenant-ops` + PagerDuty | Observability | Platform Eng | 2d | Alerts fire in staging when tenant queue > 1000 for 10m or DLQ age > 1h for 30m |
| P0-5 | **Dependency isolation** — Bulkhead carrier polling: separate connection pool, timeout, and circuit breaker per carrier; fail one carrier without stalling others | Reliability | Backend Eng | 1w | Carrier A outage does not delay Carrier B polls; queue depth stays bounded |
| P0-6 | **Mobile UX baseline** — Card layout for exception list on < 640px; touch targets ≥ 48px; risk badge accessible (not color-only) | Frontend | Frontend Eng | 1w | Lighthouse mobile ≥ 90; axe-core 0 violations on exception page |
| P0-7 | **Onboarding wizard** — Guided flow: create tenant → invite team → connect carrier → create first shipment → verify notification | Product | Full-stack Eng | 1w | New tenant reaches "first notification delivered" in < 10 min without docs |
| P0-8 | **Outbound trust signals** — Verified sender domain (DMARC/DKIM), tenant-branded email/SMS templates, tracking link shows tenant name + logo | Product | Full-stack Eng | 1w | DMARC pass on test send; customer sees tenant brand not "TrackSphere" in email/SMS |

---

## P1: High — Required for Scale & Operational Maturity

| ID | Item | Domain | Owner | Effort | Acceptance Criteria |
|----|------|--------|-------|--------|---------------------|
| P1-1 | **Canary deploy pipeline** — GitHub Action: build → staging canary (10% traffic) → smoke tests → promote/rollback | Platform | Platform Eng | 1w | `canary.sh` runs on PR; auto-promotes on green; rollback on metric regression |
| P1-2 | **Release freeze for migrations** — `db.Migrate` requires explicit flag in production; schema changes gated behind backward-compatible expand/contract | Platform | Backend Eng | 3d | Production migration requires `TRACKSPHERE_AUTO_MIGRATE=false` + manual approval |
| P1-3 | **Carrier coverage for top 10 lanes** — Implement real adapters for Maersk, MSC, CMA CGM, Hapag-Lloyd, COSCO, ONE, Evergreen, DHL, FedEx, UPS | Product | Backend Eng | 6w | Each carrier: poll succeeds in staging; events normalize to TrackSphere statuses; webhook verified |
| P1-4 | **Plan/quota enforcement** — Enforce `plan` (starter/growth/enterprise) limits: shipments/month, API calls, team seats, webhook endpoints | Billing | Backend Eng | 2w | 429 on quota exceed; admin sees usage in `/billing`; upgrade flow works |
| P1-5 | **Custom domain + brand trust** — Tenant configures `track.acme.com` → CNAME verified → TLS cert (Let's Encrypt) → all public links use custom domain | Product | Full-stack Eng | 2w | Tenant adds domain → DNS check → cert issued → `/track/xxx` works on custom domain |
| P1-6 | **Daily digest + escalation rituals** — Scheduled jobs: morning summary (open exceptions), escalation after 2h unacknowledged, weekly carrier scorecard | Product | Backend Eng | 1w | Digests sent at 08:00 local; escalation creates AlertNotifyJob; scorecard emailed Monday |
| P1-7 | **Runbook library** — Create markdown runbooks for all 9 alerts in SLO.md (ingestion, API, SSE, sweep, queue, DLQ, DB pool, notification, restore) | Operations | Platform Eng | 2w | Each runbook: detection → diagnosis → mitigation → verification; linked in Alertmanager annotations |
| P1-8 | **Capacity planning dashboard** — Grafana: queue throughput trends, DB growth, SSE fan-out, carrier latency percentiles | Observability | Platform Eng | 1w | 30d trends visible; scaling triggers documented (e.g., "add worker when p99 queue wait > 30s") |

---

## P2: Medium — Differentiation & Long-Term Health

| ID | Item | Domain | Owner | Effort | Acceptance Criteria |
|----|------|--------|-------|--------|---------------------|
| P2-1 | **Root-cause carrier scorecard** — Aggregate exception reason codes by carrier/lane; publish monthly "Carrier Reliability Report" | Product | Backend Eng | 3w | Report shows: carrier, lane, exception rate, avg delay, top root causes |
| P2-2 | **Multi-region DR (Wave 3)** — Streaming replication to secondary region; DNS failover; tested RTO < 4h | Platform | Platform Eng | 6w | Quarterly DR test passes; RTO < 4h measured; zero data loss (RPO < 1h) |
| P2-3 | **AI-assisted ETA + exception classification** — Fine-tune model on historical events → predict ETA + exception type + recommended action | Product | ML Eng | 8w | p50 ETA error < 4h; exception classification F1 > 0.85; action recommendation accepted > 60% |
| P2-4 | **Advanced analytics** — Lane intelligence: congestion heatmap, dwell time by port, carrier on-time % | Product | Backend Eng | 4w | Dashboard shows lane-level insights; exportable CSV |
| P2-5 | **Enterprise compliance pack** — SOC 2 Type II evidence export, audit log immutable archive, DPA template, HIPAA BAA option | Compliance | Security Eng | 6w | `/compliance/evidence` exports complete audit trail; DPA/BAA templates in repo |
| P2-6 | **White-label customer portal** — Full CSS theming, custom favicon, custom email templates, remove "Powered by TrackSphere" | Product | Frontend Eng | 3w | Tenant configures theme in `/branding`; public portal reflects all customizations |
| P2-7 | **Operator habit formation** — Start-of-day briefing, "my exceptions" widget, keyboard shortcuts, muscle-memory workflows | UX | Frontend Eng | 3w | Daily active operators > 80%; time-to-resolve-exception < 5 min median |
| P2-8 | **Webhook retry with idempotency keys** — Tenant outbound webhooks: exponential backoff, idempotency-key header, dead-letter after 72h | Reliability | Backend Eng | 2w | Webhook delivery rate > 99.9%; duplicate deliveries < 0.1% |

---

## 90-Day Execution Plan

### Weeks 1–2: Foundation (P0-1, P0-2, P0-3, P0-4)
- [ ] Emit all SLO metrics from workers and API
- [ ] Expose circuit breaker metrics
- [ ] Build and CI-integrate `verify-restore.sh`
- [ ] Wire PrometheusRules to Alertmanager

### Weeks 3–4: Reliability & UX (P0-5, P0-6, P0-7, P0-8)
- [ ] Bulkhead carrier polling
- [ ] Mobile card layout + accessibility
- [ ] Onboarding wizard v1
- [ ] Trust signals (DMARC, branded templates)

### Weeks 5–6: Release Safety & Carrier Coverage (P1-1, P1-2, P1-3 start)
- [ ] Canary pipeline
- [ ] Migration freeze gate
- [ ] Begin top-10 carrier adapters (parallel)

### Weeks 7–8: Monetization & Custom Domain (P1-4, P1-5)
- [ ] Plan enforcement
- [ ] Custom domain + TLS automation

### Weeks 9–10: Operations & Rituals (P1-6, P1-7, P1-8)
- [ ] Daily digest + escalation
- [ ] Runbook library complete
- [ ] Capacity dashboard

### Weeks 11–12: Differentiation Start (P2-1, P2-6)
- [ ] Carrier scorecard MVP
- [ ] White-label theming v1

---

## Enterprise-Readiness Checklist (Gate for Enterprise Sales)

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | SLOs defined, measured, alerted | 🟡 Partial | SLO.md exists; metrics not fully emitted |
| 2 | DR tested quarterly, RTO < 4h, RPO < 1h | 🔴 No | Runbook exists; never tested |
| 3 | Per-tenant isolation proven (RLS tests pass post-restore) | 🟡 Partial | RLS tests exist; not run post-restore |
| 4 | Carrier coverage for customer's lanes | 🔴 No | Only fake carrier works end-to-end |
| 5 | Plan enforcement + billing operational | 🟡 Partial | Stripe checkout works; no quota enforcement |
| 6 | Custom domain + brand trust | 🔴 No | Not implemented |
| 7 | Mobile-safe operator experience | 🔴 No | Desktop-only tables |
| 8 | Onboarding to value < 10 min | 🔴 No | Manual setup required |
| 9 | Outbound communication trust (DMARC, branding) | 🔴 No | Generic sender |
| 10 | Canary deploys + migration safety | 🔴 No | Direct deploy to prod |
| 11 | Runbooks for all critical alerts | 🔴 No | Only restore runbook exists |
| 12 | Incident process with blameless postmortems | 🟡 Template only | Template in RUNBOOK_RESTORE.md |

**Gate:** All 🟢 required before enterprise contract signature.

---

## Owner Assignment

| Role | Person | P0 Items | P1 Items | P2 Items |
|------|--------|----------|----------|----------|
| Backend Eng | TBD | P0-1, P0-2, P0-5 | P1-3, P1-4 | P2-1, P2-3, P2-4, P2-8 |
| Frontend Eng | TBD | P0-6, P0-7 (UI) | P1-5 (UI) | P2-6, P2-7 |
| Platform Eng | TBD | P0-3, P0-4 | P1-1, P1-2, P1-7, P1-8 | P2-2 |
| Full-stack Eng | TBD | P0-7 (API), P0-8 | P1-5, P1-6 | P2-5, P2-6 |
| Security Eng | — | — | — | P2-5 |

---

## Tracking

- **Board:** GitHub Project "TrackSphere Production Readiness"
- **Weekly sync:** Monday 10:00 UTC — review burndown, unblock, reprioritize
- **Monthly review:** Last Friday — SLO compliance, alert noise, capacity, DR test results
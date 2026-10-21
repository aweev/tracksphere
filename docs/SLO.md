# TrackSphere Service Level Objectives (SLOs)

**Version:** 2.0 · 2026-10-21
**Supersedes:** v1.0 (2026-10-03). v1.0 listed metrics as "key metrics to emit"
that were never incremented anywhere in the code, and an alert/pager/backup
stack that was never deployed. This version describes only what exists in code
today, and what is explicitly blocked. See `docs/POSTURE.md` §4 and
`UPGRADE_PLAN.md` Phase 2 for the work to close the gaps.

**Standing rule:** a doc that describes a system must name the code that makes
it true. If a claim and the code disagree, the code is reality and the doc is
broken.

---

## 1. Metrics emitted today (verified against code, 2026-10-21)

Every name below is a real Prometheus series incremented by a named call site.

### 1.1 API process (scrapeable via `GET /api/v1/metrics`,
gated by `TRACKSPHERE_EXPOSE_METRICS`; see the `/metrics is reconnaissance`
warning in the README)

| Series | Call sites |
|---|---|
| `tracksphere_ingestion_duration_seconds{carrier,source}` | `internal/httpapi/webhook_handlers.go` |
| `tracksphere_webhook_received_total{carrier,status}` (`ok/stale_timestamp/invalid_signature/ingest_failed/flood/bad_event`) | `internal/httpapi/webhook_handlers.go` |
| `tracksphere_circuit_breaker_state{client}` | `internal/httpclient/httpclient.go` |
| `tracksphere_circuit_breaker_failures_total{client}` | `internal/httpclient/httpclient.go` |
| `tracksphere_circuit_breaker_trips_total{client}` | `internal/httpclient/httpclient.go` |
| `tracksphere_db_pool_connections_active/idle/max` | `cmd/api/main.go` → `metrics.UpdateDBPoolMetrics` |

### 1.2 Per-tenant DB-derived gauges (API process, query-computed at scrape
time, `?per_tenant=true` + admin session or API key)

| Series | Source |
|---|---|
| `tracksphere_jobs_pending{tenant,kind}` | `internal/httpapi/metrics.go` |
| `tracksphere_job_age_seconds{status="dead",tenant}` | `internal/httpapi/metrics.go` |
| `tracksphere_sse_subscribers{tenant}` | `internal/httpapi/metrics.go` |
| `tracksphere_interrupts_sent_total{tenant,severity}` (last hour) | `internal/httpapi/metrics.go` |
| `tracksphere_interrupts_queued_total{tenant}` | `internal/httpapi/metrics.go` |
| `tracksphere_active_shipments{tenant}` | `internal/httpapi/metrics.go` |
| `tracksphere_notifications_sent_total` / `..._failed_total{tenant,channel}` (last hour) | `internal/httpapi/metrics.go` |

### 1.3 Worker process (incremented in `cmd/worker` — **currently invisible**,
because the worker starts no HTTP listener; blocked on `UPGRADE_PLAN` P2-M1)

| Series | Call sites |
|---|---|
| `tracksphere_sweep_duration_seconds` | `internal/workers/sweep.go` |
| `tracksphere_sweep_tenants_processed_total` | `internal/workers/sweep.go` |
| `tracksphere_sweep_alerts_raised_total{severity}`, `..._cleared_total`, `..._ceiling_hits_total{tenant}` | `internal/workers/sweep.go` |
| `tracksphere_notifications_failed_total{channel,tenant,error}` | `internal/workers/notify_router.go` (metered-disabled path only) |
| `tracksphere_interrupts_sent_total{tenant,severity}`, `..._queued_total{tenant,reason}` | `internal/workers/notify_router.go` |
| `tracksphere_digests_flushed_total{tenant}` | `internal/workers/digest.go` |

Circuit-breaker series (§1.1) are also incremented in the worker and are
likewise unscrapable until P2-M1 lands.

## 2. Not emitted (declared but dead — do not query these series)

`tracksphere_http_requests_total`, `tracksphere_http_request_duration_seconds`,
`tracksphere_sse_subscribers_active`, `tracksphere_sse_delivery_duration_seconds`,
`tracksphere_sse_events_published_total`, `tracksphere_jobs_running`,
`tracksphere_jobs_dead` (gauge form), `tracksphere_job_duration_seconds`,
`tracksphere_notifications_attempted_total`, `tracksphere_notifications_sent_total`
(counter form §1.1-style), `tracksphere_jobs_pending` (worker gauge form — the
per-tenant §1.2 gauge above is the live variant).

Until `UPGRADE_PLAN` P2-M2 lands, **no SLI can be computed for**: API
availability (5xx rate), SSE delivery latency, notification delivery, job
duration. The corresponding objectives in §4 are marked `BLOCKED`.

## 3. Objectives

| SLI | Target | Window | Status |
|-----|--------|--------|--------|
| Ingestion freshness (webhook → visibility, p99) | < 5 s | 30 d | **Measurable** — `tracksphere_ingestion_duration_seconds` |
| Webhook handling failure rate | < 1% | 30 d | **Measurable** — `tracksphere_webhook_received_total{status!~"ok"}` |
| API availability | ≥ 99.9% | 30 d | **BLOCKED** — no HTTP request metrics yet (P2-M2) |
| SSE delivery latency | < 2 s | 30 d | **BLOCKED** — needs P2-M2 + P2-M1 |
| Sweep completion (lease → all tenants, p99) | < 60 s | 30 d | **BLOCKED** — series exist but only in the worker process (P2-M1) |
| Notification delivery (email) | ≥ 99.5% | 30 d | **BLOCKED** — attempted counter absent (P2-M2) |
| Notification delivery (SMS/WhatsApp) | ≥ 99% | 30 d | **BLOCKED** — attempted counter absent (P2-M2) |
| Per-tenant queue depth | < 1 000 pending | ad hoc | **Measurable** — `tracksphere_jobs_pending` (§1.2) |
| Per-tenant DLQ aging | < 1 h dead | ad hoc | **Measurable** — `tracksphere_job_age_seconds` (§1.2) |
| DB pool saturation | < 80% | ad hoc | **Measurable** — §1.1 pool gauges |

Error-budget burn-rate policy (page at 1×–10× per §3 v1.0 — policy is retained
but can only bind the `Measurable` rows; no alerting stack is deployed yet
(§5), so today these are measurement targets, not paged contracts).

## 4. Alert rules

Rules below are **valid expressions** — every series they reference is emitted
today. They are documentation of intent; **no Alertmanager or Prometheus
deployment exists in this repo yet** (compose ships none). Rules depending on
blocked series (§2) are deliberately omitted rather than kept non-executable —
they return as P2-M3 lands.

```yaml
# docs/SLO.md section 4 — valid against emitted series only (2026-10-21)
groups:
- name: tracksphere-slos
  rules:
  - alert: IngestionFreshnessSLOBreach
    expr: |
      histogram_quantile(0.99, rate(tracksphere_ingestion_duration_seconds_bucket[5m])) > 5
    for: 5m
    labels: { severity: critical, slo: ingestion_freshness }
    annotations:
      summary: "Ingestion p99 > 5s for 5m"
      runbook: https://todo/P3-R4-runbook-ingestion

  - alert: TenantQueueDepthHigh
    expr: tracksphere_jobs_pending > 1000
    for: 10m
    labels: { severity: warning }
    annotations:
      summary: "Tenant {{ $labels.tenant }} queue depth > 1000 for 10m"
      runbook: https://todo/P3-R4-runbook-queue

  - alert: TenantDLQAging
    expr: tracksphere_job_age_seconds{status="dead"} > 3600
    for: 30m
    labels: { severity: critical }
    annotations:
      summary: "Tenant {{ $labels.tenant }} has a dead job older than 1h"
      runbook: https://todo/P3-R4-runbook-dlq

  - alert: DBPoolSaturation
    expr: |
      tracksphere_db_pool_connections_active / tracksphere_db_pool_connections_max > 0.8
    for: 5m
    labels: { severity: warning }
    annotations:
      summary: "Database connection pool > 80% utilized"
      runbook: https://todo/P3-R4-runbook-db-pool

  - alert: CircuitBreakerOpen
    expr: tracksphere_circuit_breaker_state > 1
    for: 1m
    labels: { severity: warning }
    annotations:
      summary: "Circuit breaker for {{ $labels.client }} open > 1m"
```

## 5. Backup & restore (as actually deployed)

Nightly `pg_dump -Fc` sidecar (`deploy/backup.sh` via the `backup` compose
service), keep 7 dumps on the `pgbackups` volume, each run recorded in
`backup_runs`. `GET /api/v1/statusz` reports
`.backup.lastSuccess/.ageHours` and degrades past 30h without a proven backup.
**Copy off-host is mandatory** (S3/R2/rsync); sidecar "off-host" is a manual
step today.

- `pgBackRest`/WAL-G PITR is **not deployed** — no WAL archive exists. Restore
  procedure + SWOT of the SEV-3 alternative (latest dump + re-ingest from
  `webhook_inbox` / carrier re-poll) are documented in
  `docs/RUNBOOK_RESTORE.md` §3–§4.
- Restore drill history (RTO/RPO measurements) lives in
  `docs/RUNBOOK_RESTORE.md` Step 0; automation target `UPGRADE_PLAN` P3-R1.

## 6. Dashboards & alert routing (not present)

Grafana dashboards, Alertmanager routing, PagerDuty/Slack wiring: **none in
repo**. Until then §3 is targets, §4 is fire-drill material. Landed with
P2-M4.

## 7. Runbooks

Existing: `docs/RUNBOOK_RESTORE.md` (with a dated 2026-10-08 drill log).

Planned (authored per-alert when alerting schema lands, P3-R4; do not write
placeholders sooner — they rot into fiction like v1.0 §8):
ingestion, API availability, SSE delivery, sweep stall, queue backlog, DLQ,
DB pool, notification delivery.

## 8. Review

| Review | Frequency | Output |
|--------|-----------|--------|
| This doc vs. emitted metrics | Quarterly + at every Phase-2 change | Metric-to-code grep proof, drop dead series |
| Targets vs. observed | Quarterly (after first dashboards) | Adjust targets/budgets or accept risk |

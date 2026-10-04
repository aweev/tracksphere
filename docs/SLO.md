# TrackSphere Service Level Objectives (SLOs)

**Version:** 1.0  
**Effective:** 2026-10-03  
**Review:** Quarterly

---

## 1. Service Level Indicators (SLIs)

| SLI | Description | Measurement |
|-----|-------------|-------------|
| **Ingestion Freshness (p99)** | Time from carrier webhook receipt to shipment visibility in API | `histogram_quantile(0.99, rate(ingestion_duration_seconds_bucket[5m]))` |
| **API Availability** | Successful HTTP responses (2xx/3xx) / total requests | `sum(rate(http_requests_total{status=~"2..|3.."}[5m])) / sum(rate(http_requests_total[5m]))` |
| **SSE Delivery Latency** | Time from `pg_notify` to browser `message` event | `histogram_quantile(0.99, rate(sse_delivery_duration_seconds_bucket[5m]))` |
| **Sweep Completion** | Time from sweep lease acquisition to all tenants processed | `histogram_quantile(0.99, rate(sweep_duration_seconds_bucket[5m]))` |
| **Notification Delivery** | Notifications sent / notifications attempted (per channel) | `sum(rate(notifications_sent_total[5m])) / sum(rate(notifications_attempted_total[5m]))` |
| **Queue Depth (per tenant)** | Pending jobs for tenant | `jobs_pending{tenant="..."}` |
| **DLQ Aging** | Oldest dead job age per tenant | `max(job_age_seconds{status="dead", tenant="..."})` |

---

## 2. Service Level Objectives (SLOs)

| SLI | Target | Window | Error Budget | Alert Threshold |
|-----|--------|--------|--------------|-----------------|
| Ingestion Freshness (p99) | < 5 seconds | 30 days | 43.2 min | > 5s for 5m |
| API Availability | ≥ 99.9% | 30 days | 43.2 min | < 99.9% for 15m |
| SSE Delivery Latency | < 2 seconds | 30 days | 43.2 min | > 2s for 5m |
| Sweep Completion | < 60 seconds | 30 days | 43.2 min | > 60s for 15m |
| Notification Delivery (email) | ≥ 99.5% | 30 days | 3.6 hrs | < 99.5% for 1h |
| Notification Delivery (SMS/WhatsApp) | ≥ 99% | 30 days | 7.2 hrs | < 99% for 1h |
| Queue Depth (per tenant) | < 1000 pending | Continuous | N/A | > 1000 for 10m |
| DLQ Aging (per tenant) | < 1 hour | Continuous | N/A | > 1h for 30m |

---

## 3. Error Budget Policy

- **Burn rate 1x** (normal): Page on-call if budget exhaustion projected in < 6h
- **Burn rate 2x**: Page on-call immediately
- **Burn rate 10x**: Page on-call + escalate to engineering lead
- **Budget exhausted**: Freeze non-critical deployments; focus on reliability

---

## 4. Monitoring Implementation

### 4.1 Metrics Endpoint (`/api/v1/metrics`)

Expose via `TRACKSPHERE_EXPOSE_METRICS=true`. Metrics prefixed `tracksphere_`.

Key metrics to emit:
```prometheus
# Ingestion
tracksphere_ingestion_duration_seconds_bucket{le="..."} 
tracksphere_webhook_received_total{carrier="...", status="ok|error"}

# API
tracksphere_http_requests_total{method="...", path="...", status="..."}
tracksphere_http_request_duration_seconds_bucket{method="...", path="...", le="..."}

# SSE
tracksphere_sse_subscribers_active{tenant="..."}
tracksphere_sse_delivery_duration_seconds_bucket{le="..."}
tracksphere_sse_events_published_total{type="...", tenant="..."}

# Queue
tracksphere_jobs_pending{tenant="...", kind="..."}
tracksphere_jobs_running{tenant="...", kind="..."}
tracksphere_jobs_dead{tenant="...", kind="..."}
tracksphere_job_duration_seconds_bucket{kind="...", le="..."}
tracksphere_sweep_duration_seconds_bucket{le="..."}
tracksphere_sweep_tenants_processed_total
tracksphere_sweep_alerts_raised_total{severity="..."}
tracksphere_sweep_alerts_cleared_total

# Notifications
tracksphere_notifications_attempted_total{channel="...", tenant="..."}
tracksphere_notifications_sent_total{channel="...", tenant="..."}
tracksphere_notifications_failed_total{channel="...", tenant="...", error="..."}
tracksphere_interrupts_sent_total{tenant="...", severity="..."}
tracksphere_interrupts_queued_total{tenant="...", reason="..."}
tracksphere_digests_flushed_total{tenant="..."}

# Database
tracksphere_db_pool_connections_active
tracksphere_db_pool_connections_idle
tracksphere_db_pool_connections_max
```

### 4.2 Alerting Rules (PrometheusRule)

```yaml
groups:
- name: tracksphere-slos
  rules:
  # Ingestion freshness
  - alert: IngestionFreshnessSLOBreach
    expr: |
      histogram_quantile(0.99, rate(tracksphere_ingestion_duration_seconds_bucket[5m])) > 5
    for: 5m
    labels:
      severity: critical
      slo: ingestion_freshness
    annotations:
      summary: "Ingestion p99 > 5s for 5m"
      description: "Carrier webhook to API visibility latency exceeded SLO"

  # API availability
  - alert: APIAvailabilitySLOBreach
    expr: |
      (sum(rate(tracksphere_http_requests_total{status=~"5.."}[5m])) 
       / sum(rate(tracksphere_http_requests_total[5m]))) > 0.001
    for: 15m
    labels:
      severity: critical
      slo: api_availability
    annotations:
      summary: "API 5xx rate > 0.1% for 15m"
      description: "API availability below 99.9% SLO"

  # SSE delivery
  - alert: SSEDeliveryLatencySLOBreach
    expr: |
      histogram_quantile(0.99, rate(tracksphere_sse_delivery_duration_seconds_bucket[5m])) > 2
    for: 5m
    labels:
      severity: warning
      slo: sse_delivery
    annotations:
      summary: "SSE delivery p99 > 2s for 5m"

  # Sweep completion
  - alert: SweepDurationSLOBreach
    expr: |
      histogram_quantile(0.99, rate(tracksphere_sweep_duration_seconds_bucket[5m])) > 60
    for: 15m
    labels:
      severity: warning
      slo: sweep_completion
    annotations:
      summary: "Sweep p99 > 60s for 15m"

  # Per-tenant queue depth
  - alert: TenantQueueDepthHigh
    expr: |
      tracksphere_jobs_pending > 1000
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Tenant {{ $labels.tenant }} queue depth > 1000 for 10m"

  # Per-tenant DLQ aging
  - alert: TenantDLQAging
    expr: |
      tracksphere_job_age_seconds{status="dead"} > 3600
    for: 30m
    labels:
      severity: critical
    annotations:
      summary: "Tenant {{ $labels.tenant }} has dead job older than 1h"

  # Database pool saturation
  - alert: DBPoolSaturation
    expr: |
      tracksphere_db_pool_connections_active / tracksphere_db_pool_connections_max > 0.8
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Database connection pool > 80% utilized"

  # Notification delivery rates
  - alert: EmailDeliveryRateLow
    expr: |
      (sum(rate(tracksphere_notifications_sent_total{channel="email"}[5m]))
       / sum(rate(tracksphere_notifications_attempted_total{channel="email"}[5m]))) < 0.995
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "Email delivery rate < 99.5% for 1h"

  - alert: SMSWhatsAppDeliveryRateLow
    expr: |
      (sum(rate(tracksphere_notifications_sent_total{channel=~"sms|whatsapp"}[5m]))
       / sum(rate(tracksphere_notifications_attempted_total{channel=~"sms|whatsapp"}[5m]))) < 0.99
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "SMS/WhatsApp delivery rate < 99% for 1h"
```

---

## 5. Backup & Restore (pgBackRest)

### 5.1 Configuration

```ini
# /etc/pgbackrest/pgbackrest.conf
[global]
repo1-type=s3
repo1-s3-bucket=tracksphere-backups
repo1-s3-region=us-east-1
repo1-s3-endpoint=s3.amazonaws.com
repo1-retention-full=30
repo1-retention-diff=7
repo1-retention-archive=30
compress-type=lz4
compress-level=3
process-max=4
log-level-console=info
log-level-file=debug

[tracksphere]
pg1-path=/var/lib/postgresql/data
pg1-port=5432
pg1-user=tracksphere_backup
```

### 5.2 Backup Schedule

| Backup Type | Schedule | Retention |
|-------------|----------|-----------|
| Full | Daily 02:00 UTC | 30 days |
| Differential | Every 6 hours | 7 days |
| WAL Archive | Continuous | 30 days |

### 5.3 Restore Testing (Quarterly)

**Runbook:** `docs/RUNBOOK_RESTORE.md`

1. Provision clean PostgreSQL 16 instance
2. `pgbackrest --stanza=tracksphere --type=full restore`
3. Verify schema integrity: `psql -c "SELECT count(*) FROM tenants;"`
4. Verify data integrity: `psql -c "SELECT count(*) FROM shipments;"`
5. Run RLS tests: `psql -U tracksphere_app -f scripts/rls_test.sql`
6. Run e2e tests: `pwsh scripts/e2e.ps1`
7. Document RTO (target < 4h) and RPO (target < 1h)

### 5.4 Restore Verification Checklist

- [ ] All 23 tenant tables have RLS policies enforced
- [ ] `shipment_current` read model matches `shipments` + `shipment_events`
- [ ] `notification_consent` audit trail intact
- [ ] `alert_rules` seeded correctly per tenant
- [ ] API starts and serves `/health` 200
- [ ] Worker processes jobs without error
- [ ] SSE hub accepts subscribers

---

## 6. Dashboard (Grafana)

### 6.1 SLO Dashboard Panels

1. **Error Budget Remaining** — Burn down chart per SLO
2. **SLI Trends** — 30d rolling p99 for each SLI
3. **Per-Tenant Health** — Queue depth, DLQ age, active shipments, critical alerts
4. **Ingestion Pipeline** — Webhook rate, processing latency, error rate
5. **Notification Funnel** — Attempted → Sent → Delivered per channel
6. **Database Health** — Pool utilization, replication lag, vacuum status

### 6.2 Alert Routing

| Alert | Route | Escalation |
|-------|-------|------------|
| Critical SLO breach | PagerDuty → On-call | +15min → Engineering lead |
| Warning SLO breach | Slack #alerts | +1h → On-call |
| Tenant queue/DLQ | Slack #tenant-ops | +30min → On-call |
| DB pool saturation | PagerDuty → On-call | Immediate |

---

## 7. Review Cadence

| Review | Frequency | Participants | Output |
|--------|-----------|--------------|--------|
| SLO compliance | Monthly | Engineering + Product | SLO report, budget burn analysis |
| Alert tuning | Monthly | On-call rotation | Alert noise reduction, threshold adjustments |
| Backup restore test | Quarterly | Platform + Engineering | Restore report, RTO/RPO measurement |
| Capacity planning | Quarterly | Engineering | Scaling trigger evaluation |

---

## 8. Runbook Links

- `docs/RUNBOOK_INGESTION_LATENCY.md`
- `docs/RUNBOOK_API_AVAILABILITY.md`
- `docs/RUNBOOK_SSE_DELIVERY.md`
- `docs/RUNBOOK_SWEEP_STALLED.md`
- `docs/RUNBOOK_QUEUE_BACKLOG.md`
- `docs/RUNBOOK_DLQ_GROWING.md`
- `docs/RUNBOOK_DB_POOL_EXHAUSTION.md`
- `docs/RUNBOOK_NOTIFICATION_DELIVERY.md`
- `docs/RUNBOOK_RESTORE.md`
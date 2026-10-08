# TrackSphere Disaster Recovery Runbook

**RTO Target:** < 4 hours
**RPO Target:** < 1 hour
**Last Tested:** [DATE]
**Next Test:** [DATE + 90 days]

## 0. Backup automation (live, not aspirational)

- `backup` compose service (`deploy/docker-compose.yml` + `deploy/backup.sh`)
  dumps nightly to the `pgbackups` volume; keep 7 by default (`BACKUP_KEEP`).
  **Copy the volume off-host** (S3/R2/rsync) — a dump on the same disk as the
  DB is not a backup.
- Every dump writes `backup_runs`; `GET /api/v1/statusz` reports
  `.backup.lastSuccess/.ageHours` and flips to `degraded` past 30h without a
  proven backup. Alert on `status != operational` and on `backup_runs` gaps.
- Pre-migration manual dumps still required until pgBackRest PITR ships
  (see `docs/pgbackrest-setup.md`): record the SHA in the PR (PR template).

### Drill log (append each rehearsal — no drill, no "production-ready")

| Date | Tester | Restore source | RTO | RPO | RLS proof | Gaps |
|---|---|---|---|---|---|---|
| _none yet_ | | | | | | _this row is the gate_ |

---

## 1. Incident Classification

| Tier | Scenario | Response |
|------|----------|----------|
| **SEV-1** | Region outage, data corruption, ransomware | Full DR failover |
| **SEV-2** | PostgreSQL instance failure, disk failure | Restore from backup |
| **SEV-3** | Single table corruption, bad migration | Point-in-time recovery |

---

## 2. SEV-1: Region Failover (Multi-Region - Wave 3)

> **NOT YET IMPLEMENTED** — Documented for Wave 3 readiness

### Prerequisites (Wave 3)
- [ ] Secondary region PostgreSQL with streaming replication
- [ ] DNS failover (Route53/Cloudflare) with health checks
- [ ] pgBackRest repo replicated to secondary region
- [ ] Tested failover runbook

### Failover Procedure
1. Declare SEV-1 in incident channel
2. Update DNS to secondary region (TTL 60s)
3. Promote secondary PostgreSQL: `pg_ctl promote`
4. Update `TRACKSPHERE_DATABASE_URL` in all services
5. Restart API/worker/web in secondary region
6. Verify `/health` and run smoke tests
7. Communicate to customers

---

## 3. SEV-2: Single Instance Restore

### 3.1 Detection
- Alert: `DBInstanceDown` or `BackupMissing`
- Manual: API returns 500, `pg_isready` fails

### 3.2 Procedure

```bash
# 1. Stop application traffic
docker compose stop api worker web

# 2. Provision new PostgreSQL instance (if needed)
#    - Same version (16)
#    - Same extensions (pg_cron, pg_stat_statements, uuid-ossp)
#    - Mount clean volume

# 3. Restore from pgBackRest
pgbackrest --stanza=tracksphere \
  --type=full \
  --target-action=promote \
  --delta \
  restore

# 4. Start PostgreSQL
docker compose start postgres

# 5. Wait for healthy
until pg_isready -h postgres -U tracksphere; do sleep 2; done

# 6. Verify data integrity
psql -U tracksphere -d tracksphere -c "SELECT count(*) FROM tenants;"
psql -U tracksphere -d tracksphere -c "SELECT count(*) FROM shipments;"

# 7. Run RLS tests (CRITICAL - proves multi-tenancy intact)
docker exec -i tracksphere-pg psql -U tracksphere_app -d tracksphere \
  -v ON_ERROR_STOP=1 -f scripts/rls_test.sql

# 8. Run schema tests
docker exec -i tracksphere-pg psql -U tracksphere -d tracksphere \
  -v ON_ERROR_STOP=1 -f scripts/schema_test.sql

# 9. Start application
docker compose start api worker web

# 10. Verify application health
curl -f http://localhost:8080/api/v1/health
curl -f http://localhost:3000

# 11. Run e2e tests
pwsh scripts/e2e.ps1
```

### 3.3 Post-Restore Verification Checklist

| Check | Command | Expected |
|-------|---------|----------|
| Tenant isolation | `rls_test.sql` | ALL TESTS PASSED |
| Schema integrity | `schema_test.sql` | No errors |
| Read model fresh | `SELECT max(updated_at) FROM shipment_current;` | < 15 min ago |
| Notification consent | `SELECT count(*) FROM notification_consent WHERE action='delivered';` | > 0 |
| Alert rules seeded | `SELECT tenant_id, count(*) FROM alert_rules GROUP BY tenant_id;` | 5+ per tenant |
| Webhook inbox intact | `SELECT count(*) FROM webhook_inbox;` | Matches pre-restore |
| API serves traffic | `curl /api/v1/health` | 200 OK |
| SSE works | Open portal, verify live badge | "Live · last update HH:MM:SS" |

---

## 4. SEV-3: Point-in-Time Recovery (PITR)

### 4.1 When to Use
- Bad migration deployed
- Accidental `DELETE`/`UPDATE` without `WHERE`
- Application bug corrupted data for < 1 hour

### 4.2 Procedure

```bash
# 1. Identify recovery target timestamp
#    - Use application logs, audit trail, or notification_consent
#    - Example: "2026-10-03 14:22:00+00" (just before bad migration)

# 2. Stop application
docker compose stop api worker web

# 3. PITR restore
pgbackrest --stanza=tracksphere \
  --type=time \
  --target="2026-10-03 14:22:00+00" \
  --target-action=promote \
  restore

# 4. Start PostgreSQL
docker compose start postgres

# 5. Verify up to target time
psql -U tracksphere -d tracksphere -c "
  SELECT max(created_at) FROM shipment_audit;
  SELECT max(created_at) FROM notification_consent;
"

# 6. Run RLS + schema tests
# (same as SEV-2 steps 7-8)

# 7. Start application
docker compose start api worker web

# 8. Verify no data loss after target
#    - Check shipments created after target are gone (expected)
#    - Check shipments before target intact
```

---

## 5. Communication Plan

| Audience | Channel | Timing | Template |
|----------|---------|--------|----------|
| Internal (Engineering) | Slack #incidents | Immediate | "SEV-X: [Title] - [Impact] - IC: @oncall" |
| Internal (Leadership) | Slack #leadership | +15 min | "SEV-X declared. Impact: [X tenants affected]. ETA: [time]" |
| Customers (affected) | Email + In-app banner | +30 min | "We're experiencing degraded service. ETA for resolution: [time]" |
| Customers (all) | Status page | +1 hour | "Incident resolved. Root cause: [summary]. Prevention: [actions]" |
| Postmortem | GitHub Issue + Confluence | +48 hours | Blameless postmortem template |

---

## 6. Postmortem Template

```markdown
# Incident #[NUMBER] - [Title]

**Date:** YYYY-MM-DD
**Duration:** Xh Ym
**Severity:** SEV-[1/2/3]
**Status:** Resolved

## Summary
[2-3 sentences: what happened, impact, resolution]

## Timeline
| Time (UTC) | Event |
|------------|-------|
| HH:MM | Detection (alert/manual) |
| HH:MM | IC acknowledged |
| HH:MM | Root cause identified |
| HH:MM | Mitigation applied |
| HH:MM | Full resolution |

## Root Cause
[5 Whys analysis]

## Impact
- Tenants affected: [count/names]
- Shipments delayed: [count]
- Notifications lost: [count]
- Revenue impact: [estimate]

## Resolution
[What fixed it]

## Action Items
| # | Action | Owner | Due | Ticket |
|---|--------|-------|-----|--------|
| 1 | [Preventive measure] | @user | YYYY-MM-DD | GH-XXX |
| 2 | [Detection improvement] | @user | YYYY-MM-DD | GH-XXX |

## Lessons Learned
[What we learned about system, process, team]
```

---

## 7. Quarterly DR Test Checklist

**Date:** ___________  
**Tester:** ___________  
**Duration:** ___________

### Pre-Test
- [ ] Notify team of scheduled test
- [ ] Confirm backup repo accessible
- [ ] Verify pgBackRest config current
- [ ] Document current RPO (last backup time)

### Test Execution
- [ ] Provision clean test environment
- [ ] Restore latest full backup
- [ ] Run verification script (`verify-restore.sh`)
- [ ] Run RLS tests as `tracksphere_app`
- [ ] Run schema tests as owner
- [ ] Start API/worker/web
- [ ] Run e2e test suite (`pwsh scripts/e2e.ps1`)
- [ ] Measure RTO (stop to healthy)

### Post-Test
- [ ] Record RTO: _______ (target < 4h)
- [ ] Record RPO: _______ (target < 1h)
- [ ] Document any failures/gaps
- [ ] Update runbook with findings
- [ ] Share results with team

### Sign-Off
- [ ] Platform Lead: ___________
- [ ] Engineering Lead: ___________
- [ ] Next test scheduled: ___________
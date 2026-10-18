-- migrate:no-transaction
-- 000033_phase1_query_indexes.sql — Phase 1 query ceilings: tenant-led
-- indexes for the hot list paths. CONCURRENTLY so the migration never
-- blocks writes; therefore no-transaction (Postgres refuses CONCURRENTLY
-- inside a transaction block). All IF NOT EXISTS → re-runnable.
CREATE INDEX CONCURRENTLY IF NOT EXISTS shipments_tenant_status_idx
    ON shipments (tenant_id, status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS alerts_tenant_status_due_idx
    ON alerts (tenant_id, status, due_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_interrupts_tenant_hour_idx
    ON notification_interrupts (tenant_id, created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_tenant_status_run_idx
    ON jobs (tenant_id, status, run_at);

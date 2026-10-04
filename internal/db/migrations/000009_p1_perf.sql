-- 000009_p1_perf.sql — P1 scale hardening: search, RLS efficiency, safety.
--
-- * pg_trgm GIN for `lower(tracking_number) LIKE %..%` + reference search
--   (sequential scan today).
-- * tenant_id-leading indexes so RLS-filtered queries use index scans.
-- * tenant_dashboard as security_invoker (PG15+): view runs with caller
--   privileges so underlying RLS always applies (defense in depth; today it
--   relies on the tenant_id=$1 parameter).
-- * statement_timeout on the runtime role: 5s guard against runaway queries.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS shipments_search_trgm_idx
    ON shipments USING gin (lower(tracking_number) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS shipments_reference_trgm_idx
    ON shipments USING gin (lower(reference) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS shipment_events_tenant_shipment_idx
    ON shipment_events (tenant_id, shipment_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS notifications_tenant_created_idx
    ON notifications (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS jobs_status_run_idx
    ON jobs (status, run_at, id);

-- PG15+: run the dashboard view with caller privileges.
DO $$
BEGIN
    IF current_setting('server_version_num')::int >= 150000 THEN
        EXECUTE 'ALTER VIEW tenant_dashboard SET (security_invoker = true)';
    END IF;
END $$;

-- Guard rail: kill runaway queries on the restricted runtime role.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'tracksphere_app') THEN
        EXECUTE 'ALTER ROLE tracksphere_app SET statement_timeout = ''5s''';
    END IF;
END $$;

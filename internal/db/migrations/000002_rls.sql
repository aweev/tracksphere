-- 000002_rls.sql — multi-tenant isolation at the database layer.
--
-- Design (docs/adr/0004):
--  * FORCE ROW LEVEL SECURITY so policies apply even to the table owner.
--    Every tenant-scoped statement must run inside db.WithTenant(), which pins
--    app.tenant_id transaction-locally. A missing/wrong context fails CLOSED
--    (zero rows) instead of leaking data.
--  * The app connects as a NON-superuser role (000004_app_role.sql): a
--    superuser bypasses RLS entirely, which would silently disable every
--    policy below. Migrations may run as the owner.
--  * Public tracking portal: a separate transaction-local flag
--    app.public_access='on' (set only by the portal repository methods)
--    unlocks is_public=true rows WITHOUT a tenant. Tenant-context queries
--    never set it, so tenants cannot see each other's public shipments.
--  * app.system='on' is the webhook-resolver-only branch (see 000002 below).
--  * System tables (jobs, webhook_inbox, schema_migrations) and auth tables
--    (users, sessions) have no RLS: they are accessed exclusively by unique
--    keys (token hash, email, job id) and never by tenant range scans.

CREATE FUNCTION current_tenant() RETURNS uuid
    LANGUAGE sql STABLE AS
$$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

-- ── tenants ────────────────────────────────────────────────────────────
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
-- Normal operation: a tenant reads only its own row. The system branch exists
-- ONLY for bootstrap writers (register/seed) — the new row's id cannot equal
-- current_tenant() before it exists. app.system is set transaction-locally by
-- db.SetSystem; HTTP clients cannot set server GUCs.
CREATE POLICY tenants_isolation ON tenants
    USING (id = current_tenant() OR current_setting('app.system', true) = 'on')
    WITH CHECK (id = current_tenant() OR current_setting('app.system', true) = 'on');

-- ── shipments ──────────────────────────────────────────────────────────
-- Read branches:
--   1. tenant_id = current_tenant()          → authenticated tenant queries
--   2. app.public_access='on' AND is_public  → public tracking portal ONLY,
--      set transaction-locally by the portal repository methods. Queries
--      that carry a tenant context never set this flag, so tenant A can
--      never read tenant B's shipments through this branch.
--   3. app.system='on'                       → carrier-webhook resolver only
--      (lookup by tracking number before any tenant is known; must also
--      resolve private shipments). db.SetSystem sets it; the resolved tenant
--      is pinned before any write — WITH CHECK rejects un-pinned writes.
ALTER TABLE shipments ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipments FORCE ROW LEVEL SECURITY;
CREATE POLICY shipments_isolation ON shipments
    USING (
        tenant_id = current_tenant()
        OR (current_setting('app.public_access', true) = 'on' AND is_public = true)
        OR current_setting('app.system', true) = 'on'
    )
    WITH CHECK (tenant_id = current_tenant());

-- ── shipment_events ────────────────────────────────────────────────────
-- The portal branch re-checks parent shipment visibility through a subquery
-- which is itself subject to the shipments policy, so a private shipment's
-- timeline stays hidden even with public_access on.
ALTER TABLE shipment_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_events FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_events_isolation ON shipment_events
    USING (
        tenant_id = current_tenant()
        OR (current_setting('app.public_access', true) = 'on'
            AND EXISTS (
                SELECT 1 FROM shipments s
                WHERE s.id = shipment_id AND s.is_public = true
            ))
    )
    WITH CHECK (tenant_id = current_tenant());

-- ── alerts ─────────────────────────────────────────────────────────────
ALTER TABLE alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE alerts FORCE ROW LEVEL SECURITY;
CREATE POLICY alerts_isolation ON alerts
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- ── notifications ──────────────────────────────────────────────────────
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY notifications_isolation ON notifications
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

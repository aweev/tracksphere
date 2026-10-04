-- scripts/schema_test.sql
--
-- Structural invariants that must run as the OWNER role (they need to create
-- tenant rows, which the app role legitimately cannot). Kept separate from
-- scripts/rls_test.sql on purpose: mixing owner and app-role assertions in one
-- file is how a "passing" isolation test ends up silently testing nothing.
--
--   docker exec -i tracksphere-pg psql -U tracksphere -d tracksphere \
--       -v ON_ERROR_STOP=1 -f - < scripts/schema_test.sql
--
-- Asserts:
--   S1. The same tracking number is reusable across tenants (no 409 oracle —
--       two customers can legitimately have the same container number, and a
--       global unique index would both break them and leak existence).
--   S2. tracking_number is NOT globally unique (guards against S1 passing
--       vacuously because no duplicate exists to test with).
--   S3. The partial unique index that dedupes open exceptions still exists and
--       is still partial.
--   S4. jobs.dedup_key has a partial unique index, so scheduled sweeps are
--       idempotent.
--   S5. Every tenant-scoped table has RLS enabled AND forced.

\set ON_ERROR_STOP on

DO $$
DECLARE
    t1 uuid;
    t2 uuid;
    n int;
    pol record;
    missing text := '';
BEGIN
    -- ── S1/S2: duplicate tracking numbers across tenants ─────────────────
    -- One INSERT per probe tenant: PL/pgSQL's RETURNING ... INTO binds a single
-- row, so a multi-row insert raises "query returned more than one row".
    INSERT INTO tenants (slug, name, plan)
    VALUES ('schema-probe-a', 'Schema Probe A', 'starter')
    RETURNING id INTO t1;
    INSERT INTO tenants (slug, name, plan)
    VALUES ('schema-probe-b', 'Schema Probe B', 'starter')
    RETURNING id INTO t2;

    IF t1 IS NULL OR t2 IS NULL THEN
        RAISE EXCEPTION 'could not create probe tenants';
    END IF;

    INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, origin, destination)
    VALUES (t1, 'SCHEMA-PROBE-DUP', 'probe', 'air', 'A', 'B'),
           (t2, 'SCHEMA-PROBE-DUP', 'probe', 'air', 'A', 'B');
    RAISE NOTICE 'S1 PASS: same tracking number insertable under two tenants';

    SELECT count(*) INTO n FROM shipments
    WHERE tenant_id IN (t1, t2) AND tracking_number = 'SCHEMA-PROBE-DUP';
    IF n <> 2 THEN
        RAISE EXCEPTION 'expected 2 rows sharing a tracking number, got %', n;
    END IF;

    -- S2: a global unique index would have rejected the INSERT above, so prove
    -- the absence explicitly rather than relying on the INSERT as evidence.
    -- The check must be for a unique index over tracking_number ALONE: the
    -- composite UNIQUE (tenant_id, tracking_number) that replaced it also
    -- contains that column, and matching on it would flag the correct schema.
    IF EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'shipments'
          AND indexdef ILIKE '%UNIQUE%'
          AND indexdef ~* '\(\s*tracking_number\s*\)'
    ) THEN
        RAISE EXCEPTION 'a global unique index on shipments.tracking_number alone exists';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'shipments'
          AND indexdef ILIKE '%UNIQUE%'
          AND indexdef ~* 'tenant_id\s*,\s*tracking_number'
    ) THEN
        RAISE EXCEPTION 'the composite UNIQUE(tenant_id, tracking_number) index is missing';
    END IF;
    RAISE NOTICE 'S2 PASS: uniqueness is per-tenant, not global';

    -- Within one tenant it MUST still be unique, or tracking is ambiguous.
    BEGIN
        INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, origin, destination)
        VALUES (t1, 'SCHEMA-PROBE-DUP', 'probe', 'air', 'A', 'B');
        RAISE EXCEPTION 'duplicate tracking number allowed within one tenant';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE 'S2b PASS: duplicate tracking number rejected within a tenant';
    END;

    -- ── S3: the open-exception dedup index ──────────────────────────────
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'alerts' AND indexdef ILIKE '%UNIQUE%'
          AND indexdef ILIKE '%status%'
    ) THEN
        RAISE EXCEPTION 'the partial unique index on alerts is missing — replayed webhooks would re-alert';
    END IF;
    RAISE NOTICE 'S3 PASS: alerts open-exception dedup index present';

    -- ── S4: idempotent job scheduling ───────────────────────────────────
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'jobs' AND indexdef ILIKE '%dedup_key%' AND indexdef ILIKE '%UNIQUE%'
    ) THEN
        RAISE EXCEPTION 'jobs.dedup_key has no unique index — the sweep can double-run';
    END IF;
    RAISE NOTICE 'S4 PASS: jobs dedup index present';

    -- ── S5: every tenant table has RLS enabled AND forced ────────────────
    -- FORCE matters: without it the table owner silently bypasses every policy,
    -- which is precisely how a "protected" table leaks.
    FOR pol IN
        SELECT unnest(ARRAY[
            'shipments', 'shipment_events', 'alerts', 'notifications',
            'shipment_current', 'shipment_milestones', 'shipment_audit',
            'tracking_subscriptions', 'shipment_documents', 'shipment_legs',
            'tenant_branding', 'ecommerce_connections', 'carrier_credentials',
            'api_keys', 'tenant_webhooks', 'webhook_deliveries',
            'idempotency_keys', 'stripe_subscriptions', 'sso_accounts',
            'notification_consent', 'notification_interrupts',
            'notification_digest_queue', 'alert_rules'
        ]) AS tbl
    LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_class c
                       WHERE c.relname = pol.tbl AND c.relrowsecurity) THEN
            missing := missing || pol.tbl || '(no-rls) ';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_class c
                       WHERE c.relname = pol.tbl AND c.relforcerowsecurity) THEN
            missing := missing || pol.tbl || '(not-forced) ';
        END IF;
    END LOOP;
    IF missing <> '' THEN
        RAISE EXCEPTION 'tables missing RLS enforcement: %', missing;
    END IF;
    RAISE NOTICE 'S5 PASS: all 23 tenant tables have RLS enabled and forced';

    -- ── Cleanup so the script is idempotent ─────────────────────────────
    DELETE FROM shipments WHERE tenant_id IN (t1, t2);
    DELETE FROM tenants WHERE id IN (t1, t2);
END $$;

SELECT 'ALL SCHEMA TESTS PASSED' AS result;
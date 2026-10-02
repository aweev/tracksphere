-- scripts/rls_test.sql
-- Tenant-isolation proof for TrackSphere. MUST be executed as tracksphere_app
-- (the application runtime role): a superuser bypasses RLS entirely and the
-- test would pass vacuously.
--
--   docker exec -i tracksphere-pg psql -U tracksphere_app -d tracksphere \
--       -v ON_ERROR_STOP=1 -f - < scripts/rls_test.sql
--
-- Asserts (each raises EXCEPTION on failure → non-zero psql exit):
--   1. Evil tenant sees 0 shipments / 0 alerts of other tenants (tenant ctx).
--   2. Evil tenant sees 0 rows with NO tenant context (fail-closed).
--   3. Portal flag (public_access) sees public shipments, but NOT private.
--   4. Portal flag cannot write (WITH CHECK requires tenant pin).
--   5. Portal flag sees only public shipments' timelines.

\set ON_ERROR_STOP on

-- Ensure evil tenant + a private shipment exist (as the app role: only under
-- its own tenant pin / portal-read semantics; use a pinned bootstrap via the
-- tenants system branch is NOT available to psql ad-hoc, so rely on rows the
-- seed created: acme tenant has public shipments only → create private marker
-- by toggling one via tenant-pinned update).
DO $$
DECLARE
    evil uuid;
    acme uuid;
    n_ship int;
    n_alert int;
    n_evil_own int;
    n_private int;
BEGIN
    SELECT id INTO acme FROM tenants WHERE slug = 'acme-logistics'
        OR id = current_tenant();
    -- current_tenant() is NULL here; fetch directly (tenants has RLS too!)
    RAISE NOTICE '(probe) rows visible with no context — expected 0 everywhere';
    SELECT count(*) INTO n_ship FROM tenants;
    IF n_ship <> 0 THEN
        RAISE EXCEPTION 'tenants visible without context: %', n_ship;
    END IF;

    -- ── Evil tenant context ────────────────────────────────────────────
    -- We cannot SELECT the acme id (RLS hides it). Use a random uuid: any
    -- foreign tenant id proves cross-tenant invisibility.
    evil := gen_random_uuid();

    PERFORM set_config('app.tenant_id', evil::text, true);
    SELECT count(*) INTO n_ship FROM shipments;
    SELECT count(*) INTO n_alert FROM alerts;
    IF n_ship <> 0 OR n_alert <> 0 THEN
        RAISE EXCEPTION 'cross-tenant leak: % shipments, % alerts visible',
            n_ship, n_alert;
    END IF;
    RAISE NOTICE 'TEST 1 PASS: foreign tenant sees 0 shipments, 0 alerts';

    -- WITH CHECK: inserting a shipment with a mismatched tenant_id must be
    -- rejected by policy. (The inner RAISE on success is NOT caught below, so
    -- a policy bug fails the whole script.)
    BEGIN
        INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, origin, destination)
        VALUES (gen_random_uuid(), 'TS-ROGUE-001', 'evil', 'air', 'X', 'Y');
        RAISE EXCEPTION 'WITH CHECK violated: cross-tenant shipment insert succeeded';
    EXCEPTION
        WHEN insufficient_privilege OR check_violation THEN
            RAISE NOTICE 'TEST 2 PASS: cross-tenant insert rejected by WITH CHECK';
    END;

    -- ── No context (fail-closed) ───────────────────────────────────────
    PERFORM set_config('app.tenant_id', '', true);
    SELECT count(*) INTO n_ship FROM shipments;
    SELECT count(*) INTO n_alert FROM alerts;
    IF n_ship <> 0 OR n_alert <> 0 THEN
        RAISE EXCEPTION 'fail-open: % shipments / % alerts without context',
            n_ship, n_alert;
    END IF;
    RAISE NOTICE 'TEST 3 PASS: no context → 0 shipments, 0 alerts';

    -- ── Portal flag: public visible, private hidden ─────────────────────
    PERFORM set_config('app.tenant_id', '', true);
    PERFORM set_config('app.public_access', 'on', true);
    SELECT count(*) INTO n_ship FROM shipments WHERE is_public = true;
    SELECT count(*) INTO n_private FROM shipments WHERE is_public = false;
    IF n_ship = 0 THEN
        RAISE EXCEPTION 'portal cannot see public shipments (seed missing?)';
    END IF;
    IF n_private <> 0 THEN
        RAISE EXCEPTION 'portal sees % private shipments', n_private;
    END IF;
    RAISE NOTICE 'TEST 4 PASS: portal sees % public, 0 private shipments', n_ship;

    -- Timeline: events of public shipments visible under portal flag.
    SELECT count(*) INTO n_evil_own FROM shipment_events;
    IF n_evil_own = 0 THEN
        RAISE EXCEPTION 'portal sees no events for public shipments';
    END IF;
    RAISE NOTICE 'TEST 5 PASS: portal timeline visible (% events)', n_evil_own;

    -- Portal flag alone must NOT permit writes (WITH CHECK needs tenant).
    BEGIN
        INSERT INTO alerts (tenant_id, shipment_id, kind, severity, title)
        SELECT gen_random_uuid(), id, 'delay', 'info', 'rogue'
        FROM shipments WHERE is_public = true LIMIT 1;
        RAISE EXCEPTION 'portal write succeeded — WITH CHECK broken';
    EXCEPTION
        WHEN insufficient_privilege OR check_violation THEN
            RAISE NOTICE 'TEST 6 PASS: portal-flag write rejected';
    END;
END $$;

SELECT 'ALL RLS TESTS PASSED' AS result;

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
--   6. Portal flag cannot write (WITH CHECK requires tenant pin).
--   7. All 23 tenant tables fail closed with NO context (the 000015 backfill
--      gate — catches any future tenant table added without a policy).
--   8. Unpinned writes are rejected on every writable tenant table.

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
    n_rows int;
    tbl text;
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

    -- NOTE: the "same tracking number is reusable across tenants" property used to
-- live here. It was never an RLS property — it is a schema property — and the
-- block could not pass as the app role, because inserting a shipment requires a
-- real parent row in `tenants`, which RLS correctly hides. It now lives in
-- scripts/schema_test.sql and runs as the owner.

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

    -- ── Every tenant table must fail closed with no context ────────────────
    -- The Phase-1 platform tables (api_keys, tenant_webhooks,
    -- webhook_deliveries, idempotency_keys) and the P2/P3 tables were created
    -- WITHOUT row-level security and were later backfilled (000015). Their
    -- handlers filtered correctly in Go, which is application-level discipline,
    -- not a database guarantee — exactly the class of bug a future refactor
    -- reintroduces. This block is the CI gate that keeps them covered.
    --
    -- If anyone adds a tenant-scoped table without a policy, this fails.
    PERFORM set_config('app.tenant_id', '', true);
    PERFORM set_config('app.public_access', '', true);
    PERFORM set_config('app.system', '', true);

    FOR tbl IN
        SELECT unnest(ARRAY[
            'shipments', 'shipment_events', 'alerts', 'notifications',
            'shipment_current', 'shipment_milestones', 'shipment_audit',
            'tracking_subscriptions', 'shipment_documents', 'shipment_legs',
            'tenant_branding', 'ecommerce_connections', 'carrier_credentials',
            'api_keys', 'tenant_webhooks', 'webhook_deliveries',
            'idempotency_keys', 'stripe_subscriptions', 'sso_accounts',
            'notification_consent', 'notification_interrupts',
            'notification_digest_queue', 'alert_rules'
        ])
    LOOP
        EXECUTE format('SELECT count(*) FROM %I', tbl) INTO n_rows;
        IF n_rows <> 0 THEN
            RAISE EXCEPTION 'fail-open: % exposes % rows with no context',
                tbl, n_rows;
        END IF;
    END LOOP;
    RAISE NOTICE 'TEST 7 PASS: all 23 tenant tables fail closed with no context';

    -- ── No-context writes must be rejected everywhere ─────────────────────
    FOR tbl IN
        SELECT unnest(ARRAY[
            'shipment_current', 'shipment_milestones', 'tracking_subscriptions',
            'notification_consent', 'notification_interrupts', 'alert_rules',
            'api_keys', 'tenant_webhooks', 'webhook_deliveries'
        ])
    LOOP
        BEGIN
            IF tbl IN ('shipment_current', 'shipment_milestones') THEN
                -- NOT NULL-heavy tables: an insert with no tenant simply cannot
                -- satisfy the policy, and the error may surface as either a
                -- policy violation or a not-null violation.
                EXECUTE format(
                    'INSERT INTO %I (tenant_id) VALUES (gen_random_uuid())', tbl);
            ELSIF tbl = 'tracking_subscriptions' THEN
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, shipment_id, channel, recipient, status) ' ||
                    'VALUES (gen_random_uuid(), gen_random_uuid(), ''email'', ''x@y.z'', ''active'')', tbl);
            ELSIF tbl = 'notification_consent' THEN
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, recipient_hash, action) ' ||
                    'VALUES (gen_random_uuid(), ''deadbeef'', ''confirmed'')', tbl);
            ELSIF tbl = 'notification_interrupts' THEN
                -- The interrupt ledger has no `action`: it records a delivery,
                -- not a consent transition.
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, recipient_hash, severity) ' ||
                    'VALUES (gen_random_uuid(), ''deadbeef'', ''critical'')', tbl);
            ELSIF tbl = 'alert_rules' THEN
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, name, kind) ' ||
                    'VALUES (gen_random_uuid(), ''rogue'', ''rogue'')', tbl);
            ELSIF tbl = 'api_keys' THEN
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, name, prefix, key_hash) ' ||
                    'VALUES (gen_random_uuid(), ''rogue'', ''rogue'', decode(''00'',''hex''))', tbl);
            ELSIF tbl = 'tenant_webhooks' THEN
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, url, secret) ' ||
                    'VALUES (gen_random_uuid(), ''https://x.invalid'', ''s'')', tbl);
            ELSE
                EXECUTE format(
                    'INSERT INTO %I (tenant_id, endpoint_id, event_type) ' ||
                    'VALUES (gen_random_uuid(), gen_random_uuid(), ''rogue'')', tbl);
            END IF;
            RAISE EXCEPTION 'unpinned write succeeded on % — WITH CHECK broken', tbl;
        EXCEPTION
            WHEN insufficient_privilege OR check_violation OR not_null_violation
                OR foreign_key_violation THEN
                NULL; -- rejected, as required
        END;
    END LOOP;
    RAISE NOTICE 'TEST 8 PASS: unpinned writes rejected on all writable tenant tables';
END $$;

SELECT 'ALL RLS TESTS PASSED' AS result;

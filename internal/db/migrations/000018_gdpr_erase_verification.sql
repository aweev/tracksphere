-- 000018_gdpr_erase_verification.sql — GDPR Art. 17 compliance verification
--
-- This migration adds a function that verifies the erase cascade works correctly
-- for a given tenant. It can be called manually or as part of an automated
-- test suite to ensure data deletion is complete.
--
-- Usage:
--   SELECT verify_tenant_erase('tenant-uuid-here');
-- Returns: JSON object with counts of remaining rows per table, or throws exception
-- if any tenant-scoped data remains after DELETE FROM tenants WHERE id = $1.

CREATE OR REPLACE FUNCTION verify_tenant_erase(p_tenant_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    v_tenant_exists boolean;
    v_table text;
    v_count bigint;
    v_result jsonb := '{}'::jsonb;
    v_failed_tables text[] := '{}';
    v_tenant_tables text[] := ARRAY[
        'shipments', 'shipment_events', 'alerts', 'notifications',
        'shipment_current', 'shipment_milestones', 'shipment_audit',
        'tracking_subscriptions', 'shipment_documents', 'shipment_legs',
        'tenant_branding', 'ecommerce_connections', 'carrier_credentials',
        'api_keys', 'tenant_webhooks', 'webhook_deliveries',
        'idempotency_keys', 'stripe_subscriptions', 'sso_accounts',
        'notification_consent', 'notification_interrupts',
        'notification_digest_queue', 'alert_rules',
        'shipment_polls', 'scheduler_leases', 'sse_subscriptions'
    ];
BEGIN
    -- First check if tenant exists
    SELECT EXISTS(SELECT 1 FROM tenants WHERE id = p_tenant_id) INTO v_tenant_exists;
    IF NOT v_tenant_exists THEN
        RAISE EXCEPTION 'Tenant % does not exist', p_tenant_id;
    END IF;

    -- Perform the erase
    DELETE FROM tenants WHERE id = p_tenant_id;

    -- Verify all tenant-scoped tables are empty for this tenant
    FOREACH v_table IN ARRAY v_tenant_tables LOOP
        EXECUTE format('SELECT count(*) FROM %I WHERE tenant_id = $1', v_table)
        INTO v_count USING p_tenant_id;
        
        IF v_count > 0 THEN
            v_failed_tables := v_failed_tables || v_table;
        END IF;
        
        v_result := v_result || jsonb_build_object(v_table, v_count);
    END LOOP;

    -- Check tables that don't have tenant_id but are scoped by FK to tenant tables
    -- (shipment_events, shipment_milestones, etc. are covered above via tenant_id)
    
    -- Webhook inbox has no tenant_id - it's intentional (audit trail outlives tenants)
    -- but we should verify no orphaned references remain
    EXECUTE format('SELECT count(*) FROM webhook_inbox wi
        WHERE NOT EXISTS (SELECT 1 FROM shipments s WHERE s.id = wi.shipment_id)'
    ) INTO v_count;
    v_result := v_result || jsonb_build_object('webhook_inbox_orphans', v_count);

    -- Return result
    v_result := v_result || jsonb_build_object('tenant_deleted', true);
    v_result := v_result || jsonb_build_object('failed_tables', v_failed_tables);
    
    IF array_length(v_failed_tables, 1) > 0 THEN
        RAISE EXCEPTION 'GDPR erase verification failed: % tables still have data for tenant %',
            array_length(v_failed_tables, 1), p_tenant_id
            USING DETAIL = v_result;
    END IF;

    RETURN v_result;
END;
$$;

-- Also add a function to list all tables that should be cascaded
CREATE OR REPLACE FUNCTION list_tenant_cascade_tables()
RETURNS TABLE(table_name text, has_tenant_id boolean, cascade_path text)
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    SELECT t.table_name::text,
           EXISTS(
               SELECT 1 FROM information_schema.columns c
               WHERE c.table_schema = 'public' 
               AND c.table_name = t.table_name 
               AND c.column_name = 'tenant_id'
           ),
           pg_get_constraintdef(c.oid) as cascade_path
    FROM (
        SELECT unnest(ARRAY[
            'shipments', 'shipment_events', 'alerts', 'notifications',
            'shipment_current', 'shipment_milestones', 'shipment_audit',
            'tracking_subscriptions', 'shipment_documents', 'shipment_legs',
            'tenant_branding', 'ecommerce_connections', 'carrier_credentials',
            'api_keys', 'tenant_webhooks', 'webhook_deliveries',
            'idempotency_keys', 'stripe_subscriptions', 'sso_accounts',
            'notification_consent', 'notification_interrupts',
            'notification_digest_queue', 'alert_rules',
            'shipment_polls', 'scheduler_leases', 'sse_subscriptions'
        ]) as table_name
    ) t
    LEFT JOIN pg_constraint c ON c.conrelid = t.table_name::regclass
        AND c.contype = 'f' 
        AND c.confrelid = 'tenants'::regclass
    ORDER BY t.table_name;
END;
$$;

GRANT EXECUTE ON FUNCTION verify_tenant_erase(uuid) TO tracksphere_app;
GRANT EXECUTE ON FUNCTION list_tenant_cascade_tables() TO tracksphere_app;
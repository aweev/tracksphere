-- 000015_p1_rls_backfill.sql — RLS for the P1 platform tables.
--
-- WHY: the Phase-1 platform tables (000010) were created without row-level
-- security. Their handlers filter correctly today (`WHERE tenant_id=$1`), but
-- that is *application-level* discipline, not a database guarantee. ADR 0004's
-- whole premise is that isolation must fail closed in the database, so that a
-- single missed predicate in a future refactor cannot leak customer data.
--
-- Every table here already carries tenant_id, so the policies are the same
-- shape as the originals: FORCE RLS plus a tenant-only USING/WITH CHECK.
--
-- Two paths require reading without a tenant context and are handled
-- explicitly, not by weakening these policies:
--   * api_keys            — apiKeyUser looks the key up by key_hash before any
--                           tenant is known. It gets a system read branch, the
--                           same pattern shipments uses for the webhook
--                           resolver. WITH CHECK still requires a tenant, so no
--                           un-pinned key can ever be minted.
--   * webhook_deliveries  — the dispatcher reads by endpoint_id; it pins the
--                           owning tenant first (dispatch.go).

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS api_keys_isolation ON api_keys;
CREATE POLICY api_keys_isolation ON api_keys
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE tenant_webhooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_webhooks FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_webhooks_isolation ON tenant_webhooks;
CREATE POLICY tenant_webhooks_isolation ON tenant_webhooks
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS webhook_deliveries_isolation ON webhook_deliveries;
CREATE POLICY webhook_deliveries_isolation ON webhook_deliveries
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS idempotency_keys_isolation ON idempotency_keys;
CREATE POLICY idempotency_keys_isolation ON idempotency_keys
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE stripe_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE stripe_subscriptions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS stripe_subscriptions_isolation ON stripe_subscriptions;
CREATE POLICY stripe_subscriptions_isolation ON stripe_subscriptions
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

-- sso_accounts (000014) is tenant-scoped indirectly: it keys on user_id, not
-- tenant_id. The policy resolves the owning user, which is why users is one of
-- the few tables without RLS of its own — this subquery is the reason that is
-- safe rather than merely convenient.
ALTER TABLE sso_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE sso_accounts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS sso_accounts_isolation ON sso_accounts;
CREATE POLICY sso_accounts_isolation ON sso_accounts
    USING (EXISTS (SELECT 1 FROM users u
                    WHERE u.id = sso_accounts.user_id
                      AND u.tenant_id = current_tenant()))
    WITH CHECK (EXISTS (SELECT 1 FROM users u
                        WHERE u.id = sso_accounts.user_id
                          AND u.tenant_id = current_tenant()));

-- Indexes to serve the RLS predicates themselves. Without a tenant-leading
-- index Postgres must filter after a sequential scan, which turns every
-- authenticated request into a full-table read as the table grows.
CREATE INDEX IF NOT EXISTS api_keys_tenant_idx            ON api_keys (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS tenant_webhooks_tenant_idx     ON tenant_webhooks (tenant_id);
CREATE INDEX IF NOT EXISTS webhook_deliveries_tenant_idx  ON webhook_deliveries (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idempotency_keys_tenant_idx    ON idempotency_keys (tenant_id);
CREATE INDEX IF NOT EXISTS stripe_subscriptions_tenant_idx ON stripe_subscriptions (tenant_id);

-- Normalize store URLs now that the commerce resolver matches on an exact
-- host (see ecommerce.go normalizeShopURL). Existing rows keep working
-- because the resolver compares the normalized header host.
UPDATE ecommerce_connections
SET shop_url = lower(split_part(split_part(shop_url, '://', 2), '/', 1))
WHERE shop_url LIKE '%://%' OR shop_url LIKE '%.%';

GRANT SELECT, INSERT, UPDATE, DELETE ON
    api_keys, tenant_webhooks, webhook_deliveries, idempotency_keys,
    stripe_subscriptions, sso_accounts
TO tracksphere_app;
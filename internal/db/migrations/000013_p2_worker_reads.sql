-- 000013_p2_worker_reads.sql — let the worker scheduler read polling
-- credentials across tenants.
--
-- The P2 scheduler runs with NO tenant (it fans out to all tenants). Tables
-- it must READ get the same system-branch pattern shipments uses
-- (docs/adr/0004): app.system='on' unlocks reads, WITH CHECK still requires
-- a tenant pin for writes. Set by server code only (tx-local GUC).

DROP POLICY IF EXISTS carrier_credentials_isolation ON carrier_credentials;
CREATE POLICY carrier_credentials_isolation ON carrier_credentials
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

DROP POLICY IF EXISTS ecommerce_connections_isolation ON ecommerce_connections;
CREATE POLICY ecommerce_connections_isolation ON ecommerce_connections
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

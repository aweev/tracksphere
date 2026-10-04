-- 000010_p1_platform.sql — P1 platform: API keys, outbound webhooks,
-- idempotency keys for bulk writes.
--
-- api_keys authenticate B2B callers (Authorization: Bearer tsk_...). Only the
-- SHA-256 hash is stored; prefix identifies the key in UIs/logs.
-- tenant_webhooks receive shipment.updated / alert.changed deliveries signed
-- with their per-endpoint secret (HMAC-SHA256, sha256=<hex>).
-- idempotency_keys make POST /shipments:batch safe to retry.

CREATE TABLE IF NOT EXISTS api_keys (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    name        text NOT NULL,
    prefix      text NOT NULL,
    key_hash    bytea NOT NULL UNIQUE,
    role        text NOT NULL DEFAULT 'member'
                CHECK (role IN ('owner', 'admin', 'member')),
    revoked_at  timestamptz,
    last_used_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS tenant_webhooks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url         text NOT NULL,
    secret      text NOT NULL,
    events      text[] NOT NULL DEFAULT '{shipment.updated,alert.changed}',
    active      boolean NOT NULL DEFAULT true,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tenant_webhooks_tenant_idx ON tenant_webhooks (tenant_id);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id          bigserial PRIMARY KEY,
    endpoint_id uuid NOT NULL REFERENCES tenant_webhooks(id) ON DELETE CASCADE,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_type  text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    status      text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts    integer NOT NULL DEFAULT 0,
    last_error  text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS webhook_deliveries_endpoint_idx
    ON webhook_deliveries (endpoint_id, created_at DESC);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key         text NOT NULL,
    status      integer NOT NULL DEFAULT 200,
    response    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);

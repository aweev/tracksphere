-- 000012_p2_growth.sql — P2 growth: carriers, branding, documents, legs,
-- commerce, Stripe, audit. All tenant tables carry tenant_id + FORCE RLS
-- (tenant-only, except tenant_branding which the public portal may read).

-- ── Carrier polling credentials (per-tenant, secrets sealed by the app) ──
CREATE TABLE IF NOT EXISTS carrier_credentials (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    carrier       text NOT NULL,
    base_url      text NOT NULL DEFAULT '',
    sealed_creds  text NOT NULL DEFAULT '',
    poll_interval_minutes integer NOT NULL DEFAULT 60,
    active        boolean NOT NULL DEFAULT true,
    last_polled_at timestamptz,
    last_error    text,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, carrier)
);
CREATE INDEX IF NOT EXISTS carrier_credentials_active_idx
    ON carrier_credentials (active, last_polled_at) WHERE active = true;

-- ── White-label branding (public-readable for the portal) ──
CREATE TABLE IF NOT EXISTS tenant_branding (
    tenant_id     uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    company_name  text NOT NULL DEFAULT '',
    primary_color text NOT NULL DEFAULT '#ff6b00',
    logo_url      text NOT NULL DEFAULT '',
    support_email text NOT NULL DEFAULT '',
    custom_domain text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ── Public tracking subscriptions ("notify me") ──
CREATE TABLE IF NOT EXISTS tracking_subscriptions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    channel     text NOT NULL CHECK (channel IN ('email', 'sms', 'whatsapp')),
    recipient   text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shipment_id, channel, recipient)
);
CREATE INDEX IF NOT EXISTS tracking_subscriptions_shipment_idx
    ON tracking_subscriptions (shipment_id);

-- ── Shipment documents (BOL, POD photos, customs paperwork) ──
CREATE TABLE IF NOT EXISTS shipment_documents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id  uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    filename     text NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   bigint NOT NULL DEFAULT 0,
    storage_key  text NOT NULL,
    uploaded_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shipment_documents_shipment_idx
    ON shipment_documents (tenant_id, shipment_id, created_at DESC);

-- ── Multi-leg journeys (ocean + drayage + customs in one record) ──
CREATE TABLE IF NOT EXISTS shipment_legs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    seq         integer NOT NULL DEFAULT 0,
    carrier     text NOT NULL DEFAULT '',
    mode        text NOT NULL DEFAULT 'ocean'
                CHECK (mode IN ('ocean', 'air', 'road', 'rail')),
    origin      text NOT NULL DEFAULT '',
    destination text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'booked',
    eta         timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shipment_id, seq)
);
CREATE INDEX IF NOT EXISTS shipment_legs_shipment_idx
    ON shipment_legs (shipment_id, seq);

-- ── E-commerce connections (Shopify / WooCommerce) ──
CREATE TABLE IF NOT EXISTS ecommerce_connections (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    provider     text NOT NULL CHECK (provider IN ('shopify', 'woocommerce')),
    shop_url     text NOT NULL DEFAULT '',
    sealed_token text NOT NULL DEFAULT '',
    webhook_secret text NOT NULL DEFAULT '',
    active       boolean NOT NULL DEFAULT true,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, provider, shop_url)
);

-- ── Stripe subscriptions (P2 billing; plans map to price IDs via env) ──
CREATE TABLE IF NOT EXISTS stripe_subscriptions (
    tenant_id        uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    stripe_customer  text NOT NULL DEFAULT '',
    stripe_sub       text NOT NULL DEFAULT '',
    plan             text NOT NULL DEFAULT 'starter',
    status           text NOT NULL DEFAULT 'incomplete',
    current_period_end timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- ── Shipment audit trail (who changed what, when) ──
CREATE TABLE IF NOT EXISTS shipment_audit (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    actor_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    actor_email text NOT NULL DEFAULT '',
    action      text NOT NULL,
    detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shipment_audit_shipment_idx
    ON shipment_audit (shipment_id, created_at DESC);

-- ── Provider used per notification (log|smtp|twilio|webhook) ──
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'log';

-- ── RLS ──
ALTER TABLE carrier_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE carrier_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY carrier_credentials_isolation ON carrier_credentials
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE tracking_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE tracking_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tracking_subscriptions_isolation ON tracking_subscriptions
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE shipment_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_documents_isolation ON shipment_documents
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE shipment_legs ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_legs FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_legs_isolation ON shipment_legs
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE ecommerce_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE ecommerce_connections FORCE ROW LEVEL SECURITY;
CREATE POLICY ecommerce_connections_isolation ON ecommerce_connections
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE shipment_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_audit_isolation ON shipment_audit
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- Branding: tenant RW + public portal read (no is_public flag on this table;
-- the portal joins it only for shipments it can already see).
ALTER TABLE tenant_branding ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_branding FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_branding_isolation ON tenant_branding
    USING (tenant_id = current_tenant()
        OR current_setting('app.public_access', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

-- Explicit grants for the runtime role (belt-and-braces alongside the
-- default privileges from 000004).
GRANT SELECT, INSERT, UPDATE, DELETE ON
    carrier_credentials, tracking_subscriptions, shipment_documents,
    shipment_legs, ecommerce_connections, stripe_subscriptions,
    shipment_audit, tenant_branding
    TO tracksphere_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO tracksphere_app;

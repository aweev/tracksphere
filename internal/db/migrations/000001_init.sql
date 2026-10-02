-- 000001_init.sql — core TrackSphere schema
-- Requires PostgreSQL 14+ (gen_random_uuid is core since PG13).
-- Convention: all tenant business tables carry tenant_id and are protected by
-- FORCE row-level security (see 000002_rls.sql). System tables (jobs, auth,
-- webhook inbox) are intentionally not RLS'd — documented in docs/adr/0004.

CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    slug       text NOT NULL UNIQUE,
    plan       text NOT NULL DEFAULT 'starter'
               CHECK (plan IN ('starter', 'growth', 'enterprise')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Auth table: intentionally global (login looks up by email before any tenant
-- context exists). Scoping to a tenant happens through sessions.user_id.
CREATE TABLE users (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email               text NOT NULL,
    name                text NOT NULL DEFAULT '',
    password_hash       text NOT NULL,          -- argon2id, encoded string
    role                text NOT NULL DEFAULT 'member'
                        CHECK (role IN ('owner', 'admin', 'member')),
    totp_secret         text,                   -- AES-256-GCM sealed, base64
    totp_enabled        boolean NOT NULL DEFAULT false,
    is_active           boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uniq ON users (lower(email));
CREATE INDEX users_tenant_idx ON users (tenant_id);

-- Opaque session tokens; only the SHA-256 hash is stored. mfa_pending marks a
-- half-authenticated session (password ok, TOTP not yet verified): such a
-- session is never attached to a cookie, only returned as a short-lived
-- challenge token in the login response body.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    mfa_pending  boolean NOT NULL DEFAULT false,
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    ip           text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE shipments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    tracking_number text NOT NULL UNIQUE,       -- globally unique: public portal lookup
    reference       text NOT NULL DEFAULT '',   -- customer PO / internal ref
    carrier         text NOT NULL,              -- maersk | dhl | fedex | ...
    mode            text NOT NULL CHECK (mode IN ('ocean', 'air', 'road', 'rail')),
    origin          text NOT NULL DEFAULT '',
    destination     text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'booked'
                    CHECK (status IN ('booked', 'in_transit', 'at_customs',
                                      'out_for_delivery', 'delivered',
                                      'exception', 'cancelled')),
    eta             timestamptz,
    shipped_at      timestamptz,
    delivered_at    timestamptz,
    is_public       boolean NOT NULL DEFAULT true,
    created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX shipments_tenant_created_idx ON shipments (tenant_id, created_at DESC);
CREATE INDEX shipments_tenant_status_idx ON shipments (tenant_id, status);
CREATE INDEX shipments_carrier_idx ON shipments (tenant_id, carrier);

-- Normalized scan/event timeline. dedup_key = "<carrier>:<carrier_event_id>"
-- makes webhook redelivery idempotent.
CREATE TABLE shipment_events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id  uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    carrier      text NOT NULL,
    code         text NOT NULL,      -- BOOKED | DEPARTED | ARRIVED | CUSTOMS_HOLD | ...
    description  text NOT NULL DEFAULT '',
    location     text NOT NULL DEFAULT '',
    lat          double precision,
    lng          double precision,
    occurred_at  timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    source       text NOT NULL DEFAULT 'webhook'
                 CHECK (source IN ('webhook', 'manual', 'system')),
    dedup_key    text NOT NULL,
    UNIQUE (tenant_id, dedup_key)
);
CREATE INDEX shipment_events_shipment_idx ON shipment_events (shipment_id, occurred_at DESC);

-- Ops alerts raised by the exception rules engine.
CREATE TABLE alerts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id  uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    kind         text NOT NULL
                 CHECK (kind IN ('delay', 'customs_hold', 'port_congestion',
                                 'sla_breach', 'eta_revised')),
    severity     text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    title        text NOT NULL,
    message      text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz,
    resolved_by  uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX alerts_tenant_status_idx ON alerts (tenant_id, status, created_at DESC);
-- At most one open alert of a given kind per shipment (prevents alert storms
-- when carriers replay webhooks).
CREATE UNIQUE INDEX alerts_open_uniq
    ON alerts (shipment_id, kind) WHERE status = 'open';

-- Transactional job queue: rows are inserted in the SAME transaction as the
-- business data (outbox pattern), claimed with FOR UPDATE SKIP LOCKED.
CREATE TABLE jobs (
    id           bigserial PRIMARY KEY,
    kind         text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'running', 'done', 'failed', 'dead')),
    run_at       timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    locked_by    text,
    locked_at    timestamptz,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_claim_idx ON jobs (run_at) WHERE status = 'pending';
CREATE INDEX jobs_kind_idx ON jobs (kind, status);

-- Immutable audit trail of every inbound carrier webhook (even rejected ones).
-- shipment_id is intentionally a plain uuid (NO foreign key): the inbox must
-- outlive shipments (audit/replay) and RI checks under FORCE RLS from a
-- non-owner role add fragile hidden reads. The application resolves the
-- shipment at processing time and stores its id here.
CREATE TABLE webhook_inbox (
    id               bigserial PRIMARY KEY,
    carrier          text NOT NULL,
    signature_valid  boolean NOT NULL,
    payload          jsonb NOT NULL,
    shipment_id      uuid,
    processed        boolean NOT NULL DEFAULT false,
    error            text,
    received_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_inbox_recent_idx ON webhook_inbox (received_at DESC);

-- Delivery log for outbound notifications (email/sms/whatsapp adapters write
-- here regardless of the underlying provider).
CREATE TABLE notifications (
    id           bigserial PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id  uuid REFERENCES shipments(id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('email', 'sms', 'whatsapp', 'system')),
    recipient    text NOT NULL,
    subject      text NOT NULL,
    body         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'sent' CHECK (status IN ('sent', 'failed', 'skipped')),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_tenant_idx ON notifications (tenant_id, created_at DESC);

-- updated_at maintenance
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tenants_updated_at BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER shipments_updated_at BEFORE UPDATE ON shipments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER jobs_updated_at BEFORE UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

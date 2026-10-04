-- 000008_auth_audit.sql — security audit trail + session management support.
--
-- auth_events is append-only (no RLS by design: login failures happen before
-- any tenant context exists). Reads are always scoped by tenant_id or user_id
-- from the authenticated session — never range-scanned.

CREATE TABLE IF NOT EXISTS auth_events (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid REFERENCES tenants(id) ON DELETE SET NULL,
    user_id     uuid REFERENCES users(id) ON DELETE SET NULL,
    email       text NOT NULL DEFAULT '',
    kind        text NOT NULL
                CHECK (kind IN ('register', 'login', 'login_failed',
                                'logout', 'mfa_challenge', 'mfa_verified', 'mfa_failed',
                                'password_change', 'session_revoke')),
    ip          text NOT NULL DEFAULT '',
    user_agent  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_events_tenant_idx ON auth_events (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS auth_events_user_idx ON auth_events (user_id, created_at DESC);

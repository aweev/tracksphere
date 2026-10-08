-- 000028_recovery_codes.sql — MFA recovery codes (P1-6).
--
-- Auth table, deliberately outside RLS like users/sessions: rows are resolved
-- by user_id + code hash, never by tenant scan. One-time use (used_at), 10
-- active codes per user max (enforced in the handler, not the schema).
CREATE TABLE IF NOT EXISTS mfa_recovery_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  bytea NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS mfa_recovery_codes_hash_uniq
    ON mfa_recovery_codes (code_hash);
CREATE INDEX IF NOT EXISTS mfa_recovery_codes_user_idx
    ON mfa_recovery_codes (user_id) WHERE used_at IS NULL;

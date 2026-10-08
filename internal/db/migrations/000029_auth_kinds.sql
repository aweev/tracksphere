-- 000029_auth_kinds.sql — admit audit kinds the code already emits (P1-6).
--
-- handleMFAEnroll audits mfa_enroll_denied/mfa_enrolled and recovery codes
-- audit mfa_recovery_issued/mfa_recovery_used, but the 000008 CHECK only
-- allows nine kinds — auditEvent is best-effort, so those rows were silently
-- dropped. Extend the constraint so the trail is complete.
ALTER TABLE auth_events DROP CONSTRAINT IF EXISTS auth_events_kind_check;
ALTER TABLE auth_events ADD CONSTRAINT auth_events_kind_check CHECK (kind IN (
    'register', 'login', 'login_failed',
    'logout', 'mfa_challenge', 'mfa_verified', 'mfa_failed',
    'mfa_enrolled', 'mfa_enroll_denied',
    'mfa_recovery_issued', 'mfa_recovery_used',
    'password_change', 'session_revoke'));

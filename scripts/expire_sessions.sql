-- scripts/expire_sessions.sql — run as owner after a secret leak or rotation.
-- Invalidates every session + MFA challenge so stolen cookies die immediately.
-- Users simply sign in again; no data loss.
-- Sessions table holds both full sessions and MFA challenges
-- (mfa_pending=true rows, 5-min TTL). One DELETE kills both.
DELETE FROM sessions;
SELECT 'ALL SESSIONS EXPIRED' AS result;

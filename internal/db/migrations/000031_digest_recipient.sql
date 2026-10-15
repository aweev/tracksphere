-- 000031_digest_recipient.sql — make the digest queue deliverable (P0 fix).
--
-- L1 audit finding: notification_digest_queue stored only recipient_hash, so
-- HandleFlushDigests called sender.Send(ctx,"email",<hex-hash>,...) — a hash
-- is not an address, and every digest was undeliverable. Grouping also
-- derived kind via split_part(subject,' ',1), which yields "[critical]", not
-- the alert kind.
--
-- This migration adds the two missing columns. RLS policy is unchanged
-- (tenant isolation still holds); new columns inherit the existing policy.
-- Old rows keep recipient NULL and are skipped by the flush (fail closed —
-- never send to a hash).
ALTER TABLE notification_digest_queue
    ADD COLUMN IF NOT EXISTS recipient text,
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'unknown';

CREATE INDEX IF NOT EXISTS notification_digest_flush_idx
    ON notification_digest_queue (tenant_id, flushed_at, created_at)
    WHERE flushed_at IS NULL;

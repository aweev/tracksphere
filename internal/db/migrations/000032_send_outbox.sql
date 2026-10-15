-- 000032_send_outbox.sql — exactly-once-leaning delivery ledger (P0 fix).
--
-- L1 audit finding: every send path did INSERT notifications + provider Send
-- inside ONE tenant transaction. A Send-OK + tx-abort (statement_timeout,
-- crash, SMTP hang) retried the job into a DUPLICATE send with a second
-- audit row — nothing deduplicated it. The fix is claim-in-tx (short, DB
-- only), Send-after-commit, mark-sent. The claim needs a server-generated
-- idempotency key, which is this migration:
--
--   dedupe_key — deterministic per logical notification (job nonce or body
--     hash + recipient). Partial unique index: a retry claims the same row
--     instead of inserting a second one.
--   sent_at — set only after the provider accepted the message. A claimed
--     but unsent row is re-owned by the retry; a sent row is never resent.
--
-- Also: shipments_needs_refresh_idx backs RefreshBatch's flag scan
-- (WHERE s.needs_refresh ORDER … LIMIT n); the existing updated_at-only
-- index on shipment_current cannot serve the filter.
ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS dedupe_key text,
    ADD COLUMN IF NOT EXISTS sent_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS notifications_dedupe_uidx
    ON notifications (tenant_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS shipments_needs_refresh_idx
    ON shipments (id)
    WHERE needs_refresh;

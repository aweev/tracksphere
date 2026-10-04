-- 000007_webhook_inbox_raw.sql — keep the exact bytes of every delivery.
--
-- WHY: the inbox insert cast the body straight to jsonb, so a non-JSON
-- delivery (XML, plain text, truncated body) failed the INSERT itself and
-- broke the "audit first" contract. New contract: raw_body always stores the
-- exact bytes (truncated to 1MiB by the API), payload holds parsed JSON or
-- '{}' with error set.

ALTER TABLE webhook_inbox
    ADD COLUMN IF NOT EXISTS raw_body text NOT NULL DEFAULT '';

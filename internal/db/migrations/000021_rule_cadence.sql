-- 000021_rule_cadence.sql — make per-rule evaluation intervals real.
--
-- alert_rules.every_minutes has existed since 000016 and is seeded per rule
-- (30/60/30/60/120), but nothing ever read it: every sweep rule ran on every
-- pass regardless of its configured cadence. last_evaluated_at records when a
-- rule last ran so the sweep can skip rules whose interval has not elapsed.
-- NULL means never evaluated; the sweep treats that as due immediately.
ALTER TABLE alert_rules
    ADD COLUMN IF NOT EXISTS last_evaluated_at timestamptz;

COMMENT ON COLUMN alert_rules.last_evaluated_at IS
    'Last pass that evaluated this rule. NULL = never run, always due. Updated in the same transaction as the evaluation, so a rolled-back pass does not advance it.';
-- 000034_user_theme.sql — user theme preference. Originally shipped as
-- 000022_user_theme (duplicate version number alongside 000022_alert_severity_rank);
-- renamed and made idempotent so databases that already applied the old
-- filename re-run this harmlessly instead of failing on the existing column.
ALTER TABLE users
ADD COLUMN IF NOT EXISTS theme text NOT NULL DEFAULT 'system'
CHECK (theme IN ('system', 'light', 'dark'));

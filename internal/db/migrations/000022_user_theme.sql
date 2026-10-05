-- 000022_user_theme.sql — user theme preference
ALTER TABLE users
ADD COLUMN theme text NOT NULL DEFAULT 'system'
CHECK (theme IN ('system', 'light', 'dark'));
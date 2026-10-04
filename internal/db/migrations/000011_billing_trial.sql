-- 000011_billing_trial.sql — make the register-page promise real.
--
-- The register page sells "14-day trial · no credit card · cancel anytime"
-- but tenants had no trial fields. New contract: every tenant gets
-- trial_ends_at (created + 14 days). Plan limits live in code (billing.go)
-- until Stripe lands in P2; the API reports usage vs limits so the UI tells
-- the truth and quotas can go hard later without a schema change.

ALTER TABLE tenants ADD COLUMN IF NOT EXISTS trial_ends_at timestamptz;

-- Backfill from creation time (fair: trial starts at signup, not at upgrade).
UPDATE tenants SET trial_ends_at = created_at + interval '14 days'
WHERE trial_ends_at IS NULL;

ALTER TABLE tenants ALTER COLUMN trial_ends_at SET NOT NULL;
ALTER TABLE tenants ALTER COLUMN trial_ends_at
    SET DEFAULT (now() + interval '14 days');

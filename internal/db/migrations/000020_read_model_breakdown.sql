-- 000020_read_model_breakdown.sql — persist the risk breakdown and drive
-- refreshes from a dirty flag instead of a time window.
--
-- risk_breakdown stores exactly what Score() computed, term by term, so the
-- API and the UI render those numbers instead of re-deriving the weights in a
-- second language (shipments/page.tsx once carried two inline copies, which is
-- how client and server silently disagreed). The column is NOT NULL with an
-- empty-object default so rows written before this migration still decode.
--
-- needs_refresh replaces the 15-minute staleness window that RefreshBatch used
-- to find work. That window, against an hourly sweep, qualified every row on
-- every pass — a full recompute truncated at 2000, not an incremental delta.
-- The flag is set by every path that mutates a shipment's inputs (ingest,
-- alert changes, subscription changes) and cleared by a successful refresh, so
-- the batch processes a bounded queue instead of scanning.
ALTER TABLE shipment_current
    ADD COLUMN IF NOT EXISTS risk_breakdown jsonb NOT NULL DEFAULT '{}';

ALTER TABLE shipments
    ADD COLUMN IF NOT EXISTS needs_refresh boolean NOT NULL DEFAULT true;

COMMENT ON COLUMN shipment_current.risk_breakdown IS
    'Per-term points from Score(): the single home of the weights. Render it, never recompute it.';
COMMENT ON COLUMN shipments.needs_refresh IS
    'True when the shipment_current row may be stale. Set by writers, cleared by RefreshBatch.';

-- Backfill: every existing shipment needs one refresh under the new regime.
-- Rows that already have a current row keep it until the flag-driven batch
-- reaches them; rows that never had one are picked up first via NULLS FIRST.
UPDATE shipments SET needs_refresh = true;

-- The dirty-flag batch orders by shipment_current.updated_at and must not
-- sequential-scan to do it.
CREATE INDEX IF NOT EXISTS shipment_current_updated_idx
    ON shipment_current (updated_at);
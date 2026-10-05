-- 000023_lane_stats.sql — pre-aggregate lane transit percentiles.
--
-- LaneStatsFor loaded every delivered shipment of a lane into Go memory on
-- every ETA recalculation: unbounded, unindexed, once per carrier event
-- carrying an ETA. A popular lane turns each recalculation into a 100k-row
-- scan that breaches the 5s statement timeout.
--
-- lane_stats materialises p50/p90 per lane so the hot path reads one indexed
-- row. It is refreshed when a shipment is delivered (cold path: once per
-- shipment lifetime) rather than when an ETA is estimated (hot path: many
-- times per shipment). A backfill below computes every lane with enough
-- history in a single statement.
CREATE TABLE IF NOT EXISTS lane_stats (
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    origin      text NOT NULL,
    destination text NOT NULL,
    carrier     text NOT NULL DEFAULT '',
    mode        text NOT NULL DEFAULT '',
    samples     integer NOT NULL DEFAULT 0,
    p50_days    numeric NOT NULL DEFAULT 0,
    p90_days    numeric NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, origin, destination, carrier, mode)
);

ALTER TABLE lane_stats ENABLE ROW LEVEL SECURITY;
ALTER TABLE lane_stats FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS lane_stats_isolation ON lane_stats;
CREATE POLICY lane_stats_isolation ON lane_stats
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

COMMENT ON TABLE lane_stats IS
    'Learned per-lane transit percentiles. Written on delivery, read on every ETA estimate. Minimum 5 samples before use.';

-- Backfill: one statement computes every lane that already has history.
-- Lanes below the 5-sample minimum are left absent; LaneStatsFor treats
-- absence as "not enough data" and falls back without them.
INSERT INTO lane_stats (tenant_id, origin, destination, carrier, mode, samples, p50_days, p90_days)
SELECT tenant_id, origin, destination, carrier, mode,
       count(*),
       percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (delivered_at - shipped_at))/86400),
       percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM (delivered_at - shipped_at))/86400)
FROM shipments
WHERE status = 'delivered'
  AND shipped_at IS NOT NULL AND delivered_at IS NOT NULL
  AND delivered_at > shipped_at
GROUP BY tenant_id, origin, destination, carrier, mode
HAVING count(*) >= 5
ON CONFLICT (tenant_id, origin, destination, carrier, mode) DO UPDATE SET
    samples = EXCLUDED.samples, p50_days = EXCLUDED.p50_days,
    p90_days = EXCLUDED.p90_days, updated_at = now();

-- Supports the per-lane recompute on delivery and the carrier-agnostic
-- fallback lookup. Covering delivered rows only keeps it narrow.
CREATE INDEX IF NOT EXISTS shipments_lane_delivered_idx
    ON shipments (tenant_id, origin, destination, carrier, mode)
    WHERE status = 'delivered';
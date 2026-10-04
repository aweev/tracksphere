-- 000005_tenant_tracking_unique.sql — scope tracking numbers per tenant.
--
-- WHY: tracking_number was globally UNIQUE, which (a) blocks two tenants from
-- tracking the same physical shipment (common in forwarding/3PL), and (b)
-- leaks existence across tenants: tenant B creating tenant A's number got a
-- 409 duplicate_tracking oracle (should be creatable, no enumeration).
--
-- Real-world carrier numbers are unique per CARRIER, not globally, so the
-- webhook resolver is carrier-scoped (see shipments.Service.IngestEvent).
-- New contract: UNIQUE(tenant_id, tracking_number).

-- Drop the global unique constraint created by UNIQUE in 000001.
ALTER TABLE shipments DROP CONSTRAINT IF EXISTS shipments_tracking_number_key;

-- Backfill guard: with the old global constraint in place there cannot be
-- duplicates, so this is a no-op on existing data. Kept as documentation for
-- operators upgrading from a hand-modified schema.
-- (If this raises, resolve duplicates before re-running migrations.)

CREATE UNIQUE INDEX IF NOT EXISTS shipments_tenant_tracking_uniq
    ON shipments (tenant_id, tracking_number);

-- Fast carrier webhook resolver: WHERE tracking_number=$1 AND carrier=$2.
CREATE INDEX IF NOT EXISTS shipments_tracking_lookup_idx
    ON shipments (tracking_number, carrier);

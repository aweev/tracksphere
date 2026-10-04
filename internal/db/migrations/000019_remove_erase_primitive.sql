-- 000019_remove_erase_primitive.sql — remove the SECURITY DEFINER erase
-- functions added in 000018 and add declared cargo value.
--
-- WHY THE 000018 FUNCTIONS ARE DROPPED
--
-- 000018 shipped verify_tenant_erase(uuid) and list_tenant_cascade_tables() as
-- SECURITY DEFINER, granted to tracksphere_app. The migrations role is Docker's
-- POSTGRES_USER, which is a superuser and therefore bypasses RLS entirely, so
-- that GRANT made "delete any tenant by id" reachable from the application
-- role — the exact class of hole ADR 0004 exists to close. Neither function
-- pinned search_path, so a `tenants` relation appearing earlier in the
-- caller's search_path would be resolved in preference to the real one
-- (SECURITY DEFINER shadowing). Neither function performed any role check.
--
-- verify_tenant_erase was also broken as written. Its table list included
-- 'shipment_polls', which exists in no migration, and 'scheduler_leases',
-- which has no tenant_id column. The first bad name raised, which rolled back
-- the DELETE the function itself had performed. handleEraseAccount logged that
-- failure and still returned {"ok":true}, so GDPR Art. 17 erasure was a
-- guaranteed no-op that reported success to the data subject.
--
-- Cascade verification now runs in Go, inside the same transaction as the
-- delete, and derives the table list from information_schema instead of a
-- hardcoded array. See internal/httpapi/account.go.

DROP FUNCTION IF EXISTS verify_tenant_erase(uuid);
DROP FUNCTION IF EXISTS list_tenant_cascade_tables();

-- Declared commercial exposure per shipment.
--
-- NULL means the forwarder did not supply a value, which is semantically
-- different from 0 and must never be imputed: a silent 0 reads as "no money at
-- risk", which is the opposite of the truth and the reason the risk score
-- treats this term as absent rather than zero. Feeds the wValueMax term in
-- internal/readmodel and the "$X at risk" line on the exception card.
ALTER TABLE shipments
    ADD COLUMN IF NOT EXISTS value_at_risk numeric(12,2);

COMMENT ON COLUMN shipments.value_at_risk IS
    'Declared cargo value in the tenant reporting currency. NULL = not provided (distinct from 0); never imputed.';
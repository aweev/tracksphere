-- 000024_jobs_tenant.sql — scope the queue by tenant for fairness and isolation.
--
-- jobs had no tenant_id at all, which made two things impossible: fair
-- claiming (one tenant's flood occupies every poller and starves the rest),
-- and tenant-scoped dead-letter reads (any tenant admin could list and replay
-- every tenant's dead jobs). Payloads already carry tenantId; this column
-- materialises it so the claim path can index and filter on it.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS tenant_id uuid
    REFERENCES tenants(id) ON DELETE CASCADE;

-- Backfill from payloads. Invalid or absent tenant ids stay NULL, which marks
-- system jobs (sweep, digest flush) that belong to no tenant.
UPDATE jobs SET tenant_id = (payload->>'tenantId')::uuid
WHERE tenant_id IS NULL
  AND payload ? 'tenantId'
  AND (payload->>'tenantId') ~ '^[0-9a-fA-F-]{36}$';

COMMENT ON COLUMN jobs.tenant_id IS
    'Owning tenant, extracted from payload at enqueue. NULL = system job. Drives fair claiming and tenant-scoped DLQ reads.';

-- Serves fair claiming (pending by tenant and time) and the running-count
-- anti-starvation check.
CREATE INDEX IF NOT EXISTS jobs_tenant_claim_idx
    ON jobs (tenant_id, status, run_at, id)
    WHERE status IN ('pending', 'running');
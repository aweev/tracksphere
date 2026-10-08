-- 000025_dlq_tenant_scope.sql — finish the tenant scoping started in 000024.
--
-- 000024 added jobs.tenant_id but no writer populated it, so the fair-claim
-- index was dead and the DLQ API stayed global. The queue writer now sets
-- tenant_id at enqueue (see queue.EnqueueTxTenant); this migration backfills
-- any rows written by older binaries and adds the DLQ read index.
UPDATE jobs SET tenant_id = (payload->>'tenantId')::uuid
WHERE tenant_id IS NULL
  AND payload ? 'tenantId'
  AND (payload->>'tenantId') ~ '^[0-9a-fA-F-]{36}$';

-- Tenant-scoped dead-letter reads: GET /api/v1/jobs/dead and replay filter
-- on (tenant_id, status, updated_at). Partial index keeps system jobs out.
CREATE INDEX IF NOT EXISTS jobs_dead_tenant_idx
    ON jobs (tenant_id, updated_at DESC, id DESC)
    WHERE status = 'dead';

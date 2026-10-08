-- 000026_backup_runs.sql — backup freshness ledger (P1-1).
--
-- System table (no RLS, like jobs/webhook_inbox): written by the backup
-- sidecar (deploy/backup.sh) after each pg_dump, read by GET /statusz so
-- "last successful backup" is a live API fact instead of a doc claim.
-- Rows are tiny (one per backup); retention prunes past 90 days.
CREATE TABLE IF NOT EXISTS backup_runs (
    id          bigserial PRIMARY KEY,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    ok          boolean NOT NULL DEFAULT false,
    path        text NOT NULL DEFAULT '',
    bytes       bigint NOT NULL DEFAULT 0,
    error       text
);
COMMENT ON TABLE backup_runs IS
    'pg_dump ledger written by deploy/backup.sh; read by /statusz for backup freshness. No RLS: ops-only via statusz aggregate.';

-- No tenant_id by design (cluster-level). No index needed beyond PK:
-- statusz reads max(finished_at) over at most ~90 rows.

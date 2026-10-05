-- 000022_alert_severity_rank.sql — make the exception queue pageable.
--
-- The triage queue ordered by CASE severity WHEN 'critical' THEN 0 ... END,
-- which no index can supply: Postgres sorted the tenant's entire open-alert
-- set on every request, and the hardcoded LIMIT 200 with no cursor meant
-- alerts 201+ were permanently unreachable. severity_rank materialises the
-- ordering as data (0=critical, 1=warning, 2=info) so a composite index can
-- serve both the order and keyset pagination.
ALTER TABLE alerts
    ADD COLUMN IF NOT EXISTS severity_rank integer NOT NULL DEFAULT 1
    CHECK (severity_rank IN (0, 1, 2));

UPDATE alerts SET severity_rank = CASE severity
    WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END
WHERE severity_rank != CASE severity
    WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END;

COMMENT ON COLUMN alerts.severity_rank IS
    'Materialised sort key for severity (0=critical, 1=warning, 2=info). Set by writers on every INSERT and on any severity change; the triage queue orders and pages on it.';

-- Serves the queue order and the keyset prefix (tenant, status, rank).
-- due_at/detected_at/id follow in the query; the index leads the scan to the
-- right slice so each page costs a bounded range plus a small bounded sort,
-- not a sort of the tenant's whole open set.
CREATE INDEX IF NOT EXISTS alerts_queue_page_idx
    ON alerts (tenant_id, status, severity_rank, due_at, detected_at DESC, id);

-- Derive the rank from severity on every write, so no INSERT site can forget
-- it and the two can never disagree. Restricted to INSERT and severity
-- changes so the hot last_seen_at touch path pays nothing.
CREATE OR REPLACE FUNCTION set_alert_severity_rank()
RETURNS trigger AS $$
BEGIN
    NEW.severity_rank := CASE NEW.severity
        WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_alert_severity_rank ON alerts;
CREATE TRIGGER trg_alert_severity_rank
    BEFORE INSERT OR UPDATE OF severity ON alerts
    FOR EACH ROW EXECUTE FUNCTION set_alert_severity_rank();
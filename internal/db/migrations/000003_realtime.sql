-- 000003_realtime.sql — LISTEN/NOTIFY channel used by the SSE layer.
-- Payload is a JSON envelope: {"type": "...", "tenant_id": "...", ...}.
-- The API process LISTENs on 'tracksphere' and fans out to SSE subscribers;
-- notifications are delivered by Postgres ON COMMIT, so clients only ever see
-- events that are durably persisted.

-- pg_notify helper used by application code inside business transactions.
CREATE OR REPLACE FUNCTION notify_tracksphere(payload text) RETURNS void
    LANGUAGE sql AS
$$ SELECT pg_notify('tracksphere', payload) $$;

-- Demo/seed convenience: a lightweight view for the operations dashboard
-- aggregates. Kept as a view (not materialized) — analytics at scale moves to
-- ClickHouse per docs/adr/0006; until then plain SQL is enough.
CREATE VIEW tenant_dashboard AS
SELECT
    s.tenant_id,
    count(*) FILTER (WHERE s.status NOT IN ('delivered', 'cancelled')) AS active_shipments,
    count(*) FILTER (WHERE s.status = 'delivered')                     AS delivered_shipments,
    count(*) FILTER (WHERE s.status = 'exception')                     AS exception_shipments,
    count(*) FILTER (WHERE s.eta < now() AND s.status NOT IN ('delivered', 'cancelled'))
                                                                       AS overdue_shipments
FROM shipments s
GROUP BY s.tenant_id;

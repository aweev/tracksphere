-- 000017_sse_subscriptions.sql — track SSE subscribers for metrics and observability
--
-- The realtime hub (internal/realtime/hub.go) manages in-memory SSE subscribers
-- but we also need a persistent record for:
--   1. Metrics: /api/v1/metrics?per_tenant=true shows subscribers per tenant
--   2. Debugging: "why is this tenant getting events but not that one?"
--   3. Capacity: know total SSE connections across API replicas
--
-- This table is written by the hub on subscribe/unsubscribe (best-effort).
-- It is NOT the source of truth for delivery (that's in-memory hub), but it
-- gives operators visibility into connection state.

CREATE TABLE IF NOT EXISTS sse_subscriptions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id     uuid REFERENCES shipments(id) ON DELETE SET NULL,
    subscriber_id   uuid NOT NULL,
    connected_at    timestamptz NOT NULL DEFAULT now(),
    disconnected_at timestamptz,
    user_agent      text,
    ip_hash         text
);
CREATE INDEX IF NOT EXISTS sse_subscriptions_tenant_idx
    ON sse_subscriptions (tenant_id, connected_at DESC);
CREATE INDEX IF NOT EXISTS sse_subscriptions_active_idx
    ON sse_subscriptions (tenant_id) WHERE disconnected_at IS NULL;

ALTER TABLE sse_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sse_subscriptions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS sse_subscriptions_isolation ON sse_subscriptions;
CREATE POLICY sse_subscriptions_isolation ON sse_subscriptions
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON sse_subscriptions TO tracksphere_app;
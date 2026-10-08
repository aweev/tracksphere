-- 000027_inbox_flood_idx.sql — serve the per-carrier flood guard (P1-2).
--
-- POST /webhooks/carriers/{carrier} counts inbox rows per carrier over the
-- rolling hour before inserting (TRACKSPHERE_WEBHOOK_FLOOD_PER_HOUR). Without
-- this index that count is a seq scan on the hottest table during a flood —
-- exactly when it must be cheap.
CREATE INDEX IF NOT EXISTS webhook_inbox_carrier_hour_idx
    ON webhook_inbox (carrier, received_at DESC);

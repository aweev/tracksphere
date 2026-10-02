# ADR 0005 — Realtime via LISTEN/NOTIFY and Server-Sent Events

**Status:** accepted · **Date:** 2026-10-01

## Context
The control tower must reflect carrier updates without a refresh, and the public
portal should look live. Carrier traffic is bursty and predominantly
server→client; browsers are already authenticated with a session cookie.

## Decision
- When a business transaction commits a change, it calls
  `pg_notify('tracksphere', <json>)` **inside the transaction**, so subscribers
  only ever observe durably-committed state.
- Each API instance runs a dedicated connection (`realtime.Hub.Run`) that
  `LISTEN`s on the channel and republishes to an in-process hub.
- The hub fans out **only to subscribers of the same `tenant_id`** — tenant
  scoping is applied again at the fan-out layer, after RLS already scoped the
  write.
- Browsers connect to `GET /api/v1/stream` (Server-Sent Events, cookie
  authenticated). SSE is chosen over WebSockets because the flow is one-way,
  it reconnects automatically, and it survives proxies as plain HTTP.
- Slow consumers are dropped (bounded per-subscriber buffer) rather than allowed
  to block the hub; `EventSource` reconnects and refetches.

## Consequences
- No WebSocket infrastructure, no broker, no sticky sessions: any API instance can
  serve any client because every instance sees every notification and filters by
  tenant.
- A dropped event degrades to "stale until next fetch" — acceptable, because React
  Query invalidation refetches authoritative state. SSE is an accelerant, not the
  source of truth.
- `pg_notify` payloads are capped (~8 KB); we send identifiers and let clients
  refetch, so this ceiling is irrelevant.
- Multi-instance correctness depends on each process holding its own LISTEN
  connection; the hub reconnects with backoff if it drops.

## Alternatives considered
- **WebSockets**: bidirectional capability we do not need, plus more proxy and
  scaling complexity.
- **Client polling**: simplest, but wasteful and visibly laggy at thousands of
  open dashboards.
- **Redis pub/sub**: adds a cluster; redundant while Postgres is already present.

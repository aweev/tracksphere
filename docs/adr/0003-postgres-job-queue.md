# ADR 0003 — Job queue in Postgres (transactional outbox + SKIP LOCKED)

**Status:** accepted · **Date:** 2026-10-01

## Context
Ingesting a carrier event must (a) persist the timeline event, (b) advance the
shipment, (c) raise alerts, (d) notify the customer, (e) recompute ETA. Steps (c)–(e)
are slow and failure-prone, so they belong in a background worker. The hard
requirement is that a committed carrier event is **never** silently dropped, and
that replayed webhooks do not duplicate side effects.

## Decision
Implement the queue as a `jobs` table in the same Postgres database:
- jobs are inserted **inside the business transaction** (transactional outbox), so
  "event stored" and "work scheduled" commit atomically or not at all;
- workers claim work with `UPDATE … WHERE id = (SELECT … ORDER BY run_at, id
  FOR UPDATE SKIP LOCKED LIMIT 1)`, making N replicas safe without coordination;
- retries use exponential backoff (`2^attempts`, capped at 5 min) and a
  `max_attempts` ceiling, after which the job becomes `dead`;
- `reapStuck` returns jobs locked by a crashed replica (older than 5 minutes) to
  the pending pool;
- idempotency is enforced by data constraints, not by the queue:
  `UNIQUE (tenant_id, dedup_key)` on events and a partial unique index on open
  alerts.

## Consequences
- At-least-once delivery with data-level idempotency → no lost events, no
  duplicate alerts/notifications from replays.
- No broker to operate; jobs are inspectable with plain SQL and can be replayed.
- Polling (`TRACKSPHERE_WORKER_POLL_INTERVAL_MS`, default 250 ms in dev) adds
  latency; acceptable for scan events. A `LISTEN/NOTIFY` wake-up is a cheap
  future optimisation that does not change the contract.
- Job throughput is bounded by Postgres write capacity; the partial index keeps
  the claim query O(pending).

## Alternatives considered
- **Redis/asynq** or **NATS JetStream**: better throughput, but enqueueing is no
  longer atomic with the business write, so a crash between commit and enqueue
  loses work. Would need an outbox anyway.
- **River** (Postgres queue library): compatible with this design; skipped to keep
  the dependency surface small and the retry/reap semantics explicit.

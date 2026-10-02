# ADR 0002 — PostgreSQL is the only datastore

**Status:** accepted · **Date:** 2026-10-01

## Context
The feature list implies Redis (cache), a message broker (Kafka/NATS), and
ClickHouse (analytics). Each is a separate cluster to run, monitor, back up, and
pay for. The stated constraint is to reduce resource-heavy technologies and keep
operations simple on VMs.

## Decision
Use **PostgreSQL 16 for everything** in phase 1:
- primary data store,
- job queue (ADR 0003),
- realtime fan-out via `LISTEN/NOTIFY` (ADR 0005),
- initially the analytics store too (plain SQL + views).

Introduce no other datastore until a **measured** trigger is crossed:
- analytics beyond ~10M events/month → ClickHouse,
- cache hit-rate or latency requirements that Postgres cannot meet → Redis,
- cross-service event volume beyond `LISTEN/NOTIFY` payload limits → NATS.

## Consequences
- One backup strategy (`pgBackRest`/`WAL-G` to object storage), one connection
  pool, one set of credentials, one thing to page on.
- Whole stack fits a 2 vCPU / 4 GB VM (~€80–120/month across Hetzner + DO).
- Postgres becomes the throughput ceiling. `jobs` claim queries are indexed with a
  partial index (`WHERE status='pending'`) and use `SKIP LOCKED`, which is
  efficient at the target scale; the trigger above defines when to revisit.

## Alternatives considered
- **Redis + NATS + ClickHouse from day one**: better ceiling, far worse
  operational cost for a team of this size at this stage.

# ADR 0008 — A maintained read model, not live aggregates

- **Status**: accepted
- **Date**: 2026-10-02
- **Amends**: ADR 0002 (Postgres-only).

## Context

Every list view and dashboard tile was a live aggregate over the write tables.
The dashboard issued five independent counts. The shipments list issued
`count(*)` plus a page. The carrier poller ran a correlated
`max(occurred_at)` subquery per row. Search was `ILIKE '%x%'`, a sequential
scan.

That is fine at six shipments — which is what the seed data has — and
unacceptable at 50,000. More importantly, it made the most valuable number in
the product impossible to produce: *how much trouble is this shipment in?*
Sorting by that requires computing it for every row, every request.

The underlying gap is expressive, not just performance. `shipments.status` is one
flat mutable field, so there was nowhere to record "arrived 6 days ago against a
2-day norm". Dwell time was not measurable, therefore the highest-value
exception class could not be detected (see ADR 0007).

## Decision

1. **`shipment_current`: one narrow row per shipment**, carrying exactly the
   fields hot paths read: status, risk score and tier, open/critical alert
   counts, last event at and code, staleness hours, ETA with **provenance**, ETA
   slip, dwell against the expected norm, value at risk, and whether the customer
   has been told. `FORCE`d RLS, same policy shape as every other tenant table.

2. **The risk score is computed once, here, in Go** (`internal/readmodel`), not
   per request in the client. A score computed per request cannot be sorted
   without recomputing the world, and ops need the list ordered by consequence.
   It is a pure, bounded, unit-tested function: every term saturates, and the
   result is always 0–100.

3. **Milestones are their own table.** `shipment_milestones` records expected and
   actual times per stage, so dwell time becomes expressible and per-stage
   expectations can be seeded from lane norms and corrected by real data.

4. **Refreshing is cheap and self-healing.** `Refresh` is called inside the
   transaction that changes state (ingest, alert raise, resolve), and
   `RefreshBatch` lets the sweep top up anything missed. The read model cannot
   silently rot.

5. **ETA provenance is first-class.** `eta_source` ∈ {none, carrier, estimated,
   lane_model} with a confidence. A carrier-published ETA and our own estimate
   must never render identically — the first time an estimate is wrong by a week,
   operators stop believing every number on the screen.

6. **Ordering is the default.** `GET /shipments?sort=risk` is the operational
   ordering; `recent`, `eta` and `stale` are views. The previous client-side sort
   sorted one page, which is meaningless across pages.

## Alternatives rejected

- **Materialized views.** Rejected: they refresh on a schedule, so the ordering a
  user acts on can be minutes stale, and they cannot compute a Go risk function.
- **Add the columns to `shipments`.** Rejected: the read model needs to be
  rewritable without touching write-path row width and locking, and it will grow
  columns as the model improves. A separate table keeps the write path narrow.
- **ClickHouse / Redis.** Rejected *for now*, consistent with ADR 0002. The
  trigger is recorded there as a number, not an adjective.
- **Compute risk in the browser.** Rejected: unsortable server-side, unfilterable,
  and it would put business logic in two places.

## Consequences

- List and dashboard queries become index-only scans on a purpose-built table.
- Risk ordering is available and consistent across every surface.
- One extra row written per state change. That is a real cost, and it is the
  trade that buys sub-linear dashboards.
- `shipment_current` must be backfilled on first deploy; `RefreshBatch` does
  this, and rows can be absent safely (they sort last) rather than erroring.
# ADR 0007 — Exception detection runs on a clock, and reconciles

- **Status**: accepted
- **Date**: 2026-10-02
- **Supersedes**: nothing. **Amends**: ADR 0003 (job queue kinds).

## Context

The exception engine evaluated a hardcoded `switch` on a carrier event code,
inside the event handler. Two properties of that design were wrong in ways that
only appear with real traffic.

**1. Every rule was event-triggered.** The most expensive exception in freight
is *nothing happening*. A container sits at a terminal for six days because the
vessel missed a transhipment connection, or the paperwork was never approved.
There is no event, so no rule fired — ever. The shipment was invisible to the
entire detection system, and the customer found out before we did.

**2. Rules appended, they did not converge.** An SLA-breach alert opened on day 1
of a late shipment stayed open at day 90, after the container was delivered.
Every "unresolved exceptions" number the product showed was therefore wrong, and
the alert queue was noise.

There was also a subtler variant of bug 1: a *time-based* check
(`IsAnomaly(time.Since(createdAt))`) had been added inside the event handler. It
looked like time-based detection and was not — it fired whenever some unrelated
scan happened to land, with a `detected_at` hours too late. Confident, mistimed
criticals are worse than silence, because they teach a team to distrust the
channel.

## Decision

1. **Two kinds of rule, explicitly separated.** `trigger_type = 'event'` rules are
   evaluated by the ingestion handler and only ever test event-shaped facts. No
   time-based condition may live there. `trigger_type = 'sweep'` rules are
   evaluated by a scheduler. This is enforced by review and by
   `TestEvaluateRules_NoTimeBasedConditionsRemain`.

2. **Rules are data, not code.** `alert_rules` holds JSON conditions and raise
   specs, per tenant, versioned and disable-able. Support can add a rule for a
   customer without a deploy. The shipped set is seeded idempotently per tenant
   by name.

3. **Detection reconciles instead of appending.** Each sweep sets `last_seen_at`
   on the alerts it confirms. Any sweep-managed alert unconfirmed for two lease
   windows is auto-resolved with `resolution = 'condition_cleared'`. A single
   slow or failed pass cannot mass-close a queue, because the threshold is two
   windows.

4. **Periodic work is lease-held, not per-replica.** `scheduler_leases` is
   claimed with a conditional `INSERT … ON CONFLICT DO UPDATE … WHERE
   expires_at < now()`, so exactly one replica runs a window and a crashed holder
   is replaced after the TTL. Per-replica tickers would run N times over.

5. **Scheduling is idempotent.** `jobs.dedup_key` with a partial unique index
   makes the hourly sweep content-addressed, so a restart mid-window or two
   replicas firing together cannot double-run.

6. **Silence is relative to the lane norm.** The staleness threshold is
   `expected_dwell / 6`, floored at 24h. An absolute "48h" threshold is wrong for
   every mode except road: 48h of quiet on a 21-day ocean transit is exactly on
   plan, and scoring it as risky teaches operators to ignore the column.

## Alternatives rejected

- **Run time-based checks in the event handler, with a timestamp fix.** Rejected:
  the trigger is still wrong. A shipment with no events has no handler to run.
- **A periodic sweep in every replica, guarded by an advisory lock.** Rejected:
  advisory locks are session-scoped and interact badly with pool reconnects. A
  leased row is inspectable, survives crashes, and reports its own staleness.
- **Keep appending alerts, and filter stale ones in the UI.** Rejected: the
  counts stay wrong, the queue stays unbounded, and every consumer of the data
  has to re-derive the truth.

## Consequences

- Staleness and dwell breaches become detectable at all. This is the single
  biggest capability gain in the product.
- Every "open exceptions" number becomes trustworthy, which is what makes the
  exception queue usable as a work queue.
- The alert table grows with resolved rows. `RetentionSweep` and `ArchiveOld`
  handle this; retention is a deliberate per-tenant decision, not a silent cap.
- A tenant can now generate more exceptions than before, because detection is
  genuinely broader. That is why ADR 0009's interrupt budget is not optional:
  broader detection without a noise ceiling is a net negative.
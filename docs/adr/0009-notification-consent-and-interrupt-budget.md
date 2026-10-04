# ADR 0009 — Notification routing: consent first, then a hard interrupt budget

- **Status**: accepted
- **Date**: 2026-10-02

## Context

Two independent failures, one on the customer side and one on the operator side.

**Customer side.** `POST /track/{n}/subscribe` accepted any `recipient` — any
phone number, any address — with no verification, no double opt-in, no consent
record, and no per-shipment cap. `tracking_subscriptions` then fanned out to
every subscriber on every ingested event. Anyone could POST a victim's number and
cause unsolicited SMS/WhatsApp/email on every status change of any published
shipment. That is harassment, a direct cost on metered channels, and a WhatsApp
Business policy violation that risks the sending account — which would break
delivery for every tenant at once.

**Operator side.** Every ingested event enqueued a notification. Every alert
carried a severity that was only ever rendered as a colour. A single
port-congestion webhook touching 500 shipments produced 500 alerts and 500
notifications. P3 then *added* two more alert sources. Alert fatigue is the
documented number-one reason operations teams abandon visibility platforms, and
the volume was going up while the controls were unbuilt.

## Decision

### Consent

1. **Double opt-in for metered channels.** SMS and WhatsApp subscriptions are
   created `pending` with a high-entropy confirmation token and are **not
   deliverable** until confirmed. Only `status = 'active'` rows are ever selected
   by the fan-out query. Email activates immediately: proving control of an inbox
   the customer typed into is itself the consent signal, and email is unmetered.

2. **Every step is audited.** `notification_consent` records `requested`,
   `confirmed`, `revoked`, `suppressed` and `delivered` with the reason. A
   subscription cannot be created without its audit row — the audit insert shares
   the transaction.

3. **Hard caps, enforced inside the transaction** so concurrent requests cannot
   both pass the count: 25 subscribers per shipment, 5 pending confirmations per
   recipient.

4. **Opt-out needs no account and never confirms anything.** `POST
   /subscribe/unsubscribe` always returns 200, so a one-click unsubscribe cannot
   leak whether an address was ever subscribed.

### Interrupt budget

5. **Severity → channel is a constant, not configuration.** `info` never
   interrupts and never reaches a customer; `warning` tells the customer but does
   not interrupt; only `critical` may. Getting this wrong is how a team gets
   buried, so it is not configurable.

6. **A hard ceiling of N interrupts per recipient per hour** (default 5),
   enforced against `notification_interrupts` inside the same transaction that
   sends. The budget is spent by the most severe first.

7. **Per-shipment cooldown** (default 6h) collapses a burst of rules on one
   container into one interruption.

8. **Quiet hours in the tenant's timezone** (default 22:00–07:00). A 3am page
   about a container nobody is waiting on is exactly what the ceiling exists to
   prevent.

9. **Suppressed means deferred, never dropped.** Everything not interrupted goes
   to `notification_digest_queue` and is flushed as a *grouped* digest — forty
   customs holds become one line, not forty interruptions.

10. **Escalation requires acknowledgement.** An unacknowledged alert past its SLA
    is not escalated. Nagging someone who has not seen it yet is the behaviour
    that trains a team to ignore the queue.

`DecideRoute` is a pure function and the whole policy is unit-tested, including
the midnight-wrapping quiet window and the zero-cap case.

## Alternatives rejected

- **Configurable severity routing per tenant.** Rejected for severity → *channel*.
  Configuration here means one customer's preference can page a whole ops team.
  The budget stays configurable; the mapping does not.
- **Throttle by discarding the excess.** Rejected: discarding information to fix
  a volume problem destroys the value of the product. Digest it.
- **Rely on provider-side rate limits (Twilio) for the budget.** Rejected: that
  limit protects the account, not the operator's attention, and it fires after
  the damage.
- **Verify SMS by OTP inside the request.** Rejected as an unnecessary round trip
  for a self-service portal; the link/token flow is one tap and is standard for
  WhatsApp.

## Consequences

- The product can no longer be used as an unsolicited-message relay.
- Operators receive at most 5 interrupting notifications an hour regardless of
  how bad the incident is; the rest are batched. This is a deliberate product
  trade: completeness is preserved, immediacy is capped.
- A tenant that *wants* everything immediately sets a cap of 50 and disables quiet
  hours. That is now a choice they can make, which it was not before.
- The confirmation flow adds one step before SMS/WhatsApp updates begin, which
  costs some opt-in volume and buys deliverability and legal defensibility.
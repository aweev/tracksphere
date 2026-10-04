# ADR 0010 — Abuse posture: trusted proxies, public-data minimisation, framing

- **Status**: accepted
- **Date**: 2026-10-02
- **Amends**: ADR 0005 (realtime), ADR 0006 (deployment).

## Context

Three of these are the same mistake in different places: **trusting a claim made
by the client about who the client is.**

**Rate limiting was decorative.** `clientKey` derived a per-IP bucket from
`r.RemoteAddr`, relying on chi's `middleware.RealIP` having already rewritten it
from trusted proxy headers. chi's `RealIP` believes `X-Forwarded-For` and
`X-Real-IP` from **any** peer — there is no allowlist. So a caller could rotate
the header per request and receive a fresh bucket every time. Meanwhile, in the
deployment ADR 0006 actually specifies (behind Cloudflare/nginx), an absent or
untrusted header leaves `RemoteAddr` as the *edge node*, so every tenant behind
that edge shares one bucket and one customer's traffic rate-limits everyone
else's.

The endpoint being protected is `GET /api/v1/track/{trackingNumber}`. Container
numbers are `MRKU` + 6 digits + a check digit — low entropy, effectively
enumerable. This was the most serious finding in the audit and the limiter as
built did not mitigate it.

**The portal stream was tenant-scoped.** There was no public stream at all, so the
customer-facing tracker was a snapshot. Adding one naively (subscribe the
tenant) would have handed every anonymous visitor every other customer's events.

**`X-Frame-Options: DENY` plus `frame-ancestors 'none'`** made the embeddable
tracking widget impossible — the stated growth engine — while CSP `img-src`
allowed no external origin, so every externally hosted tenant logo was silently
blocked and white-label looked broken with no error anywhere.

**`script-src` shipped `'unsafe-inline' 'unsafe-eval'`.** `'unsafe-eval'` is a
dev-only need; shipping it removes essentially all CSP protection against XSS
while the header's presence implies otherwise.

## Decision

1. **Forwarding headers are believed only from a configured trusted proxy.**
   `TRACKSPHERE_TRUSTED_PROXIES` takes CIDRs; unparseable entries are a startup
   error, because silently ignoring one quietly disables the protection it gates.
   Otherwise the bucket keys on the peer address. chi's `RealIP` is removed.
   `CF-Connecting-IP` is honoured only behind an explicit opt-in.

2. **Two budgets on the public tracking routes**, not one: a source-address
   budget and an independent subject budget keyed on a SHA-256 fingerprint of the
   tracking number. Rotating addresses defeats the first and nothing else.
   Buckets are lazily swept and bounded so a long-running process cannot be grown
   without limit.

3. **The public stream is shipment-scoped.** The portal resolves the shipment
   through the public projection (so a private shipment is invisible), then
   subscribes to exactly that one shipment. `SubscribeShipment` filters on
   `ShipmentID` inside `Publish`. An anonymous visitor can never observe another
   customer's events.

4. **SSE is resumed, not refetched.** A monotonic `id:` and a `retry:` hint, plus
   a 20s heartbeat, so an idle connection is not reaped and a reconnect can tell
   a gap from silence.

5. **Framing is https-only, not off.** `X-Frame-Options: SAMEORIGIN` and
   `frame-ancestors 'self' https:`. Plaintext framing stays blocked. `DENY` /
   `'none'` made the widget impossible; `'self' https:` makes it possible without
   opening a downgrade path.

6. **CSP `img-src` includes tenant logo origins**, restricted to https and to
   known CDNs rather than a wildcard scheme.

7. **`'unsafe-eval'` is not shipped.** `'unsafe-inline'` remains because Next.js
   injects inline bootstrap scripts; nonces are the correct fix and are tracked.
   HSTS and `object-src 'none'` / `base-uri 'self'` are added.

8. **Compression skips the event stream.** gzip buffers, and a buffered stream
   defeats the point of SSE.

9. **`/metrics` is off by default** (`TRACKSPHERE_EXPOSE_METRICS`). Queue depth
   and outage rate are operational reconnaissance, and the endpoint shares the
   public hostname. It exposes global counters only, never tenant labels.

10. **The inbox query is driven from the tenant-scoped side.** `webhook_inbox` has
    no `tenant_id` and no RLS, so `FROM webhook_inbox … WHERE s.tenant_id = $1
    … LIMIT 50` scanned every tenant's webhooks on every page view and only then
    applied the limit.

## Alternatives rejected

- **Key the limiter on tracking number alone.** Rejected: a single legitimate
  customer looking up their own shipment repeatedly, and a shared office NAT,
  would both trip it.
- **A CAPTCHA after N failures.** Rejected as the *only* control: it does nothing
  against a low-and-slow sweep, which is exactly the enumeration pattern here.
- **Keep `RealIP` and add a trusted-proxy list to it.** Rejected: chi's middleware
  has no such hook. Rewriting in our own `clientIP` is the only way to make the
  trust decision explicit and testable.
- **Serve the widget from a separate subdomain.** Rejected for now: it fragments
  the origin model for a fix that `frame-ancestors` already provides adequately at
  this stage. Revisit if a tenant needs first-party cookies on the embedded frame.

## Consequences

- Enumeration of the tracking space is bounded by the subject budget, not by how
  many addresses an attacker controls. **The public portal is now safe to launch.**
- Behind Cloudflare, operators must set `TRACKSPHERE_TRUSTED_PROXIES` correctly
  or every request keys on the edge IP. This is a deployment footgun and is
  called out in README and `deploy/`.
- A tenant logo hosted somewhere outside the listed CDN patterns is still blocked.
  The list is deliberately not a wildcard; widening it is a config change.
- `unsafe-inline` remains the weakest link in the CSP. Nonces need a middleware
  that rewrites Next's bootstrap tags and are the next piece of work.
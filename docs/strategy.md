# TrackSphere — Strategy

The positioning, the wedge, and what is deliberately **not** being built. This
replaces the original planning documents; the implementation now lives in
`docs/architecture.md` and the decisions live in `docs/adr/`.

## The wedge

Visibility is a mature, crowded, enterprise-priced category. Every competitor
leads with a **map**, because a map is impressive in a demo. A map is not a
differentiator; it is table stakes.

**TrackSphere is the exception engine and customer-communication layer for
mid-market freight forwarders** — the Douala→Rotterdam operator, the Lagos
consolidator, the Nairobi 3PL — running their business on Excel, WhatsApp groups
and seventeen carrier portal logins. 200 to 20,000 shipments a month. Too small
for CargoWise, too complex for a free tracker, unserved by enterprise platforms
that cost six figures and need a six-month rollout.

Their actual pain, by financial damage:

| Pain | Cost to them | Status in this product |
|---|---|---|
| A shipment silently goes dark at a port | The customer calls *them*. Penalty + relationship | Detected by staleness sweep (ADR 0007) |
| A container is held at customs unnoticed | Demurrage, storage, chargebacks, lost account | Carrier event + SLA countdown (ADR 0007) |
| Nothing useful to tell the customer | Every shipment is a "please hold" email | WhatsApp/SMS/email with consent (ADR 0009) |
| Cannot prove what was said | Disputes go to the carrier and they lose | Root-cause taxonomy + operator audit log |
| Cannot tell which carrier to blame | Renegotiates on anecdote | Root cause per exception, ready for scorecards |

None of that is a map problem. All of it is an exception-and-communication
problem.

## The four defensible things

1. **The exception engine.** Not "here is a red dot." *This container has been at
   Rotterdam 6 days against a 2-day norm, is 3 days past contractual free time,
   and its customer is a named account — here is the likely cause, the one-click
   action, the owner, and the clock.*
2. **WhatsApp as the primary channel.** In every target market it is the actual
   customer channel, it costs a fraction of SMS, and open rates are an order of
   magnitude higher. It ships as a first-class channel with consent, not a
   "Growth tier" checkbox.
3. **White-label as a sales weapon.** When a forwarder shows their own customer a
   TrackSphere-branded page, you have been exposed as a vendor in their customer
   relationship. Tenant branding is in the portal, the email, and the framing
   policy.
4. **Closure as a data moat.** Every resolved exception carries a root cause.
   In 24 months that is the only real dataset on how Maersk actually behaves on
   the Douala→Rotterdam lane. Carrier scorecards and ETA calibration are built on
   it, and they are not copyable.

## The three loops

```
OPS LOOP      event → detect → explain cost → assign → act → resolve → learn
                     ▲                                            │
                     └────────── root cause, carrier scorecard ──────┘

CUSTOMER LOOP  track → opt in → confirm → milestone message → arrive → share
                         ▲                                    │
                         └────── fewer "where is my order?" calls

GROWTH LOOP    exception resolved faster → renews → adds a carrier
               → more data → better detection → fewer exceptions
```

Every screen serves one of these. If it serves none, it is decoration.

## Success metrics

| Metric | Target |
|---|---|
| Time to first value (signup → first real exception) | < 10 min |
| Time to first delivered customer notification | < 60 s from event |
| Exception precision (judged useful by an operator) | > 85% |
| Interrupts per operator per hour | ≤ 5, enforced in code |
| Median exception time-to-close | < 4 h |
| Detection lag for a stalled shipment | < 2 h |
| "Where is my order?" contacts per 1,000 shipments | −60% |
| Ingest freshness p99 (carrier event → visible) | < 5 s |
| Trial → paid conversion | > 20% |

## What we are not building

Discipline is what separates a company from a feature pile.

- **A better map.** Everyone has one. Spend the effort on the exception engine.
- **A native mobile app** before the web app works on a phone. A good PWA covers
  90% of field use at 10% of the cost.
- **Carrier-by-carrier integrations** beyond a handful. Build the adapter
  framework, ship the top 3–5 plus a simulator, and let webhook onboarding handle
  the long tail.
- **A general-purpose analytics warehouse.** Postgres plus a read model covers the
  real questions. ClickHouse on ADR 0002's recorded numeric trigger.
- **Microservices.** One Go binary is a feature at this stage.
- **Feature parity with CargoWise.** They are a 30-year company. The win is depth
  on the exception engine and the customer conversation, not breadth.

## Honest state of the claims

The old feature sheet claimed things the product could not do — native carrier
integrations, AI/ML ETA, 99.9% uptime, GDPR deletion inside 72 hours, AES-256
"at rest" as a product feature. Those were removed. **Every claim in this repo
must map to something demonstrable or evidence you can hand over**; a sales sheet
that over-promises loses the deal in week three of the security review, after the
credibility is spent.

Concretely today:

- ETA is a **heuristic with a lane-learned fallback**, labelled `estimated` or
  `lane_model` in the API and marked `est` in the UI. It is never rendered
  identically to a carrier-published date.
- "AI carrier-detect" is a **tracking-number shape heuristic**, not a model.
- Carrier connectivity is **webhooks plus a poll adapter framework**, not native
  integrations with Maersk or DHL.
- **No uptime SLA is claimed.** There is no SLO yet; ADR 0007/0008 gave the
  observability to build one.

## Roadmap

**Wave 0 — correctness and trust.** Done: status transition matrix, RLS backfill,
trusted-proxy rate limiting, consent, poll transaction boundaries, read model.

**Wave 1 — finish the product.** Shipped: exception queue with ownership, SLA
countdowns, snooze, root cause and notes; declarative rules; reconciliation;
leased sweep; interrupt budget and digests; risk-sorted lists; live fleet map;
portal SSE; tenant branding.

**Wave 2 — monetise.** Entitlement-driven plans and quotas, one real carrier
integration done properly plus a simulator, developer portal with API keys and
outbound webhooks, carrier scorecards and lane analytics, document management
with a per-shipment checklist, embeddable widget, Shopify/WooCommerce import.

**Wave 3 — enterprise and moat.** OIDC/SAML SSO and SCIM, ERP connectors,
calibrated ETA with persisted prediction history, carbon/ESG reporting for
European shippers, SOC 2 evidence, multi-region with a tested restore path.
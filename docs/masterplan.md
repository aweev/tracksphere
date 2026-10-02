# TrackSphere — Master Plan

> The audit, the thesis, and the build order.
> Written against the repository as it stands on 2026-10-02.

This document does three things: it tells you **honestly what you have**, it
decides **what TrackSphere should become**, and it gives the **order of
construction** with acceptance criteria, so it is a plan and not a wish list.

Everything below is cited against real files. Where I claim a defect, you can go
read the line.

---

## 1. Verdict, honestly

**What you actually have is a genuinely good skeleton with a missing middle.**

The infrastructure layer is better than most teams ship in a year. Postgres-only
with `FORCE RLS` on a non-superuser role (ADR 0004) is a real isolation proof,
not a claim. A transactional outbox with `SKIP LOCKED` and data-level idempotency
(ADR 0003) is the correct queue design. `LISTEN/NOTIFY` → tenant-filtered SSE
(ADR 0005) is the right amount of machinery. Six ADRs that mostly say "no" is a
team that understands operational cost.

**But the product is not a control tower yet. It is a shipment table with a
status column.** The thing that makes a control tower a control tower is
*telling a human what broke and what to do about it* — and that is exactly the
half you have not built. There is no exception queue, no assignment, no SLA, no
owner, no root cause, no time-based detection, no notification channel at all.
Alerts exist as a database table and a read-only list.

Three findings that matter more than any feature on the feature sheet:

1. **`notify_eta.go` writes notifications to a table nothing reads.** There is no
   email sender, no SMS, no WhatsApp, no push. The `notifications` table is
   write-only. So the "Core" tier promise *Automated email notifications* is
   currently 0% delivered, while the public-facing sheet lists it as shipped.
2. **The public portal is not live.** `useLiveStream` is called only from
   `Shell.tsx:19`. The tracker at `track/[trackingNumber]/page.tsx` does not use
   `Shell`, so it never subscribes to SSE. The single most-demoed surface in the
   product — the one your customer sees — shows a snapshot fetched once.
3. **The feature sheet is a sales document, and parts of it are not true.** See
   §3.6. Shipping claims you cannot evidence is the fastest way to lose an
   enterprise deal in the security review, and it is currently the default
   posture of the repo.

The good news: none of the expensive decisions are wrong. You do not need to
rewrite the architecture. You need to **finish the product inside the
architecture you already chose**, and fix four things that will actively destroy
trust at scale.

---

## 2. The thesis — what the unicorn actually is

You asked for the ultimate. Here is the specific answer, and it is narrower than
the feature sheet.

### The market you are in

Visibility is a mature, crowded, enterprise-priced category: Flexport, project44,
FourKites, Shipsteer, Ship24, CargoWise, and a long tail of carrier portals.
Every one of them leads with a **map**, because a map is what looks impressive in
a demo and it is what every competitor ships. A map is not a differentiator. It is
table stakes.

### The wedge nobody is serving

**Mid-market freight forwarders and e-commerce shippers in emerging markets** —
the Douala→Rotterdam operator, the Lagos consolidator, the Nairobi 3PL — running
their business on Excel, WhatsApp groups, and seventeen separate carrier portal
logins. They have 200 to 20,000 shipments a month. They are too small for
CargoWise, too complex for Ship24, and completely un-served by enterprise
visibility platforms that cost six figures and require a six-month rollout.

Their actual pain, in order of financial damage:

| Pain | What it costs them | What exists today |
|---|---|---|
| A shipment silently goes dark at a port | Customer calls *them*. They find out late. Penalty + relationship | Nothing. Zero visibility |
| They don't know a container is held at customs until the customer complains | Demurrage, storage, chargebacks, lost account | Reactive, if at all |
| They can't tell their customer anything useful | Every shipment is a "please hold" email | Manual copy-paste |
| They can't prove what they told the customer | Disputes go to the carrier and they lose | WhatsApp scrollback |
| They don't know which carrier to blame | They renegotiate on anecdote | Nothing |

**None of that is a map problem. All of it is an exception-and-communication
problem.**

### The positioning

> **TrackSphere is the exception engine and customer-communication layer for
> mid-market freight forwarders.**
>
> It watches every shipment across every carrier, tells your team exactly what
> broke, what it will cost you, and what to do next — and tells your customers in
> their own language, on WhatsApp, under your brand. Your customers never see
> TrackSphere. Your competitors' customers see theirs.

Four things make that defensible, in this order:

1. **The exception engine.** Not "here is a red dot." *Here is a container that
   has been at Rotterdam for 6 days against a 2-day norm, that is 3 days past
   contractual free time, whose customer is BioMed Pharma — here is the document
   that is probably missing, here is the one-click action, here is who owns it.*
2. **WhatsApp as the primary channel.** In every target market on earth
   WhatsApp is the actual customer channel, it costs a fraction of SMS, and its
   open rate is an order of magnitude higher. Your feature sheet files this under
   "Growth". It is the product.
3. **White-label as a sales weapon, not a feature.** When a forwarder shows their
   own customer a TrackSphere-branded page, you have not won that account, you
   have been exposed as a vendor in their customer relationship. White-label is
   the difference between selling a tool and selling infrastructure.
4. **Exception closure as a data moat.** Every resolved exception with a tagged
   root cause trains carrier scorecards and calibrates ETA. In 24 months you have
   the only real dataset on how Maersk actually behaves on the
   Douala→Rotterdam lane. That is not copyable.

### The three loops

Everything in the product should serve one of these. If a screen serves none of
them, it is decoration.

```
OPS LOOP     event → detect → explain cost → assign → act → resolve → learn
                    ▲                                              │
                    └──────────── carrier scorecard, ETA ─────────┘

CUSTOMER LOOP  track → opt in → WhatsApp milestone → arrive → share
                        ▲                                 │
                        └──────  fewer "where is my order?" calls

GROWTH LOOP    exception → resolved faster → forwarder renews → adds carrier
                → more data → better detection → fewer exceptions
```

---

## 3. The audit

Severity: **P0** corrupts data or loses trust · **P1** blocks revenue or enterprise
sales · **P2** materially degrades daily use · **P3** polish.

### 3.1 Correctness and data integrity

**P0 — Status can regress on out-of-order webhooks.**
`internal/shipments/service.go:109-121`. `newStatus` is assigned whatever the
carrier sent, and the `UPDATE` writes it unconditionally. There is no transition
matrix and no monotonic guard. Carriers absolutely deliver out of order and
retrospectively correct themselves. A late-arriving `IN_TRANSIT` scan for a
container you already marked `DELIVERED` will **un-deliver it**: the status
flips back, `delivered_at` stays set (it is only ever set, never cleared —
`service.go:118`), and now you have a permanently incoherent record that will
generate false "past ETA" alerts forever via `rules.go:82`.

Note that `occurred_at` *is* stored alongside `received_at` and is never used for
anything. You already have the data you need to fix this and you are not using it.

**P0 — The "past ETA" rule fires forever once it fires.**
`internal/workers/rules.go:82-89` raises a `delay` alert when `now > eta` and the
shipment is not delivered. The partial unique index (`alerts_open_uniq`) stops
duplicates while the alert stays *open*, so this is deduped — but there is no
periodic re-evaluation and no resolution on delivery. An SLA-breach alert opened
at day 1 of a late shipment stays open at day 90 even after the customer gets
their container, poisoning every "unresolved exceptions" number you will ever
show. Exception state must be **derived and reconciled**, not just appended.

**P0 — The exception engine is a `switch` on a string.**
`internal/workers/rules.go:53-91`. Four rules, hardcoded, identical for every
tenant, unconfigurable, not versioned, not auditable. Your sheet promises *"notify
me if any shipment has no update for 48 hours"* and *"alert when ETA changes by
more than 3 days"*. Neither is expressible. Both are the rules customers actually
ask for. See §5 for the redesign.

**P0 — There is no time-based detection at all.**
Every rule is event-triggered. This is the deepest structural gap in the product.
The most common and most expensive exception in freight is **nothing happening**:
a container sits at a terminal for six days because nobody scanned it, the vessel
missed a transhipment connection, or the customer's paperwork was never approved.
There is no event. No rule fires. The shipment is invisible to your entire
detection system. You need a scheduled sweep, a dwell-time model, and expected
milestones. Today the data model cannot even express "sitting at customs for 6
days against a 2-day norm" — `shipments.status` is one flat mutable field
(`internal/model/model.go`), so there is nowhere to record a per-milestone
timestamp.

**P1 — The list view silently truncates.**
`web/src/app/shipments/page.tsx:33` hardcodes `limit: '50'`. `meta.total` is
displayed on line 48 — so the UI cheerfully tells the user "312 total" while
rendering 50 rows, with no pagination, no cursor, and no way to reach the other
262. A user who searches for a specific tracking number that happens to be
shipment 2,900 will conclude your platform lost it.

**P1 — Offset pagination on a live, constantly-mutating table.**
Rows shift between pages as webhooks land, so users see duplicates and skipped
records. Needs keyset/cursor pagination on `(occurred_at, id)` or a stable sort.

**P1 — Search fires a request per keystroke.**
`web/src/app/shipments/page.tsx:34` puts the raw `search` string straight into the
query key. No debounce, no `useDeferredValue`. Typing a 12-character tracking
number issues 12 queries, 11 of which match nothing. Combined with no rate
limiting (§3.2) this is a self-inflicted load problem.

**P1 — SSE events cause a refetch storm.**
`web/src/lib/live.ts:36-43` invalidates `shipments`, `dashboard`, and `alerts` on
**every** `shipment.updated`. A port-congestion webhook affecting 400 shipments
produces 400 events, which produce up to 1,200 refetches, coalesced only by
React Query's own 15s `staleTime` (`providers.tsx:12`) — which does not help,
because invalidation bypasses staleness. The dashboard will lock up during exactly
the incident it exists to show you.

**P2 — ETA has no history, so its quality is unknowable.**
`internal/workers/notify_eta.go` recomputes an ETA in place. Nothing records what
you predicted or what actually happened. You therefore cannot compute prediction
error, cannot show confidence, cannot calibrate, and cannot honestly market the
number. You are flying blind on the single most-trusted and most-likely-to-be-wrong
value in the product.

### 3.2 Security and abuse

**P0 — The public portal is an unauthenticated, unrated, enumerable endpoint.**
`GET /api/v1/track/{trackingNumber}`, self-documented as a gap at
`architecture.md:103`. This is worse than the doc implies. Tracking numbers are
**low-entropy and sequential** per carrier — a Maersk container number is
`MRKU` + 6 digits + a check digit. A human can walk the entire space for a
customer in minutes, and so can a script. Consequences:

- **Data exfiltration**: a competitor enumerates your customers' shipment
  histories, origin/destination pairs, and ETAs.
- **Availability**: unauthenticated DB queries, no limit, no cost control.
- **Reputational**: a leaked tracking list is a leaked customer list.

This is the one P0 that should block a public launch. Needs per-IP and per-IP-hash
token-bucket limits, a non-enumerating error path with constant-time behaviour,
and a CAPTCHA-after-threshold on the lookup form.

**P1 — No security headers on the web tier.**
No CSP, no HSTS, no `frame-ancestors`, no `Referrer-Policy`. For a product that
embeds a tracking widget on *customer* domains, `frame-ancestors` is not optional
— you will need a deliberate, tenant-scoped framing policy, which is itself a
tenant-isolation surface. Add the headers, and add a framing test.

**P1 — No CSRF token.** `architecture.md:103` correctly notes `SameSite=Lax` plus
JSON-only parsing mitigates this, and that is *mostly* right — Lax does block
cross-site POST. But the mitigation is implicit and one config change away from
regression. Make it explicit and tested.

**P1 — No rate limiting anywhere, including auth.** Login, registration, MFA
verify, and webhook ingest are all unbounded. Credential stuffing on
`/auth/login` is unthrottled; `auth_login.go` has uniform errors (good) but
uniform errors are not throttling.

**P1 — No audit log for operator actions.** `webhook_inbox` audits *inbound
carrier* traffic only. There is no record of who resolved an alert, who edited a
shipment, who changed a status, who exported customer data, who viewed a
document. Your sheet promises "Full audit log." For enterprise, this blocks the
deal. For day-to-day, it blocks dispute resolution — the forwarder's single most
common fight is "you told my customer it had delivered," and right now you have
no answer.

**P2 — No key rotation tooling.** Own secrets today. Documented gap. Fix before
the first enterprise customer asks, which they will.

### 3.3 UX, accessibility, and craft

This section is blunt because the current UI would not survive a design review.

**P0 — The dashboard is a snapshot with five integers.**
`web/src/app/page.tsx:47-51` renders `StatTile` × 5 — raw counts, no trend, no
time context, no denominator. Your own prototype
(`design/prototypes/ai_studio_code (33).html:440-471`) has on-time rate, trend
arrows, deltas, and a live clock. **The prototype is better than the product.**
That is the finding: the design intent was abandoned in implementation. A count of
`143` unresolved exceptions is not information — it is a number. Ops needs
"143, up 12 since this morning, 8 of them critical and 3 past SLA."

**P0 — There is no live fleet map on the dashboard.**
`ShipmentMap` exists but is only mounted per-shipment
(`shipments/[id]/page.tsx:94`). The ambient "where is everything right now" view
is *the* control-tower gesture and it is missing from the main screen. Ship
prototypes had it. App does not.

**P0 — The map doesn't even draw a route.**
`shipments/[id]/page.tsx:31-40` maps every geocoded event to `kind: 'event'` and
nothing else. `ShipmentMap.tsx:55` branches on `origin`/`destination` — dead
code — and **no line is ever drawn between points**. So the "route map" is a
scatter of unrelated dots, and the origin and destination of a shipment are
absent from it. Also: OSM raster tiles have no usage policy compliance for a
commercial product, and the default view is hardcoded `center: [8, 10], zoom: 1.5`
(`ShipmentMap.tsx:38-39`), so a European lane loads the whole Atlantic.

**P0 — The exception queue does not exist.**
The `alerts` table, the `/api/v1/alerts` list, and a resolve endpoint
(`alerts.go`) are the entire feature. There is no queue, no assignment, no
triage, no SLA clock, no root-cause tagging, no internal notes, no bulk actions.
The dashboard lists alerts as a read-only sidebar
(`web/src/app/page.tsx:84-112`) where each one is a link to a shipment page. This
is the core of the product's thesis and it is unbuilt.

**P1 — Notifications are never delivered to a human.**
`notifications` is written by `notify_eta.go` and read by nothing. No email, no
SMS, no WhatsApp, no push, no in-app inbox, no preferences model, no unsubscribe,
no delivery status, no bounce handling. The entire "Customer communication"
section of the feature sheet is aspirational.

**P1 — Empty and error states are conflated.**
`web/src/components/ui.tsx:70-76` — `Empty` is a dashed box. It is used for
"no data" *and* for 404s (`shipments/[id]/page.tsx:55`). A user who mistypes an
ID and a user with an empty fleet get the same visual. Empty states are the
highest-leverage UI in B2B software — they are where activation is won or lost —
and right now the first-run experience is a dashed rectangle saying
"No shipments yet — create your first one" next to a button.

**P1 — No loading, error, or optimistic states anywhere.**
Every query renders either data or the literal string "Loading…"
(`RequireAuth.tsx:19`, `shipments/[id]/page.tsx:62`). There are no skeletons, so
the dashboard flashes and shifts (CLS on every navigation). There is no
optimistic update and no toast anywhere in the codebase — creating a shipment
makes the form silently vanish with zero confirmation
(`shipments/page.tsx:60-66`). If the create failed you would not know.

**P1 — The app is desktop-only.**
`Shell.tsx:23` — the sidebar is a hard `w-64` with `shrink-0` and no responsive
behaviour. On a 375 px phone it consumes 256 px of 375. The shipments table
(`shipments/page.tsx:90`) has no horizontal scroll and no card fallback. Ops agents
work from phones on the dock. Your sheet claims "Web · Mobile · API."

**P1 — Accessibility failures, systematic.**
- No skip-to-content, no landmark elements, no `<main>` labelling.
- Icon glyphs `◧ ▣ ◎` (`Shell.tsx:9-12`) are unlabelled glyphs — meaningless to
  a screen reader, and they are not icons, they are Unicode dingbats.
- Alert severity is encoded **by border colour alone** (`page.tsx:90-96`) with no
  text or icon equivalent. Colour-only encoding is a WCAG 1.4.1 failure.
- No `aria-live` region, so SSE-driven updates are entirely invisible to assistive
  tech — the "live" product is silent for them.
- No focus management: the create-shipment form appears and vanishes with focus
  left on `<body>`.
- No keyboard navigation in the shipments table, no visible focus rings
  (`focus:outline-none` appears in `CreateShipmentForm.tsx:37` and
  `shipments/page.tsx:73,79` with no replacement).
- Color contrast: `text-slate-400` on white for secondary text
  (`ui.tsx:80`, `Timeline.tsx:25`) fails WCAG AA.

**P2 — Time and locale handling is naive and will generate support tickets.**
`toLocaleString()` with no timezone, no relative time, no unit formatting, no
locale (`Timeline.tsx:20`, `ui.tsx`, `page.tsx:83`). An ops team in Lagos
coordinating a Rotterdam dwell and an American customer needs: canonical
timestamps with a visible timezone, relative time ("6d ago"), the ability to see
the counterparty's local time, and correct pluralisation. Nothing here is
localisation-aware at all — `lang="en"` only, no i18n, no message catalogue. The
target market is multilingual and freight is global.

**P2 — The public portal contradicts the architecture and the promise.**
- It is `'use client'` (`track/[trackingNumber]/page.tsx:1`), so it gets **no
  SSR**. ADR 0001 lines 17-19 names SSR for the public portal as a primary reason
  for choosing Next. The implementation defeats the documented decision. The
  customer-facing page — the SEO and first-paint surface — is client-rendered.
- Metadata is static in `layout.tsx:9-12`; there is no per-shipment title, no OG
  image, no `robots` control. Each tracking page is undifferentiated in search.
- It is branded **TrackSphere**, hardcoded (`page.tsx:43-46`), directly
  contradicting the white-label promise. Your customer's end customer sees your
  name. That is the single most sales-damaging line in the frontend.
- No QR code, no multi-shipment lookup, no share, no notify-me, no webhook for
  "customer is watching this page" — all promised or implied, none present.

**P2 — Two divergent design systems are in the repo.**
`design/tracksphere_feature_list.html` uses Playfair Display + DM Sans + a blue
accent (`#1a3de4`) with sharp 6 px radii. The prototypes and the app use Plus
Jakarta Sans + JetBrains Mono + orange (`#FF6B00`) with 24 px radii. One of these
is your brand and the other is a dead end. It must be decided explicitly, in one
place, with tokens — currently `globals.css` mirrors the app and the sheet is
orphaned.

**P3 — No dark mode**, despite a deep-navy brand identity that begs for it, and
ops desks run dark.

### 3.4 Behavioural and psychological design

This is the layer that decides whether people keep the product open at 2am. It is
also the layer the current design is worst at, because it was designed as a
data display rather than as a decision-support system.

**P0 — Alert fatigue is architecturally inevitable.**
`service.go:134-144` enqueues `shipment.evaluate` and `shipment.notify` on
**every single ingested event**, with no filtering by significance. A single
port-congestion webhook touching 500 shipments raises 500 alerts. The partial
unique index prevents duplicates *per shipment per kind*, which is the right
instinct, but it does nothing about volume. Operators will be buried inside a
week, and alert fatigue is the documented number-one cause of ops teams abandoning
visibility platforms. **This is the most important single issue in this
document.** See §6.

**P0 — Severity is decorative; there is no triage logic.**
`info` / `warning` / `critical` exist, but every path treats them identically:
same queue, same list, same (nonexistent) delivery. An `ETA_REVISED` info alert
and a `CUSTOMS_HOLD` critical alert are the same object with a different colour.
In a real system, `info` should not generate an interruption at all. It should be
a line in a timeline.

**P0 — Nothing is owned, so nothing gets done.**
Alerts have no assignee, no due date, no status beyond `open`/`resolved`, no
notes, no root cause. Diffusion of responsibility guarantees that in a team, *no
one* works the alert — not because they are lazy but because "the alert" belongs to
the team, and unowned work is invisible work. The resolve button
(`alerts.go`) is the only affordance, which invites the worst possible behaviour:
clicking through 12 alerts in 20 seconds to make the number go down. That
destroys the only metric you have.

**P1 — Trust collapse is baked into the ETA display.**
The ETA shown is a heuristic (`notify_eta.go`) rendered in the same
`font-mono font-semibold` as a carrier-published ETA. The UI does not distinguish
"Maersk told us this" from "we guessed." The first time your estimate is wrong by
a week, every operator stops believing every number on the screen — including the
ones that are right. **Provenance is not polish; it is the foundation of
credibility.** Every predicted value needs a source and a confidence.

**P1 — Risk is invisible in the primary view.**
The shipments list sorts by nothing and encodes status as a flat enum. A shipment
at 120% of its expected transit time and one at 105% both read `in_transit`. The
single most decision-relevant number — *how much trouble is this shipment in* —
does not exist anywhere in the product. Ops do not want a status list; they want
a **to-do list ordered by consequence**.

**P1 — Timezone-blindness in a global product.**
Every timestamp renders in the browser's local zone with no zone shown. For a
team coordinating ports across continents this is a correctness bug with a
trust cost. The tenant has a home timezone; the user may have a working timezone;
the counterparty has theirs. All three matter and none are modelled.

**P1 — There is no onboarding, so time-to-first-value is undefined.**
Register → empty dashboard → dashed box → "+ New shipment" → a form with a
hardcoded `carrier: 'maersk'` dropdown of 4 modes and no help
(`CreateShipmentForm.tsx:13`). Nothing teaches webhook setup, nothing shows what
a good setup looks like, nothing imports existing shipments. A new user has no
path to their first useful insight and will not find one. Activation will be
near zero, and no amount of product quality downstream can fix that.

**P1 — The customer-facing loop has no hook.**
Customer tracks, sees a status, leaves. No opt-in, no notify-me, no share, no
return visit. The forwarder pays you to reduce "where is my order?" calls, and
your product does nothing to reduce them. The WhatsApp opt-in is the single
highest-ROI feature in the entire backlog and it is currently unticked.

**P2 — Healthy state and crisis state look identical.**
The prototypes pulse red dots at anomalies
(`ai_studio_code (33).html:518-519`). This is alarm theatre: at steady state it
manufactures anxiety, which erodes attention when a real critical event arrives.
A control tower should be able to look **quiet**, and get loud only when it means
something. Restraint is a design feature.

**P2 — No closure feedback, so the work feels futile.**
Resolving an alert produces nothing. No root-cause capture, no carrier
attribution, no visible contribution to any metric the operator will ever see.
The loop `detect → act → resolve → learn` is missing its final clause, so the
operator gets no evidence that their effort mattered. That is the intrinsic
motivator, and it is missing.

### 3.5 Architecture and scale

Your ADRs are disciplined and mostly correct. **I am not proposing a rewrite.**
I am proposing extensions with explicit triggers, plus four changes where there
is a real defect.

**Keep, with extensions:**

| Decision | Verdict | Extension |
|---|---|---|
| Postgres-only (0002) | **Keep.** Correct for this stage and this team | Add measured triggers for ClickHouse/Redis, and record them as *numbers*, not adjectives |
| PG job queue (0003) | **Keep.** The outbox is non-negotiable and correct | Add: sweep/scheduled job kinds, per-tenant concurrency limits, job priority, a dead-letter view in the UI |
| RLS isolation (0004) | **Keep. This is your best asset** | Extend: RLS test into CI as a hard gate; add `document`/`audit` tables under policy; frame-ancestors per tenant |
| LISTEN/NOTIFY + SSE (0005) | **Keep for now** | Add: heartbeat frames, `Last-Event-ID` resume, per-instance notification volume metric, and a *documented numeric trigger* to move to NATS (e.g. "sustained >5k notifications/sec/instance") |
| Compose on Hetzner (0006) | **Keep** | Add: tested restore runbook, warm standby, and the reverse proxy that white-label will require anyway |

**Change — four things:**

1. **Add a read-model projection.** Every list view and every dashboard tile is a
   live aggregate over the write tables. That is fine at 6 shipments (your seed
   data) and unacceptable at 50,000. The dashboard issues 5 independent counts;
   the list issues `count(*)` plus a page. Introduce
   `shipment_current` — one row per shipment carrying status, risk score, open
   exception count, last event at, ETA + ETA provenance, customer tier — maintained
   transactionally by the ingestion path. List views and dashboards then read one
   narrow, pre-aggregated, index-friendly table. This is the single change that
   makes the product scale without adding a datastore, and it is the thing that
   makes the "risk score" in §7.2 possible at all.

2. **Event coalescing before the browser.** `live.ts` must not invalidate on every
   event. Buffer, debounce (250–500 ms), and coalesce by entity before
   invalidating; additionally ship the *changed entity* in the SSE payload so the
   client can patch a single row instead of refetching a list. SSE is an
   accelerant (ADR 0005 says so correctly) — the current implementation lets it
   become the bottleneck.

3. **SSE hardening.** No heartbeat (idle connections get reaped by proxies), no
   `Last-Event-ID` resume (every reconnect refetches everything), no `retry:`
   hint. Add `: ping` every 15 s, a monotonic `id:`, and resume support. Also:
   every API replica holds a LISTEN connection and receives *every* tenant's
   traffic to filter locally. Fine at 3 replicas; document the ceiling and the
   sharding option (channel per tenant-shard) before it bites.

4. **Keyset pagination everywhere**, and add a trigram/`pg_trgm` index for
   search. `ILIKE '%x%'` is a sequential scan; it will be the first thing to fall
   over at 50k rows and nobody will notice until a customer files a bug.

**Missing architectural capabilities, in build order:**

- **No scheduler.** The only periodic work is `reapStuck`, in-process. You need
  a durable, leased, single-flight sweep for: staleness detection, dwell-time
  breach detection, ETA recomputation, alert auto-escalation, and digest
  assembly. Implement it as a `jobs` kind claimed with a lease so N replicas do
  not duplicate work. Do **not** use per-replica tickers.
- **No entitlement layer.** `Tenant.plan` is a free-text string
  (`web/src/lib/api.ts:73`) with nothing enforcing it. Your pricing is decorative
  until quotas are real. Quota enforcement is also a *growth* lever — hitting a
  limit is a conversion moment, and it should be a graceful in-product wall, not
  an error.
- **No API keys, no outbound webhooks.** The two highest-ARR surfaces in the
  entire product (BYO integration, and ecosystem pull) are entirely absent. Both
  promised in the sheet, neither exists.
- **No observability.** Structured logs and nothing else. No metrics, no traces,
  no error tracking, no dashboards, no SLOs, no alerting on your own service. You
  cannot honour a 99.9% claim and you cannot debug a customer's "it was slow on
  Tuesday" without it. This is a P1 for enterprise and a P0 for your own
  operational sanity.
- **No feature flags.** You will need to ship the exception engine, the read
  model, and the new dashboard to a subset of tenants. Retrofitting flags into
  `db.WithTenant` call sites is painful; building them in now is cheap.
- **Testing depth is thin where it matters most.** Go unit tests cover crypto,
  TOTP, rules, ETA, signatures — good. But: `scripts/rls_test.sql` is a manual
  script, not a CI gate, and it is your single most important invariant. There
  are **no frontend tests at all**, no integration tests against real Postgres,
  no load test, and no contract test binding the OpenAPI spec to the handlers.
  `openapi.yaml` is a hand-maintained document that is already drifting (it does
  not document the alerts `status=all` value, and there is no
  `components/schemas` section at all, so most `$ref`s are dangling).

### 3.6 Claims audit — stop saying these

The feature sheet is going to be read by customers. Several claims are not
supportable today, and each one is a landmine in a security review or a
procurement conversation. Fix the sheet, not the product, for these:

| Claim | Reality | Action |
|---|---|---|
| "Automated email notifications — **Core**" | `notifications` table is written and never read. Nothing is sent. | Re-tier to "in build" until a sender ships |
| "Public REST API with API keys — Growth" | No API keys exist at all | Re-tier |
| "Outbound webhooks — Growth" | Not implemented | Re-tier |
| "Full audit log" | Only carrier webhooks are audited, not operator actions | Re-tier or build it in Wave 1 |
| "AES-256 encryption at rest — military-grade" | This is disk-level infrastructure, not a feature you ship or claim. Saying it invites a question you cannot answer | Delete. Replace with the real, defensible claim: app-layer AES-256-GCM for secrets, provider-managed volume encryption, and stated regions |
| "99.9% uptime SLA" | No SLOs, no monitoring, no alerting, single host, no tested restore | Remove until Wave 3 |
| "GDPR deletion within 72 hours" | No deletion workflow, no data map, no ROPA, no DPO, no sub-processor list, no DPA | Remove. Then actually build it in Wave 3 |
| "SOC 2 Type II certification roadmap" | Fine as a statement of intent — keep, but do not use as a trust badge | Keep as roadmap only |
| "Encrypted at rest and in transit" blanket claim | True-ish, but unevidenced and unversioned | Narrow to specifics with evidence |
| "Four built-in roles: Admin, Ops Manager, Ops Agent, Customer" | Three exist: `owner`, `admin`, `member` (`web/src/lib/api.ts:67`). There is no Customer role and no RBAC model at all — roles are a string column | Fix the sheet now; build real RBAC in Wave 3 |
| "Web · Mobile · API" | Web only, and desktop-only at that | Fix now |
| "Natively integrated with Maersk, MSC, CMA CGM, DHL, FedEx, UPS, Evergreen" | Zero carrier integrations. You accept HMAC webhooks | **Highest-risk line on the page.** Remove or implement a sandbox connector for one carrier |
| "Configurable alert rules" with thresholds | A `switch` with 4 hardcoded rules | Re-tier, build in Wave 1 |
| "AI-powered ETA prediction — ML model, confidence score" | A heuristic, no persisted history, no confidence | Remove "ML". Say "estimated", and add provenance everywhere |
| "Interactive world map… vessel positions, flight paths" | Scattered dots, no route, no live positions | Fix now |
| "Cold chain / IoT" | Nothing | Re-tier |
| "Automated daily backups, 30-day retention, PITR" | `deploy/` mentions pgBackRest/WAL-G as a plan; verify it is actually configured and *restore-tested* | Verify, then keep or remove |

**The principle:** every claim on that sheet must map to something you can demo,
or to evidence you can hand over. Right now roughly a third of it cannot. A sales
sheet that over-promises does not win the deal — it loses the deal in week three
of the security review, after you have spent the credibility.

---

## 4. The product, redefined

### 4.1 Information architecture

Organise around **jobs to be done**, not around database entities. Current nav is
three items (`Shell.tsx:8-12`); the product needs seven.

```
CONTROL TOWER      Live fleet map · exception feed · KPIs with trends · risk summary
EXCEPTIONS    (NEW) Triage queue · assignment · SLA clocks · root cause · bulk actions
SHIPMENTS          Risk-sorted list · search · bulk edit · documents · events
ANALYTICS    (NEW) On-time performance · carrier scorecards · lane analysis · exports
CUSTOMERS    (NEW) Recipients · notification preferences · contact history
SETTINGS           Carriers · webhooks · API keys · team & roles · branding · billing
```

`/track` stays completely outside this shell — it is the customer's surface, on
the tenant's brand, with no TrackSphere chrome whatsoever.

### 4.2 The two screens that matter

**The Control Tower** answers, in one screen, in this order: *Is anything wrong?
Where? How much is it costing me? What should I do first?*

- **Ambient layer** (low density, high signal): world map, all live shipments,
  colour = risk tier not status, exceptions pulsing only when genuinely critical,
  lane congestion visible.
- **Triage layer** (high density): the top 5 things requiring a human decision,
  each as one card carrying *what happened · why it matters · what to do · who owns
  it · the clock*.
- **Trend layer**: on-time rate, exception rate by cause, average resolution time,
  carrier score deltas — each with a comparison window and a direction.

A healthy control tower must be able to look **quiet**. Restraint is the design.

**The Exception Queue** is the product. An exception card must always answer seven
questions:

1. **What** happened, in plain language, not a code.
2. **Which** shipment, customer, and value at risk.
3. **Why it matters** — days past free time, demurrage exposure, SLA clock, the
   specific commercial consequence.
4. **Likely cause** — inferred, with the rule that fired shown.
5. **Recommended action** — one click, pre-filled where possible.
6. **Owner and SLA** — who, since when, breaching when.
7. **Customer impact** — is the customer already told, or do they not know?

**Batch triage** is not a nice-to-have, it is the actual workflow. Ops resolve
exceptions in groups: "these 12 are all missing commercial invoices at
Rotterdam." Select-similar → assign → notify affected customers → bulk resolve.
Designing for the single-card path guarantees you lose to a competitor who
designed for the batch path.

### 4.3 The list view — sort by consequence, not by enum

This is the highest-value single change in the UI. Replace status-sorted with
**risk-sorted**, using a score computed in the read model:

```
risk = w1·(days past expected dwell / expected dwell)
     + w2·(exception severity, recency-decayed)
     + w3·(ETA slip vs. original commitment)
     + w4·(customer tier weight)
     + w5·(declared value at risk)
     - w6·(customer already notified)
```

The list then reads as a work queue: *most consequential unresolved thing first*.
Every row shows its risk tier, its provenance, its owner, and its clock. This is
the difference between a database view and an operations tool.

---

## 5. The exception engine, redesigned

The current `switch` (`internal/workers/rules.go:53`) cannot express what the
sheet promises. This is the design that can.

### 5.1 Milestone model

Detection requires knowing *where a shipment should be*. Add:

- `shipment_milestones` — per shipment: `code`, `expected_at`, `actual_at`,
  `expected_dwell_hours`, `source`. Codes: `BOOKED`, `DEPARTED_ORIGIN`,
  `ARRIVED_TRANSIT`, `CUSTOMS_ENTRY`, `CUSTOMS_CLEARED`, `ARRIVED_DESTINATION`,
  `OUT_FOR_DELIVERY`, `DELIVERED`.
- Populate `expected_at` from a **lane profile** (`lane_profiles`: origin,
  destination, mode → per-milestone norms with p50/p90), which seeds cold and
  self-corrects as real data arrives.
- Carrier events map to milestones via a per-carrier mapping table, so
  `CUSTOMS_HOLD` from Maersk and from DHL both mean the same thing internally.

Now expressible: *sitting at customs 6 days against a p90 of 2*.

### 5.2 Declarative rules

A tenant-configurable, versioned rule stored as JSON, evaluated by a real
evaluator. Not code, so support can add a rule for a customer without a deploy.

```jsonc
{
  "id": "stale_no_scan",
  "name": "No carrier update",
  "enabled": true,
  "trigger": { "type": "sweep", "every": "30m" },          // NOT event-driven
  "scope":  { "modes": ["ocean"], "min_value": 5000 },
  "condition": {
    "all": [
      { "fact": "hours_since_last_event", "op": ">", "value": 48 },
      { "fact": "status", "op": "in", "value": ["in_transit", "at_customs"] }
    ]
  },
  "raise": {
    "kind": "stale", "severity": "warning",
    "title": "No scan in 48h",
    "explain": "Last event {location} at {last_event_at}. Lane norm is {lane_p50}h.",
    "recommend": "Contact carrier agent {lane_agent} or verify vessel {vessel} position.",
    "notify": ["ops_email"], "interrupt": false
  }
}
```

Fields available to conditions: `hours_since_last_event`, `dwell_hours`,
`expected_dwell_hours`, `dwell_ratio`, `eta_slip_hours`, `value_at_risk`,
`customer_tier`, `carrier`, `mode`, `lane`, `port`, `status`, `milestone`,
`doc_checklist_state`, `sensor_breach_count`, `blank_sailing_flag`.

Ship ~12 rules covering: no-scan staleness, dwell breach (per milestone), missed
transhipment connection, ETA slip over threshold, blank sailing, customs hold,
documentation gap (drives a checklist), vessel/flight off-schedule, repeated
delivery attempt, temperature/shock breach, `dead`-job symptom, and late
arrival after a prior exception.

### 5.3 Reconciliation, not append

Alerts must be **reconciled** on every sweep, not appended to:

- Rule no longer true → auto-resolve with `resolution: "condition_cleared"`.
- Severity changed → update, and if it *escalated*, re-notify; if it
  *de-escalated*, record the de-escalation without a new interrupt.
- Still true → age the alert, and escalate on SLA breach.

This kills the "alert from day 1 still open at day 90" failure in §3.1 and makes
every number on the dashboard trustworthy. It is also what makes the customer
promise true: when the exception clears, the customer gets told. That is the
trust flywheel.

### 5.4 Scheduling

Add a durable sweep job (`system.sweep`) claimed with a **lease** so N replicas
do not duplicate work, running every 1–5 min. It evaluates all time-based rules,
reconciles open alerts, recomputes risk scores into the read model, and assembles
digests. Time-based detection *is the product*; it gets a first-class scheduler.

---

## 6. Notification budget — the most important section here

The rule that makes or breaks retention: **an operator may never receive more
than N actionable interruptions per hour.** Everything else is a channel-managed
pull. This is a hard product constraint, enforced in code, not a guideline.

### 6.1 Severity → channel → interrupt

| Severity | Ops channel | Customer channel | Interrupts? |
|---|---|---|---|
| `critical` | Push + SMS, immediate | WhatsApp + email, immediate | **Yes**, once |
| `warning` | Email + in-app, immediate | WhatsApp, batched at local 18:00 | No |
| `info` | In-app only, digest | None, timeline only | No |

`info` must not be able to generate an interruption, ever. Codify it as a test.

### 6.2 Digest and quiet hours

Non-interrupting notifications roll into a **per-tenant, per-recipient digest**
(local-timezone aware): instant / morning / evening. Quiet hours per user.
Digest entries are deduplicated and grouped by *kind*, not by event, so 40
customs holds become one line: *"40 customs holds at Rotterdam — 12 shipments
affected"*.

### 6.3 The interrupt budget, enforced

- A global per-user per-hour interrupt ceiling (default 5). Beyond it, the most
  severe wins and the rest queue for the digest, with a visible "N more
  suppressed" line. Being told you were protected from noise is itself
  reinforcing.
- One interrupt per shipment per 6 h regardless of rule count.
- Per-rule cooldowns.
- Escalation is the *only* way to interrupt again, and it requires the alert to
  have been acknowledged and then breached.
- Every interrupt carries a **one-click resolve-or-mute** in the notification
  itself. If acting on an alert takes more than two taps from the push, it will
  not be acted on.

### 6.4 Customer notification, done properly

Per-recipient preference centre: channels, per-event-class opt-in, language,
timezone, digest-vs-instant. Consent captured with an audit trail (required for
marketing-adjacent messaging in most target jurisdictions). Delivery receipts
and status webhooks from the providers, so "the customer was notified" becomes a
verifiable fact in your UI. WhatsApp first: template approval flow, session
windows, and per-recipient opt-in/opt-out words.

---

## 7. UI/UX masterclass

### 7.1 Foundations to fix first

These are prerequisites; almost nothing above them can be done well without them.

1. **One design system, in code.** `shadcn/ui` + Radix primitives, composed into
   a TrackSphere token layer in `globals.css`. Kill the hand-rolled `Card` /
   `StatTile` / ad-hoc `<input>` styling, which currently repeats focus and
   disabled-state logic inconsistently across 5 files. This is what makes
   accessibility and dark mode tractable instead of a per-screen negotiation.
2. **Tokens for meaning, not for paint.** `--risk-critical`, `--risk-watch`,
   `--risk-clear`, `--eta-confidence-high/low`, derived from and contrast-checked
   against the surface tokens. Status colour is then a *system*, not a per-screen
   decision, and the accessibility tests become mechanical.
3. **A11y as a gate, not a review.** Keyboard navigation, focus management,
   `aria-live` for the realtime feed, non-colour encoding for severity, AA
   contrast enforced in CI, visible focus rings everywhere. This is table stakes
   for enterprise procurement and it is currently absent.
4. **Responsive from `Shell` down.** Collapsible sidebar with a mobile drawer,
   table → card fallback below `md`, and a layout that works at 375 px. Field ops
   are on phones on the dock.
5. **Real state components.** `Skeleton` (no layout shift), `ErrorState` with a
   retry (distinct from `EmptyState`), `EmptyState` with a *next action*
   (registration → connect a carrier → import a manifest → see your first
   exception, as a guided checklist).
6. **Toasts + optimistic updates** for every mutation, with rollback on failure.
7. **Dark mode**, plus a `prefers-reduced-motion` path and real focus-visible
   states.
8. **One time layer.** `Intl.DateTimeFormat` with an explicit tenant timezone on
   every timestamp, relative time alongside absolute, and a locale-aware number
   and unit layer. Then i18n: message catalogue, locale routing, RTL-ready, and
   translated customer-facing notifications and templates.

### 7.2 Screens, in build order

**Control Tower** — ambient map with risk-tiered, clustered markers and live
positions; the seven-question exception feed; KPIs with trend, comparison window,
and direction; a "what changed since I last looked" line. Replaces
`web/src/app/page.tsx` wholesale.

**Exception Queue** — filterable, sortable, assignable, batch-triageable, with
SLA clocks, root-cause taxonomy, and internal notes. New. This is the product.

**Exception detail** — the full narrative: timeline, dwell against lane norm,
documents checklist, recommended action, customer notification state, and a
one-click "resolve and tell the customer."

**Shipment detail** — fix the map (origin, destination, route line, live
position, port/airport context), add a **dwell vs. norm** chart, document
checklist, and risk breakdown.

**Shipments list** — risk-sorted, cursor-paginated, debounced search with
`useDeferredValue`, saved filters and views, bulk actions, column config,
CSV export.

**Public portal** — server-rendered, tenant-branded (custom domain, logo,
colours, fonts, copy), per-shipment metadata and OG image, live updates, QR code,
multi-shipment lookup, WhatsApp/email notify-me, share link, and a "reference
number" flow so customers can find their order without a tracking number.
It should be indistinguishable from the forwarder's own site. That is the goal.

**Onboarding** — a guided path to first value in under 10 minutes: connect or
simulate a carrier, import a manifest (CSV) or paste a tracking list, and watch
real exceptions appear. Include a **demo tenant** that is always populated, so
nobody ever evaluates an empty product.

---

## 8. Security and compliance hardening

Ordered by what blocks a deal.

1. **Rate limiting** on every endpoint, especially the public tracker — per-IP and
   per-token-hash buckets, with a non-enumerating failure path. **P0.**
2. **Operator audit log** — immutable, append-only, covering every state change
   by a human, with actor, tenant, entity, before/after, IP, user agent, and
   request ID. Surfaced in the UI and exportable for compliance. Also the
   dispute-resolution tool. **P1.**
3. **Entitlements and quotas enforced in the API**, not the UI — plus a
   graceful in-product upgrade wall at the limit.
4. **Real RBAC** — a permission matrix, not a three-value string column. Four
   roles as promised (Admin, Ops Manager, Ops Agent, Customer) plus per-tenant
   custom roles, enforced server-side on every handler and reflected in the UI.
5. **API keys** — hashed at rest, scoped, rotatable, with visible last-used and a
   usage dashboard. This is the enterprise integration door.
6. **Outbound webhooks** — per-tenant subscriptions, HMAC-signed, at-least-once
   with visible delivery attempts, exponential backoff, replay, and a dead-letter
   view.
7. **GDPR, actually** — a real data map, self-service export and erasure wired to
   the audit log, retention policies, DPA and sub-processor list, region
   documentation, consent records for customer messaging, and an age-of-data
   purge job. Stop claiming 72 hours until it is true.
8. **SSO** — OIDC first (minutes of work, covers Google/Okta/Azure), then SAML,
   with SCIM for enterprise. Just-in-time provisioning, domain capture, and
   enforced MFA for admins.
9. **Key rotation tooling** and secrets hygiene — versioned secrets, dual-key
   acceptance windows, documented rotation runbook.
10. **Security headers, CSP with nonces**, dependency scanning, and secret
    scanning in CI.

---

## 9. Observability — the prerequisite for everything else

You cannot operate what you cannot see, and you cannot sell what you cannot prove.

- **Structured logs** with `request_id`, `tenant_id`, `user_id`, and duration —
  already partially there, finish it and ship it to aggregation.
- **RED metrics** for every HTTP route and every job kind: rate, errors, duration
  histograms. Plus business metrics that matter: events ingested/sec, webhook
  signature failures, queue depth and oldest-job age, dead-job count, SSE
  subscriber count and notification throughput, alert precision, time-to-resolve.
- **Distributed tracing** with OpenTelemetry, trace ID propagated into logs and
  into the queue so one shipment's whole lifecycle is one trace.
- **Error tracking** with release tagging and source maps.
- **SLOs defined and published internally**: API availability and latency, ingest
  freshness (p99 time from carrier event to visible), notification delivery
  success. Alert on burn rate, not on thresholds.
- **Synthetic checks** on the three things that must never break: login, public
  tracking lookup, webhook ingest.
- **A dashboard your customer can see.** A public status page and a tenant-facing
  "platform health" view is a real differentiator for a trust-based product and
  costs almost nothing once the metrics exist.

---

## 10. Go-to-market and monetization

**The number one change:** make pricing *real* (entitlements, quotas, metering,
in-product upgrade walls), and align the tiers to the actual build order rather
than to a wish list.

- **Free tier, deliberately small.** 50 shipments/mo, one user, email only. It
  exists to be outgrown. Every limit is a conversion moment and must be a
  graceful, honest wall.
- **Starter** — the single-operator forwarder. Live tracking, portal, email,
  core exception engine.
- **Growth** — the sweet spot. WhatsApp + SMS, full exception engine with
  assignment and SLA, carrier scorecards, API keys, webhooks, the embeddable
  widget, scheduled reports, Shopify/WooCommerce. This is where the product is
  best and this is the plan to engineer toward.
- **Enterprise** — white-label, SSO, audit log export, ERP connectors, AI ETA,
  cold chain, carbon reporting, SLA, multi-region.

**Distribution:**
- The **embeddable widget** is the growth engine. Every forwarder who embeds it
  puts TrackSphere on a customer-facing surface and renews to keep it there.
  Build it early, not in phase 3.
- **API + webhooks** make TrackSphere infrastructure inside someone else's
  workflow. Infrastructure does not churn.
- **Outbound webhooks and a public status page** are trust multipliers in
  procurement.
- **A real public tracking page with per-shipment SEO** is a free acquisition
  channel for the forwarder, which is a reason for them to choose you.

**Trust is the sales weapon in this category.** Publish a real security page, an
honest status page, a DPA, a sub-processor list, and your actual architecture
decisions. The ADRs in `docs/adr/` are genuinely good material — most companies
cannot show work like that. Ship a public trust centre built from them.

---

## 11. The roadmap

Sequenced by **risk retirement and revenue proximity**, not by feature list order.
Each wave has acceptance criteria; a wave is not done until they pass.

### Wave 0 — Correctness and trust (do this first; it is small)

Fixes things that corrupt data or destroy credibility the moment volume arrives.

- Status transition matrix with a monotonic guard; use `occurred_at` to reject
  stale out-of-order events; clear `delivered_at` on legitimate regression.
- Per-milestone timestamps and the lane-profile seed data.
- Scheduled `system.sweep` with leases; dwell + staleness detection; alert
  reconciliation.
- ETA provenance and confidence on every predicted value, plus ETA history.
- Keyset pagination; `pg_trgm` search index; debounced search; remove the 50-row
  cap.
- Rate limiting everywhere, non-enumerating public tracker.
- Tenant + user timezone model and a single time-formatting layer.
- SSE heartbeat, `Last-Event-ID`, client-side event coalescing.
- `scripts/rls_test.sql` into CI as a hard gate. `go vet` + tests + web build +
  OpenAPI lint.

**Accept:** no test can regress a delivered shipment; a shipment with no scan for
48 h raises exactly one alert; the 512th shipment is reachable; the 900th
keystroke of a search does not hit the database; CI fails if RLS isolation
regresses.

### Wave 1 — Finish the product

- **The exception queue**: triage, assignment, SLA clocks, root-cause taxonomy,
  notes, bulk triage, detail view with the seven questions, dwell-vs-norm chart.
- **The Control Tower**: live fleet map with routes and live positions, risk-tiered
  clustering, exception feed, trending KPIs.
- **Risk scoring** into a `shipment_current` read model; risk-sorted list; saved
  views; bulk actions; CSV export.
- **Real notifications**: email sender, WhatsApp sender, SMS sender, the severity
  routing and interrupt budget from §6, digests with quiet hours, per-recipient
  preferences and consent, delivery receipts.
- **Onboarding**: guided setup to first value, manifest import, a populated demo
  tenant.
- **Portal rebuilt**: SSR, tenant branding, live updates, QR, multi-lookup,
  notify-me, share, per-shipment SEO/OG.
- **Design system + a11y + responsive + dark mode + i18n** per §7.1.
- **Operator audit log**; real error/empty/loading states; toasts and optimistic
  updates.
- **Entitlements and quotas** with metering and in-product upgrade walls.
- **API keys** and **outbound webhooks** with a delivery log.
- **Real RBAC** (four roles + permissions).
- **Observability**: metrics, tracing, error tracking, SLOs, status page.

**Accept:** an operator can go from "an exception appeared" to "customer
notified, root cause tagged, alert closed" without leaving the screen; the
dashboard survives a 500-shipment exception storm without dropping a frame; the
portal is a tenant's own brand and renders server-side; no user receives more
than the interrupt budget; every operator action is in the audit log.

### Wave 2 — Monetise and differentiate

- Billing, plans, trials, dunning, upgrade/downgrade, invoices.
- Carrier integrations: **one real carrier first**, done properly (their API,
  their semantics, their rate limits), then an adapter framework so the rest are
  configuration. Include a **sandbox/simulator** so new customers see value
  before a real carrier is connected.
- Outbound webhooks on the public API; developer portal and real API docs.
- Carrier scorecards and lane analytics; scheduled PDF/CSV reports.
- Document management: upload, checklist per shipment type, time-limited secure
  sharing, version history.
- White-label: custom domain, logo, colours, fonts, email sender, custom copy.
- Embeddable tracking widget.
- Shopify and WooCommerce order import.
- Carbon/ESG reporting (a real European shipper budget line — CSRD).

**Accept:** a new tenant can sign up, pay, connect a carrier, and see a real
exception in under 30 minutes unaided; a forwarder can embed branded tracking on
their own domain and never see the TrackSphere name; a customer can self-serve
from trial to paid.

### Wave 3 — Enterprise and moat

- SSO (OIDC, then SAML) and SCIM; enforced MFA; domain capture.
- ERP connectors (SAP, Salesforce, Dynamics).
- Cold-chain IoT ingest and excursion alerting.
- AI ETA with persisted prediction history, calibrated confidence, and a
  self-improving model — only defensible once Wave 0's ETA history is real.
- SOC 2 Type II: control evidence, policies, and the runbooks.
- Multi-region with a tested restore path and warm standby.
- Native mobile (or a genuinely good PWA) for field ops.
- Full GDPR programme: data map, ROPA, DPO, sub-processor management, regional
  residency.

**Accept:** a Fortune-500 shipper passes a security review; a wrong ETA is
measured, published, and improving; a full-region failure is a documented,
tested, timed recovery.

---

## 12. What I would *not* build

Discipline is what separates a unicorn from a feature pile. Cut these:

- **A better map.** Everyone has one. Spend the effort on the exception engine.
- **A native mobile app** before the web app works on a phone. A good PWA
  covers 90% of field use at 10% of the cost.
- **Your own carrier-by-carrier integrations** for more than a handful. Build
  the adapter framework, ship the top 3–5 carriers plus a simulator, and let
  webhook onboarding handle the long tail.
- **A general-purpose analytics warehouse.** Postgres plus a read model and
  materialized views covers the actual questions for a long time. ClickHouse on
  the ADR 0002 trigger, not before.
- **Microservices.** One Go binary is a feature at this stage. Split when one
  team is the bottleneck, not before.
- **Feature parity with CargoWise.** It is a 30-year company. You are not
  winning on breadth; you are winning on the exception engine and the customer
  conversation.
- **Anything on the feature sheet you cannot demo.** Delete it from the sheet
  (§3.6) rather than letting it become debt you owe a customer.

---

## 13. Success metrics

If these do not move, the work was not worth doing.

| Metric | Now | Target |
|---|---|---|
| Time to first value (signup → first real exception) | undefined | **< 10 min** |
| Time to first delivered customer notification | never | **< 60 s** from event |
| Exception precision (alerts an operator judged useful) | unmeasured | **> 85%** |
| Interrupts per operator per hour | unbounded | **≤ 5, enforced** |
| Median exception time-to-close | unmeasured | **< 4 h** |
| Detection lag for a stalled shipment | infinite | **< 2 h** |
| "Where is my order?" inbound contacts per 1,000 shipments | baseline unknown | **−60%** |
| Ingest freshness p99 (carrier event → visible) | unmeasured | **< 5 s** |
| API availability | unmeasured | **99.9%**, with published SLO |
| Trial → paid conversion | unmeasured | **> 20%** |
| Weekly active ops seats per tenant | unmeasured | **> 3** (multi-seat is the expansion signal) |
| On-time delivery rate improvement for customers | unmeasured | **+3–5 pts** in year one |

---

## 14. Do this next

In order. The first three are days, not weeks, and each one removes a class of
failure:

1. **Fix the status regression guard** and the `delivered_at` incoherence
   (`internal/shipments/service.go:109-121`). Add the test that proves it.
2. **Rate-limit the public tracker** and make its failure path non-enumerating.
   This blocks a public launch.
3. **Run the real carrier webhook simulator** through Wave 0's detection
   work — a fake Maersk that emits 500 events over 10 minutes. Everything above
   is designed against that test, and it will find every remaining bug cheaply.
4. **Replace the rules `switch`** with the declarative evaluator and the
   `system.sweep` scheduler (§5). This is the product's core and it cannot be
   retrofitted later without painful migrations.
5. **Build the `shipment_current` read model** and the risk score. Everything
   scalable depends on it.
6. **Build the exception queue.** The product does not exist without it.
7. **Fix the claims sheet** (§3.6) before the next sales conversation. Cheap,
   and it protects credibility you cannot buy back.
8. **Design system, a11y gate, responsive shell** (§7.1). Then rebuild the
   dashboard and the portal on top of it.
9. **Real notifications with the interrupt budget** (§6). This is the retention
   decision.
10. **Onboarding to first value in under 10 minutes.** Nothing downstream
    survives a user who never sees value.

---

## Appendix A — Findings index

| # | Severity | Finding | Location |
|---|---|---|---|
| 1 | P0 | Status regresses on out-of-order webhooks; `delivered_at` never cleared | `internal/shipments/service.go:109-121` |
| 2 | P0 | SLA-breach alerts never reconcile; poison every "open exceptions" metric | `internal/workers/rules.go:82-89` |
| 3 | P0 | Exception engine is a hardcoded 4-case `switch`; not configurable | `internal/workers/rules.go:53-91` |
| 4 | P0 | No time-based detection at all — a stalled shipment is invisible forever | `internal/workers/rules.go` (whole file) |
| 5 | P0 | Public tracker is unauthenticated, unrated, and enumerable | `internal/httpapi/public_handlers.go`, `architecture.md:103` |
| 6 | P0 | Notifications written to a table nothing reads; zero delivery | `internal/workers/notify_eta.go` |
| 7 | P0 | Public portal has no SSE — the demoed surface is not live | `Shell.tsx:19` (only caller) |
| 8 | P0 | Alert fatigue architecturally inevitable; no severity routing, no budget | `internal/shipments/service.go:134-144` |
| 9 | P0 | No ownership, SLA, or root cause on exceptions | `internal/httpapi/alerts.go` |
| 10 | P0 | ETA shown identically whether carrier-published or heuristic | `web/src/app/shipments/[id]/page.tsx:80-88` |
| 11 | P0 | Dashboard is 5 raw integers, no trend, no time context | `web/src/app/page.tsx:47-51` |
| 12 | P0 | No live fleet map on the dashboard | `web/src/app/page.tsx` (absent) |
| 13 | P0 | Map draws no route and omits origin/destination (dead code) | `web/src/app/shipments/[id]/page.tsx:31-40` |
| 14 | P0 | Exception queue does not exist | `web/src/app/page.tsx:84-112` |
| 15 | P1 | List view truncates at 50 rows while displaying the true total | `web/src/app/shipments/page.tsx:33,48` |
| 16 | P1 | Offset pagination on a live table | `internal/httpapi/shipments_handlers.go` |
| 17 | P1 | Search issues a request per keystroke | `web/src/app/shipments/page.tsx:34` |
| 18 | P1 | SSE refetch storm on event bursts | `web/src/lib/live.ts:36-43` |
| 19 | P1 | No audit log for operator actions | absent |
| 20 | P1 | No rate limiting anywhere, including auth | `architecture.md:103` |
| 21 | P1 | No security headers / CSP / framing policy | `web/` (absent) |
| 22 | P1 | No RBAC — three-value string column, not four roles as promised | `web/src/lib/api.ts:67` |
| 23 | P1 | Empty and error states conflated | `web/src/components/ui.tsx:70-76` |
| 24 | P1 | No skeletons, no toasts, no optimistic updates, no rollback | `web/src/app/shipments/page.tsx:60-66` |
| 25 | P1 | Desktop-only shell; table unusable on mobile | `web/src/components/Shell.tsx:23` |
| 26 | P1 | Systematic a11y failures (landmarks, focus, `aria-live`, contrast, glyph icons) | `Shell.tsx:9-12`, `page.tsx:90-96` |
| 27 | P1 | No onboarding; first run is an empty dashboard and a form | `web/src/app/register/page.tsx` |
| 28 | P1 | No customer-side retention loop; portal is a dead end | `web/src/app/track/[trackingNumber]/page.tsx` |
| 29 | P1 | No entitlement layer; pricing is decorative | `web/src/lib/api.ts:73` |
| 30 | P1 | No API keys and no outbound webhooks | absent |
| 31 | P1 | No observability, SLOs, or alerting | absent |
| 32 | P1 | No durable scheduler; only in-process `reapStuck` | `internal/queue/queue.go:173-198` |
| 33 | P1 | No frontend tests; RLS test is manual, not a CI gate; OpenAPI is drifting and has dangling `$ref`s | `scripts/rls_test.sql`, `docs/api/openapi.yaml` |
| 34 | P2 | Portal is `'use client'` — no SSR, contradicting ADR 0001 | `web/src/app/track/[trackingNumber]/page.tsx:1` |
| 35 | P2 | Portal hardcoded TrackSphere branding, contradicting white-label | `web/src/app/track/[trackingNumber]/page.tsx:43-46` |
| 36 | P2 | Timezone/locale-naive timestamps throughout | `web/src/components/Timeline.tsx:20` and others |
| 37 | P2 | No i18n; `lang="en"` only | `web/src/app/layout.tsx:16` |
| 38 | P2 | Two divergent design systems in the repo | `design/tracksphere_feature_list.html` vs `web/src/app/globals.css` |
| 39 | P2 | ETA has no history, so prediction error is uncomputable | `internal/workers/notify_eta.go` |
| 40 | P2 | No read model; every list and dashboard view is a live aggregate | `internal/httpapi/shipments_handlers.go` |
| 41 | P2 | SSE has no heartbeat, no `Last-Event-ID`, no `retry:` hint | `internal/realtime/hub.go` |
| 42 | P2 | No key rotation tooling | self-documented |
| 43 | P3 | No dark mode | absent |
| 44 | P3 | OSM raster tiles; commercial usage policy unaddressed | `web/src/components/ShipmentMap.tsx:31` |

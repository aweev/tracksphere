# Sprint close-out — reliability, amplification, motion & onboarding (2026-10-18)

Scope: Phases 0–3 of the mission file (`# TRACKSPHERE: THE "IT" PROJECT — MAKE I.md`),
Sprints A and B inclusive. Everything below is committed to `main` and verified by
the exact commands listed.

## Shipped

### Phase 1 — reliability
- Typed queue payloads (`internal/queue/jobs.go`), `map[string]any` fan-out gone.
  One regression caught during this pass: digest raises/emails were formatted from
  untyped keys and silently skipped fields — fixed by passing structs.
- Deep `/health`: DB ping + queue depth, plus per-tenant notification SLIs.
- Production migration gates: `AUTO_MIGRATE` is rejected in prod; restore must
  confirm `TRACKSPHERE_MIGRATION_BACKUP_DONE` (ADR 0005 discipline, applied).
- Concurrent query indexes migration `000033` (`queued_at DESC` partials).
- Quota enforcement returns real 429s counting *existing* rows (seats, API keys,
  webhooks) — not per-request counters.

### Phase 2 — amplification
- Exception engine verbatim payloads end to end; exception page renders server
  values 1:1 (grep-audited; no client-side recomputation of risk fields).
- Resolution verdicts persist (`resolution` on alerts); "False alarm" button in
  triage marks `false_positive` — car useful% honesty instead of gaming it.
- Ecommerce order → shipment mapping: mode per carrier code, dest join,
  auto-subscribe of order email with commerce consent audit entries.
- Real SLA escalation pages from the sweeper (severity-band titles; recruiting
  budget respected: page = interrupt only when critical).
- White-label custom brand color; `brandForTenantRow` returns the real CSS
  variables the portal consumes.

### Phase 3 / Sprint A — map + motion
- Risk delta count-up: `web/src/components/CountUp.tsx` — 250ms ease-out on
  change, instant first paint, disabled under `prefers-reduced-motion`; used in
  `RiskBadge`, dashboard chip, detail-page chip. Nothing else on the page animates.
- Route line rendered as a facts polyline (positions it can prove), not the
  spec's "route by percent" — that would have invented progress we don't have.
- Already in place from earlier passes: SSE with reconnect, marker easing,
  dwell halos, mobile strip, resolve-proof banner.

### Sprint B — onboarding + empty states
- Setup guide is 5 steps now (connect carrier → add shipment → publish portal →
  fire a real exception → resolve with cause); each step computed from DB state,
  never from click events. Dismissible per tenant; auto-hides when complete.
- First-run shipments empty state carries all three honest paths (manual create,
  carrier connect, webhook curl); exceptions "all clear" states the read-model
  freshness instead of a static "nothing here" (stale silence was a lie).

## Perf proof (build artifacts, measured 2026-10-18, Node v20.19.0)
- `next build` passes (previously blocked: WSL Node 18.19.1).
- Maplibre is an isolated lazy chunk: 1,003 KB raw / ~270 KB gzip, loaded only
  on routes that ask for the map (`dynamic` import, `ssr:false`).
- Core first-load chunks: 45–72 KB raw each (~157 KB gzip combined).

## Honest gaps
- Lighthouse was not run — no headless Chrome in this environment. The numbers
  above are from the build; run Lighthouse before quoting anything else.
- Duplicate root `package-lock.json` (empty stub, 90 B) made Next pick the wrong
  workspace root; stub deleted — `web/package-lock.json` is canonical.
- The escalate page send moved after commit; transaction never blocks on mail.

## Verification
- `go build ./... && go vet ./... && go test ./... -count=1`
- `/usr/local/go/bin/gofmt -l` → clean (CI gates this)
- `npm run typecheck` and `npm run build` in `web/`

Commits: `7238218` (phases 1–2 + count-up), `5b0ff12` (checklist carrier step),
pushed to `origin/main`.

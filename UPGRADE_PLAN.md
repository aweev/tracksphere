# TrackSphere Upgrade Plan

**Source:** `docs/POSTURE.md` (consolidated audit v2.0, 2026-10-21).
**Rule:** every item ships with its acceptance check run in the same commit.
"Verified, not assumed" — a claim without a greppable command is not done.
No `continue-on-error` in CI, ever.

Supersedes `REMEDIATION_BACKLOG.md` (folded in; file removed 2026-10-21 — its
P0 matrix drifted from code: P1-2 was shipped but still showed 🔴).

Status legend: `[x]` done · `[~]` partial (what's missing is named) · `[ ]` not started · `[?]` needs evidence before being believed

---

## Phase 0 — Hygiene & docs-in-sync ✅ DONE 2026-10-21

- [x] **0.1** Remove repo debris: duplicated manifesto (one copy without
      `.md` extension), stray agent-prompt file (preserved → `docs/MISSION.md`),
      `doc.md` stub, `bin/*.exe`, `logs/`.
      *Check:* repo root contains only tracked project files.
- [x] **0.2** Dead code: `httpapi.writeGaugeSimple`, `config.RecommendedDBPoolSize()`.
      *Check:* `go build ./... && go vet ./...` green (ran: BUILD-OK).
- [x] **0.3** Orphaned scripts removed: `create_github_issues.ps1`,
      `generate-types.sh`, `canary.sh`, `load-test.sh`;
      `docs/pgbackrest-setup.md` removed, references fixed in
      `deploy/docker-compose.yml` + `docs/RUNBOOK_RESTORE.md`.
      *Check:* `grep -r pgbackrest-setup` → zero hits; deploy docs reference
      pg_dump as the actually deployed path.
- [x] **0.4** Audits consolidated: `AUDIT.md` (superseded, 478 lines) +
      `AUDIT-2026-10-04.md` (corrupt — mid-sentence truncation + pasted tool
      artifact) → `docs/POSTURE.md`. Runbook/backlog/`SPRINT_CLOSEOUT`/MISSION
      pointers updated; `.gitignore` §-pointer updated.
      *Check:* `grep -rn "AUDIT-2026-10-04" --include=*.md` → zero hits.
- [x] **0.5** SLO.md rewritten to describe only metrics that are actually
      emitted; section 5 replaced by the deployed pg_dump path;
      runbook list states what exists.
      *Check:* every metric named in SLO.md exists in `internal/metrics` or
      `httpapi/metrics.go`.
- [x] **0.6** `docs/STAGING.md` migration-version reference fixed;
      `REMEDIATION_BACKLOG.md` folded here and removed.
      *Check:* no doc references a superseded migration number as latest.

## Phase 1 — Correctness (mechanical, no behavior change beyond fixes) `[ ]`

- [ ] **P1-C1** Restore `go.mod`: `go get golang.org/x/crypto@latest`, drop the
      1.25.0 downgrade. *Check:* `go test ./...` green on go 1.26 toolchain.
- [ ] **P1-C2** Fix hooks-after-return: `web/src/app/shipments/[id]/page.tsx`
      (`useState` after early returns; flips between error/loading →
      "Rendered more hooks" crash on React 19).
      *Check:* `npm run typecheck` + build; manual error-path render.
- [ ] **P1-C3** App route boundaries: `error.tsx` + `loading.tsx` (any render
      crash currently kills the whole authed app).
- [ ] **P1-C4** Dedupe migration 000022 (two files share the number).
      *Check:* `migrate --version` prints both filenames; add assertion that
      filenames are unique.
- [ ] **P1-C5** Client-side filtering lies: `web/src/app/shipments/page.tsx`
      sorts/filters `mode` over only the current 25-row page; `sort` state
      double-books `serverSort`. Move both server-side.
      *Check:* e2e asserts filtered counts == server counts.
- [ ] **P1-C6** Install Playwright as a devDependency (script + spec exist but
      dependency does not); run it in CI.
      *Check:* `npx playwright run` green in CI web job.

## Phase 2 — Observability (unblocks the rest) `[ ]`

- [ ] **P2-M1** Worker metrics listener: `cmd/worker` starts a `:9091`-style
      promhttp endpoint. All worker-incremented counts (`tracksphere_sweep_*`,
      notify/sends, digests) become scrapeable.
      *Check:* `curl localhost:9091/releases/metrics` from worker ≠ empty;
      compose exposes the port.
- [ ] **P2-M2** Wire or delete the 10 never-touched metric vars in
      `internal/metrics` (`HTTPRequestsTotal`, `HTTPRequestDuration`, SSE
      counters, `Jobs*`, `JobDuration`, `NotificationsAttemptedTotal`…).
      *Check:* repo-wide grep: every declared metric has ≥1 non-declaration
      reference.
- [ ] **P2-M3** Rewrite SLO.md against step 2's reality (SLIs exist ⇒
      expressions resolve against actual series).
      *Check:* `promtool check rules` on the inline rules.
- [ ] **P2-M4** Alert routing to exist (Alertmanager compose service + rules
      file), `TenantQueueDepthHigh`/`TenantDLQAging` wired to a delivery
      target. *Check:* alerts fire in staging against seeded data.

## Phase 3 — Reliability proof `[ ]`

- [ ] **P3-R1** `scripts/verify-restore.sh`: provision temp PG → restore latest
      backup → run `rls_test.sql` + `schema_test.sql` + e2e → emit RTO/RPO.
      *Check:* weekly CI job green; output recorded in
      `docs/RUNBOOK_RESTORE.md` drill log.
- [ ] **P3-R2** Tests for the zero-coverage load-bearing packages:
      `internal/sso` (JWKS/OIDC verify), `internal/realtime` (hub fan-out,
      tenant scoping), `internal/queue` (claim/lock/reap), sweep `Evaluate`
      engine (`DefaultRules`), `internal/notify` senders.
      *Check:* `go test ./...` covers them; no package below 60% lines of these
      five.
- [ ] **P3-R3** Bulkhead per carrier (P0-5 from old backlog): separate
      pool/timeout/breaker per carrier; carrier A outage must not delay B.
      *Check:* unit test proves isolation.
- [ ] **P3-R4** Runbook library: the 8 missing `RUNBOOK_*.md` files, each
      detection → diagnosis → mitigation → verification, linked from
      SLO.md alerts.
      *Check:* every alert annotation in the rules file names an existing doc.

## Phase 4 — UI/UX refresh [~]

- [~] **P4-U1** Known-good foundations already shipped: empty states with real
      proof, onboarding checklist (5 DB-computed steps), ⌘K palette, mobile
      card list + bottom nav, a11y attributes. **Missing:** theme server/client
      duality (persisted server theme never read on boot) — fix with Phase 1
      items.
- [ ] **P4-U2** Collapse `web/src/app/globals.css`: one `@theme` token set on
      `:root`/`[data-theme]`; delete duplicate media-query + `data-theme`
      blocking copies; delete the parallel `--app-*` palette (different accent
      hex, `#e76524` vs `#ff6b00`) and the ~700 lines of unlayered dark-mode
      patch selectors. Undefined-token classes (`hover:bg-accent-700-hover`,
      `text-accent-300`, `animate-in zoom-in-95`, stray `dark:`) die with it.
      *Check:* zero references to undefined tokens; visual pass on light+dark.
- [ ] **P4-U3** Primitive layer: `Button/Input/Select/Skeleton/Banner/Dialog`
      (three competing primary-button styles, 5× rebuilt one-time-secret
      banner, hand-rolled `animate-pulse` on 4 pages, `window.prompt` for
      saved views). Split `exceptions` (910 lines) and `settings` (664 lines).
      *Check:* `npm run build`; button/banner usage grep converges.
- [ ] **P4-U4** Density + typography: drop the 10/11px uppercase microtype to
      12–13px for body data; mono for tracking numbers only; unify token
      leakage (`bg-blue-100`/`text-orange-700` vs `bg-risk-*`).
      *Check:* no hardcoded palette classes in semantic risk/status spots.
- [ ] **P4-U5** Operator UX: optimistic mutations for triage actions, undo /
      multi-select resolve, command palette gains verbs (assign/resolve/go-to),
      time-range filters, restore `/status` reachable in nav (it is orphaned).
      *Check:* e2e covers the triage loop.
- [ ] **P4-U6** Enable ESLint (4 `eslint-disable` pragmas reference a missing
      tool); make knip's `continue-on-error` failure-blocking.
      *Check:* lint job runs clean; no unused exports unreported by knip.

## Phase 5 — CI hardening `[ ]`

- [ ] **P5-K1** Add gitleaks to CI (the leak class that actually cost this
      history). *Check:* CI red on intentionally planted secret.
- [ ] **P5-K2** Go coverage report in CI with a floor (see P3-R2).
- [ ] **P5-K3** Docker-compose build job (current CI never builds the images).
- [ ] **P5-K4** Integrate `canary.sh` / `load-test.sh` equivalents *when they
      have consumers* — do not resurrect dead scripts.

## Product (from the backlog, unchanged priority)

- [ ] **PD-1  Carrier adapters (top 10 lanes)** — the enterprise-gate item.
      Only the fake carrier works end-to-end.
- [ ] **PD-2  Onboarding-to-value < 10 min measuring + trust signals
      (DMARC/DKIM, branded sender)** — P0-7/P0-8.
- [ ] **PD-3  Plan/quota enforcement** — [~] quota 429s exist (2026-10-18
      sprint); end-to-end enforcement + `/billing` usage views unproven.
      *Check:* 429 on quota exceed in e2e.
- [ ] **PD-4  Custom domain** (CNAME verify → TLS → branded links) — P1-5.
- [ ] **PD-5  Daily digest at tenant hour + 2h escalation + weekly scorecard**
      — [~] digest engine exists (`internal/workers/digest.go`); schedule
      firing/evidence unproven. *Check:* cli/e2e produces one digest.

## Decisions needed (not code)

- [ ] **D1  History rewrite** for the 2026-10-02 leaked session tokens —
      all commit hashes change; coordinate before any public release /
      external due diligence (see POSTURE.md §3).
- [ ] **D2  MCP** (`internal/httpapi/mcp.go`, flag-gated off): keep-and-customer
      or delete. No consumer exists today.

---

## Execution order & gates

Phase 1 → 2 → 3 (behavior-safe, aggressively landable) → Phase 4 (only
user-visible change; lands last) → 5 (CI hardening continues opportunistically).
Every phase run: `scripts/go-gate.sh build`, `go test ./...`,
`web/ npm run typecheck && npm run build`, `scripts/e2e.ps1`, `scripts/dev.ps1
rls-test`.

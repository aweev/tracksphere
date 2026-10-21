# TrackSphere Engineering Posture

**Version:** 2.0 · 2026-10-21
**Supersedes:** `AUDIT.md` (2026-10-03) and `AUDIT-2026-10-04.md` — both removed.
The 10-03 file was materially stale; the 10-04 file was found **corrupt on disk**
(§4 ends mid-sentence at "pins both flags for exactly th" and a literal
`...[truncated 3885 chars]` tool artifact had been pasted into the file, so its
tail content is unrecoverable). Everything below that survives is carried
forward; anything else is re-derived from the current tree.
**Method:** every claim was verified against code (grep/trace), not inferred from
docs. Rules borne out by the audit history itself: two previous fixes shipped as
controls that provably did nothing while reporting success.

---

## 1. Verdict

The architecture is genuinely sound and was verified, not assumed:

- Transactional outbox + `FOR UPDATE SKIP LOCKED` queue, `pg_notify` fan-out,
  single-flight sweep lease, parameterised queries throughout.
- `FORCE ROW LEVEL SECURITY` behind a real non-superuser role
  (`tracksphere_app`), three mutually exclusive read branches, `db.WithTenant`
  as the only tenant door with named, transaction-local escape hatches.
- Security posture that survived a hostile audit: argon2id (64 MiB), uniform
  auth errors, session-token hashing, HMAC webhooks with 1 MiB cap,
  dual rate limiting, SSRF guards with regression tests, fail-closed production
  migration gating (double-fenced: env flag rejected in prod + backup-done gate).
- Migration safety: advisory lock, `statement_timeout=0`/`lock_timeout=30s`
  on the migration connection only, `-- migrate:no-transaction` contract.
- CI gates that are unusually rigorous: race tests, govulncheck, staticcheck,
  trivy, CRLF guard, gofmt clean-tree gate, real-Postgres RLS + schema +
  idempotent-migrate jobs, e2e with a live sweep-reconciliation probe.

What sits on top is **operationally immature**: the worker's metrics are
unscrapable, SLO.md describes metrics that do not exist, there is no
verify-restore automation, the most safety-critical Go packages have zero tests,
and the UI carries a legacy CSS layer it must outgrow. These are tracked in
`UPGRADE_PLAN.md` (repo root).

## 2. Fixed and verified in `cab6d65` (2026-10-04)

Kept as the record of the control failures that defined the audit standard
("silent success is worse than loud failure") — each fix verified by tests:

| # | Failure | Fix |
|---|---------|-----|
| 1 | GDPR erase deleted 0 rows, swallowed the error, returned `{"ok":true}` | `SetSystem`+`SetTenant` before DELETE, assert `RowsAffected()==1`, in-tx cascade verify from `pg_class`; migration 000019 dropped both unsafe `SECURITY DEFINER` functions |
| 2 | `auditShipment` used raw `pool.Exec` (no tenant ctx) → RLS rejected all rows, error discarded → audit trail always empty | `db.WithTenant`; log failures without failing request |
| 3 | Per-event fan-out delivered SMS to `pending`/`revoked` consent rows — bypassed double opt-in | filter `status='active' AND NOT digest_only` |
| 4 | `querySystem` set app flags in autocommit (lost) and released the conn before `Scan` → SSO-by-subject and Stripe plan sync were dead | `querySystemRow` scans inside an explicit transaction |
| 5 | `mfa/enroll` overwrote the TOTP secret with a bare session; SSO minted full sessions | enroll requires code-or-password when replacing an active factor, factor stays disabled until confirmed, other sessions revoked; SSO → challenge cookie → `/login?mfa=1` |
| 6 | SSRF: carrier `baseURL` unvalidated at write, fetched with sealed credential as bearer; webhook URLs were bare `https://` proxy | `ValidateOutboundURL` at both write sites + dial-time and redirect guards; 29 regression subtests in `httpclient` |
| 7 | Retention ran three FORCE-RLS tables unpinned on the pool → 0 rows, logged success, advertised as live in docs | global deletes pooled (non-RLS tables), RLS tables prune per-tenant |
| 8 | `statement_timeout='5s'` on the migration connection could abort the advisory lock and GIN builds | timeout regime above + no-transaction marker |

Adjacent fixes in the same pass: `mfa/verify` accepts the cookie-borne challenge;
bodiless `enroll` POST tolerated; public `/track` returned an `etaSource` field
the projection never included; client-side risk-weight recomputation collapsed to
one typed `riskBreakdown()` with no client-side weights; three `next build`-
breaking tsc errors; a versioning `Link` header naming a host that doesn't exist.

## 3. Debt carried (known and deliberate)

| Debt | Why it exists | Where tracked |
|---|---|---|
| **Session tokens/cookies in git history (2026-10-02)** | Fixed by `cab6d65` but the root-commit history was never rewritten/purged. Poison if the repo is ever published or used in due diligence | UPGRADE_PLAN P0-H: history rewrite — needs a *decisive* call (all hashes change) |
| **`go.mod` downgrade** | `go 1.26.0 → 1.25.0`, `golang.org/x/crypto v0.57.0 → v0.23.0` committed so a 1.25.0 toolchain without proxy access could build; x/crypto pin is unintended in the argon2id package | UPGRADE_PLAN P1-C1 |
| **Hooks-after-return crash site** | `web/src/app/shipments/[id]/page.tsx` `useState` after early returns — reported 2026-10-04, still live | UPGRADE_PLAN P1-C2 (ship before Phase 4 UX work) |
| **`app.system` is not a universal bypass** | Eight tenant tables (000012 seven + `sse_subscriptions` 000017) have no `app.system` branch in their policies. Any code that assumes system context reads zero rows — this exact failure mode produced items 1 and 7 above | Standing rule, enforced by `scripts/schema_test.sql` |
| **MCP server kept behind a flag** | `internal/httpapi/mcp.go` routed at `/api/v1/mcp` but off unless `TRACKSPHERE_MCP_ENABLED=1`; no named consumer | UPGRADE_PLAN decision item |

## 4. Current findings — 2026-10-21 sweep

New/standing findings from this pass (superseding both audit files' open items):

1. **Worker metrics are unscrapable.** Sweep, notification, and digest counters
   are incremented in `cmd/worker`, which starts no HTTP listener; promhttp is
   mounted only in the API's `/api/v1/metrics`. Per-tenant DB-derived gauges in
   the API exist but are scrape-hostile (`?per_tenant=true` session-scoped,
   rate-limited). P0.
2. **Half of `internal/metrics` is dead instrumentation.** ~10 of ~30 metric
   vars are declared and never incremented (`HTTPRequestsTotal`,
   `HTTPRequestDuration`, SSE counters, `JobsPending/Running/Dead`,
   `JobDuration`, `NotificationsAttemptedTotal`…) while SLO.md §4.1 advertises
   exactly these as "key metrics to emit". P0.
3. **No verify-restore automation.** `deploy/backup.sh` + `backup_runs` ledger +
   `statusz` degradation past 30h is good design; the P0-3 acceptance check
   (`scripts/verify-restore.sh`: restore → RLS + schema tests → RTO/RPO) does
   not exist. One handwritten drill row so far. P0.
4. **Test concentration risk.** Zero tests: `internal/notify`, `internal/sso`
   (OIDC verify chain), `internal/realtime` (SSE hub), `internal/circuitbreaker`.
   `sweep.go` (1,006 LOC engine) has only a window test. `httpapi` is ~6%
   covered. P1.
5. **Duplicate migration number 000022** (two files) — ordering works but
   `ParseVersion` collisions are a landmine. P1.
6. **UI debt** covered in Phase 4 of `UPGRADE_PLAN.md` (three
   stacked CSS systems in `globals.css:1-1155`, dead/undefined utility classes,
   no `error.tsx`/`loading.tsx` boundaries, `/status` orphaned, Playwright
   configured but not installed, no eslint despite 4 pragmas).

### Cleaned in this pass (Phase 0, 2026-10-21)

Deletions (each verified to have zero references before removal):
- Repo debris: duplicated product manifesto (one copy missing its `.md`
  extension), stray agent-prompt file (→ preserved as `docs/MISSION.md`),
  6-line `doc.md` stub, committed `bin/*.exe`, `logs/`.
- Dead code: `httpapi.writeGaugeSimple`, `config.RecommendedDBPoolSize()`
  (duplicate of the live formula in `db.Open`).
- Orphaned scripts: `create_github_issues.ps1` (its blueprint docs never
  existed), `generate-types.sh` (its output dir never existed), `canary.sh` and
  `load-test.sh` (referenced by nothing), `docs/pgbackrest-setup.md`
  (undeployed system; references fixed in compose + restore runbook).
- Doc truth pass: both audits → this file; SLO.md rewritten to describe only
  what is emitted; runbook link list corrected; backlog rows that shipped
  marked done; STAGING.md migration reference fixed;
  `SPRINT_CLOSEOUT.md` mission-file pointer fixed.
- Added `scripts/go-gate.sh` so `go build`/`vet`/`test` run identically from
  Windows and WSL checkouts.

## 5. Standing docs-sync rule

Incidents 1 and 7 above (§2) both happened because a doc claimed a control was
live while code did nothing. Standing rule, for every future change:

> A doc that describes a *system* (not a plan) must name the code that makes it
> true, and the claim must be re-greppable at merge time. Strategy/roadmap docs
> must say what is *not* built. When a claim and the code disagree, the code is
> reality and the doc is broken.

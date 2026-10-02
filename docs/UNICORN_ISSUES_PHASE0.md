# TrackSphere Phase 0 / P0 — Ready-to-push Issues

> No git remote / `gh` found in this workspace, so issues are staged here.
> When ready: `git init+commit+remote` then run `pwsh scripts/create_github_issues.ps1`.
> Or copy-paste each block into GitHub → New Issue.
> Labels suggested: `P0`, `security`, `backend`, `frontend`, `ux`, `a11y`, `infra`, `compliance`.

---

## ISSUE 1 — [P0/backend/security] Scope tracking_number uniqueness per tenant
**Labels:** P0, backend, security
**Body:**
Global `UNIQUE(tracking_number)` in `internal/db/migrations/000001_init.sql:58` blocks cross-tenant reuse + leaks existence via 409 (`internal/shipments/repository.go:102`).
**Accept:**
- [ ] Migration: drop global unique, add `UNIQUE(tenant_id,tracking_number)`, backfill check
- [ ] Cross-tenant create returns 404-style (no enumeration), not 409 oracle
- [ ] `rls_test.sql` + e2e case for same tracking number in 2 tenants
- [ ] Docs updated

## ISSUE 2 — [P0/backend] Correct webhook error mapping + retry semantics
**Labels:** P0, backend
**Files:** `internal/httpapi/webhook_handlers.go:70-77`
**Accept:**
- [ ] `ErrNotFound→404 unknown_tracking`, validation→400, DB/internal→500 with `Retry-After`
- [ ] `webhook_inbox.error` taxonomy column
- [ ] Carriers can backoff correctly; ops dashboard shows failure class
- [ ] e2e asserts 404 vs 500 split

## ISSUE 3 — [P0/backend] Harden webhook_inbox for non-JSON payloads
**Labels:** P0, backend
**Files:** `internal/httpapi/webhook_handlers.go:49-53`
**Accept:**
- [ ] Store raw `text` + `jsonb` generated/try-parse; never fail audit insert on bad JSON
- [ ] 1MiB cap retained, invalid JSON → 400 + audited row
- [ ] Test with XML/plain-text payload

## ISSUE 4 — [P0/backend] Fix manual event ID collision + source
**Labels:** P0, backend
**Files:** `internal/httpapi/webhook_handlers.go:128`
**Accept:**
- [ ] `EventID = uuid / ops-<uuid>` (no same-second collision)
- [ ] `source='manual'` not `webhook`
- [ ] Duplicate returns idempotent 200 with `duplicate:true`, not misleading 201

## ISSUE 5 — [P0/backend] Validate pagination + add cursor pagination
**Labels:** P0, backend
**Files:** `internal/httpapi/shipments_handlers.go`, `internal/shipments/input.go`
**Accept:**
- [ ] Negative/NaN `limit/offset` → 400, enum-validate `status/carrier`
- [ ] Cursor (`created_at+id`) pagination alongside offset; `alerts LIMIT 100` removed
- [ ] Stable sort, no duplicates under concurrent inserts

## ISSUE 6 — [P0/security] Per-carrier/per-tenant webhook secrets + replay window
**Labels:** P0, security, backend
**Files:** `internal/config/config.go:70`, `internal/httpapi/webhook_handlers.go:45`
**Accept:**
- [ ] Secret map (not single global), carrier allow-list, timestamp tolerance (e.g. ±5min)
- [ ] Rotation without breaking all carriers
- [ ] `dedup_key` namespaced safely; docs for carrier onboarding

## ISSUE 7 — [P0/security] Default shipments to private + publish API
**Labels:** P0, security, backend, frontend
**Files:** `internal/db/migrations/000001_init.sql:71`
**Accept:**
- [ ] `is_public DEFAULT false` migration
- [ ] `PATCH /shipments/{id}` (cancel/visibility) + UI toggle on detail
- [ ] Public portal only serves explicitly published; no precise lat/lng for anon

## ISSUE 8 — [P0/security] Rate-limit auth/webhook/track
**Labels:** P0, security, backend
**Files:** `internal/httpapi/auth_login.go`, `public_handlers.go`, `webhook_handlers.go`
**Accept:**
- [ ] Per-IP rate-limit on login/MFA/webhook/track (e.g. `x/time/rate` + PG `login_attempts`)
- [ ] Argon2 DoS mitigated; 429 with `Retry-After`
- [ ] Tests for lockout/backoff

## ISSUE 9 — [P0/security] Enforce RBAC owner/admin/member
**Labels:** P0, security, backend
**Files:** `internal/httpapi/server.go`, `middleware.go`
**Accept:**
- [ ] Middleware checks role for create/resolve/invite; member read-mostly
- [ ] Invite/user CRUD/deactivate API; `is_active` enforced
- [ ] e2e: member cannot resolve/create where forbidden

## ISSUE 10 — [P0/infra] Restore CI (vet+test+web-build+rls+e2e)
**Labels:** P0, infra
**Files:** `.github/workflows/` (currently empty), `Makefile:88`
**Accept:**
- [ ] `ci.yml`: Go vet+test, `tsc --noEmit` + `next build`, `rls_test.sql` on PG service, e2e
- [ ] Branch protection ready; build stamps version via ldflags

## ISSUE 11 — [P0/security] Prod guards: secrets, seed reset, version
**Labels:** P0, security, infra
**Files:** `internal/config/config.go`, `cmd/seed/main.go`, `deploy/Dockerfile.api`
**Accept:**
- [ ] Reject weak `SECRET_KEY/WEBHOOK_SECRET` when `ENV=production` (min 32B)
- [ ] `seed --reset` refuses in production
- [ ] `buildVersion` stamped, exposed in `/health`

## ISSUE 12 — [P0/security] Auth completeness: reset/verify/revoke/recovery/audit
**Labels:** P0, security, backend
**Accept:**
- [ ] Password reset, email verify, session list/revoke-others, TOTP recovery codes
- [ ] Session rotation on login; delete all on password/MFA change
- [ ] `auth_events` audit table; no PII in logs

## ISSUE 13 — [P0/frontend/a11y] Labels, focus, contrast, live regions
**Labels:** P0, frontend, a11y
**Files:** `web/src/app/login/page.tsx`, `register/page.tsx`, `web/src/components/CreateShipmentForm.tsx`, `ui.tsx`, `globals.css`
**Accept:**
- [ ] All `<label htmlFor/id>`, `aria-current` nav, skip-link, focus trap on dialogs
- [ ] `autocomplete="one-time-code"` on MFA; `aria-live` errors/toasts
- [ ] Pills icon+label; text accent `#d95a00`; contrast pass

## ISSUE 14 — [P0/frontend] Responsive shell + skeletons + actionable empties
**Labels:** P0, frontend, ux
**Files:** `Shell.tsx`, `page.tsx`, `shipments/page.tsx`, `shipments/[id]/page.tsx`, `RequireAuth.tsx`, `ui.tsx`
**Accept:**
- [ ] Collapsible sidebar, `overflow-x-auto` tables, `p-4 md:p-8`
- [ ] Skeletons replace all `Loading…`; empties with illustration + CTA + retry
- [ ] Public logo → `/track`; remove demo creds from prod UI; forgot-password stub

## ISSUE 15 — [P0/frontend] SSE toasts + badges + drill-downs
**Labels:** P0, frontend, ux
**Files:** `web/src/lib/live.ts`, `Shell.tsx`, `app/page.tsx`
**Accept:**
- [ ] Toast on `shipment.updated`/`alert.changed` + nav exception badge
- [ ] All `StatTile`s link to pre-filtered views
- [ ] No silent list jumps; `aria-live` announcements

## ISSUE 16 — [P0/frontend] Humanized timeline + relative times
**Labels:** P0, frontend, ux
**Files:** `web/src/components/Timeline.tsx`
**Accept:**
- [ ] Code → human copy + icons; dual-vocab (ops vs customer)
- [ ] Relative times (`in 2d`, `3d overdue`); source/freshness shown

## ISSUE 17 — [P0/frontend] Route map: pins + polyline + legend
**Labels:** P0, frontend, ux
**Files:** `web/src/components/ShipmentMap.tsx`, `shipments/[id]/page.tsx:31-40`
**Accept:**
- [ ] Origin/destination pins + dashed route + legend; no `center flash`
- [ ] Keyboard-accessible markers; `prefers-reduced-motion` respected

## ISSUE 18 — [P0/product] Publish toggle + copy-link/QR + Notify-me stub
**Labels:** P0, frontend, backend, product
**Accept:**
- [ ] Detail + public page: `isPublic` toggle, copy-tracking-link/QR
- [ ] `Notify me` opt-in stub (email) → feeds Phase 1 notification center
- [ ] Customer portal hides internal fields; white-label ready props

---

### Phase 1 preview (create after P0 burn-down)
- `/exceptions` queue (severity/age/assignee/SLA/note/resolve drawer)
- Shipment list: carrier/mode/ETA filters, sort, pagination UI, ⌘K, saved views, bulk
- Customer: history + preferences + branded portal (logo/color/domain) + ETA confidence
- `/settings`: profile, MFA enroll UI (`mfaEnroll` exists, no UI), team/RBAC, API keys, webhooks-out, billing/trial state
- Analytics v1: on-time %, transit avg, exceptions by kind, carrier bars, CSV export

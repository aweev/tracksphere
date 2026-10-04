# Security & SOC 2 readiness (P3)

This is the control narrative behind `GET /api/v1/compliance/evidence`
(owner-only, live data). Control IDs follow TSC CC6–CC8 language.

## CC6.1 — Logical access

- Sessions are 256-bit tokens; only SHA-256 hashes persist. Cookies are
  HttpOnly + SameSite=Lax (+Secure in production).
- Passwords: argon2id (64 MiB, t=1, p=4). Uniform login errors (no user
  enumeration); registration still discloses taken emails (accepted,
  documented — invite-only tenants mitigate).
- MFA: TOTP, AES-256-GCM-sealed secrets. SSO: OIDC (Google, Entra) with
  JWKS RS256 verification, aud/iss/exp checks, subject-pinned linking
  (no email-takeover after first link).
- Roles enforced per route (`owner > admin > member`); cross-tenant access
  returns 404 (no enumeration oracle). API keys: hashed, prefixed,
  role-capped, revocable. Evidence: `tenant.activeUsers/mfaUsers/sessions`,
  `auth90d` success/failure split.

## CC6.6 — Encryption

- In transit: TLS at the edge (Cloudflare); loopback-only Compose binds.
- At rest: sealed TOTP/credential/token blobs (AES-256-GCM via
  `TRACKSPHERE_SECRET_KEY`), hashed sessions/keys; DB disk encryption is a
  host responsibility (recorded in the DPA).

## CC7.2 — Monitoring

- One structured log line per request (method/path/status/duration/
  request-id, no PII). `auth_events` + `shipment_audit` are append-only.
- `/metrics` (queue depth, DLQ, SSE, pool) + `/statusz` feed the status page
  and the on-call alerts (`jobs_dead > 0` pages).
- Rate limits on every unauthenticated route (register 5/m, login/MFA 10/m,
  webhooks 120/60/m, track 60/m + subscribe 10/m) with `429 + Retry-After`.

## CC7.3 — Incident & change

- DLQ (`/jobs/dead` + replay) makes failures visible and reversible.
- Migrations are embedded, advisory-locked, forward-only; `APP_VERSION`
  stamps every build into `/health`.
- Backups: nightly `pg_dump` off-host before migration changes (runbook:
  `docs/STAGING.md`).

## CC8.1 — Change control

- CI (`ci.yml`): vet + tests + typecheck/build + migrate/seed/RLS proof.
  `rls_test.sql` proves six isolation properties as the restricted role.
- Secrets: 32-char production minimums enforced at boot; per-carrier
  rotation; seed `--reset` refuses production.

## Privacy (GDPR)

- Minimization: public portal projects safe fields only; precise lat/lng
  never leaves the tenant context.
- Rights: `GET /account/export` (Art. 20), `DELETE /account` with ERASE
  confirm (Art. 17, cascade-verified by FK).
- Residency: single-region deployments, `TRACKSPHERE_REGION` attested at
  runtime (`docs/REGIONS.md`).

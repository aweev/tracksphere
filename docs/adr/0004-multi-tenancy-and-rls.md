# ADR 0004 — Tenant isolation in the database (FORCE RLS + restricted role)

**Status:** accepted · **Date:** 2026-10-01 · **Revised:** 2026-10-02

## Context
TrackSphere is multi-tenant: one database holds every customer's shipments,
timelines, and alerts. A single missing `WHERE tenant_id = $1` in application code
would leak data across customers. The GDPR and SOC 2 story also wants isolation to
be demonstrable, not merely intended.

## Decision
Enforce isolation in the database and make it **fail closed**:
- every business table carries `tenant_id` and has `ENABLE` + **`FORCE ROW LEVEL
  SECURITY`** with `USING`/`WITH CHECK` policies;
- the application connects as **`tracksphere_app`** — explicitly *not* a
  superuser, *not* `BYPASSRLS`, and *not* the table owner. Migrations use the owner
  role via `TRACKSPHERE_MIGRATIONS_URL`;
- `db.WithTenant()` pins `app.tenant_id` with `set_config(..., is_local => true)`
  so it is scoped to the transaction and cannot leak onto a pooled connection;
- only two narrowly-scoped, explicitly-named escape hatches exist, both
  transaction-local:
  - `db.SetSystem()` — bootstrap writes (tenant creation) and the webhook resolver,
    which must find a shipment by tracking number before a tenant is known; it is
    **cleared** immediately after the tenant is pinned,
  - `db.SetPublicAccess()` — the tracking portal, which unlocks only
    `is_public = true` rows and is never combined with a tenant context;
- portal visibility is a dedicated flag rather than an unconditional
  `OR is_public = true` branch, so tenant-scoped queries can never see another
  tenant's public shipments.

## Consequences
- A tenant-scoped query with no context returns **zero rows**, never another
  tenant's data. Enforced by the database, not by developer discipline.
- Two independent proofs ship with the repo: `scripts/rls_test.sql` (six
  properties, run as the restricted role) and the isolation suite in
  `scripts/e2e.ps1`.
- Operational rule: **never** point `TRACKSPHERE_DATABASE_URL` at the owner or a
  superuser. Doing so silently disables every policy — this was caught during
  development precisely because the first RLS test ran against Docker's superuser
  `POSTGRES_USER` and failed to isolate.
- Cross-tenant resource access returns **404, not 403**, so shipment UUIDs cannot
  be enumerated across tenants.
- Auth tables (`users`, `sessions`) are deliberately outside RLS: login looks up
  by email before any tenant is known, and sessions are read by token hash. They
  are never queried by tenant range.

## Alternatives considered
- **Application-level filtering only**: single missed predicate = breach.
- **Schema- or database-per-tenant**: strongest isolation, but migration and
  connection-management cost is prohibitive at the low end of the pricing tiers.
- **RLS with a superuser app role**: no isolation at all (superusers bypass RLS).

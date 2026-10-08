# TrackSphere PR checklist — paste into the PR description.

## Backup gate (required for destructive changes)
- [ ] N/A (no migration, no erase path, no retention change), or
- [ ] `pg_dump -Fc` taken before merge; SHA: `sha256:________`
      cmd: `pg_dump -Fc -U <owner> -d tracksphere > tracksphere-$(date +%F).dump`

Destructive = new/edited migration, `DELETE /account`, retention pruning,
`jobs`/`webhook_inbox` schema change, RLS policy change.

## Tenancy
- [ ] New tenant table has `tenant_id`, FORCE RLS, and a `rls_test.sql` case, or N/A
- [ ] New job kind writes `tenant_id` via `queue.EnqueueTxTenant`, or N/A
- [ ] DLQ reads (if touched) stay tenant-scoped, or N/A

## Verification
- [ ] `go vet ./...` + `go test ./... -count=1` (note: run inside WSL/Linux, not Windows Go)
- [ ] `web`: `npm run typecheck && npm run build`
- [ ] DB: migrate + seed + `rls_test.sql` (app role) + `schema_test.sql` (owner)

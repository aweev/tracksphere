# Creates GitHub issues from docs/UNICORN_ISSUES_PHASE0.md titles.
# Usage:
#   1. git add -A; git commit -m "chore: unicorn blueprint"; git remote add origin <url>; git push -u origin master
#   2. gh auth login
#   3. pwsh scripts/create_github_issues.ps1
# Requires: gh CLI (https://cli.github.com)

$ErrorActionPreference = "Stop"

function New-Issue($Title, $Labels, $Body) {
  $labelArgs = @()
  foreach ($l in $Labels) { $labelArgs += @("--label", $l) }
  gh issue create --title $Title @labelArgs --body $Body | Write-Host
}

New-Issue "[P0/backend/security] Scope tracking_number uniqueness per tenant" @("P0","backend","security") @"
Global UNIQUE(tracking_number) blocks cross-tenant reuse + leaks via 409. See docs/UNICORN_BLUEPRINT.md P0-1 and docs/UNICORN_ISSUES_PHASE0.md ISSUE 1.
Accept: migration to UNIQUE(tenant_id,tracking_number), no enumeration, rls+e2e cases.
"@

New-Issue "[P0/backend] Correct webhook error mapping + retry semantics" @("P0","backend") @"
All IngestEvent errors map to 404 today (webhook_handlers.go:70-77). Split ErrNotFound->404 else 500+Retry-After. See ISSUE 2.
"@

New-Issue "[P0/backend] Harden webhook_inbox for non-JSON payloads" @("P0","backend") @"
Inbox insert fails on non-JSON, breaking audit-first. Store text+jsonb generated. See ISSUE 3.
"@

New-Issue "[P0/backend] Fix manual event ID collision + source" @("P0","backend") @"
ops-<8>-timestamp collides same-second. Use uuid + source=manual. See ISSUE 4.
"@

New-Issue "[P0/backend] Validate pagination + add cursor pagination" @("P0","backend") @"
Negative offset -> 500 today. Validate + cursor pagination. See ISSUE 5.
"@

New-Issue "[P0/security] Per-carrier/per-tenant webhook secrets + replay window" @("P0","security","backend") @"
Single global secret, no allow-list/window. Add secret map + rotation. See ISSUE 6.
"@

New-Issue "[P0/security] Default shipments to private + publish API" @("P0","security","backend","frontend") @"
is_public DEFAULT true leaks by default. Flip to false + PATCH publish + UI toggle. See ISSUE 7.
"@

New-Issue "[P0/security] Rate-limit auth/webhook/track" @("P0","security","backend") @"
No rate-limit + heavy Argon2 = DoS. Add per-IP limits + 429. See ISSUE 8.
"@

New-Issue "[P0/security] Enforce RBAC owner/admin/member" @("P0","security","backend") @"
Roles exist in DB, never checked. Add middleware + invite/deactivate. See ISSUE 9.
"@

New-Issue "[P0/infra] Restore CI (vet+test+web-build+rls+e2e)" @("P0","infra") @"
.github/workflows/ empty. Add ci.yml. See ISSUE 10.
"@

New-Issue "[P0/security] Prod guards: secrets, seed reset, version" @("P0","security","infra") @"
Weak defaults ship, seed --reset can wipe prod, version=dev. Guard all. See ISSUE 11.
"@

New-Issue "[P0/security] Auth completeness" @("P0","security","backend") @"
Missing reset/verify/revoke/recovery/audit. See ISSUE 12.
"@

New-Issue "[P0/frontend/a11y] Labels, focus, contrast, live regions" @("P0","frontend","a11y") @"
Missing htmlFor/id, aria-current, focus trap, contrast. See ISSUE 13.
"@

New-Issue "[P0/frontend] Responsive shell + skeletons + actionable empties" @("P0","frontend","ux") @"
Sidebar never collapses, Loading..., dashed Empty. See ISSUE 14.
"@

New-Issue "[P0/frontend] SSE toasts + badges + drill-downs" @("P0","frontend","ux") @"
live.ts silently invalidates. Add toasts + badges + drillable tiles. See ISSUE 15.
"@

New-Issue "[P0/frontend] Humanized timeline + relative times" @("P0","frontend","ux") @"
Raw CUSTOMS_HOLD leaked to customers. Dual-vocab + relative times. See ISSUE 16.
"@

New-Issue "[P0/frontend] Route map: pins + polyline + legend" @("P0","frontend","ux") @"
Markers only, no route. Add pins+polyline+legend. See ISSUE 17.
"@

New-Issue "[P0/product] Publish toggle + copy-link/QR + Notify-me stub" @("P0","frontend","backend","product") @"
No visibility control or share. Add toggle + copy/QR + stub. See ISSUE 18.
"@

Write-Host "Done. Review with: gh issue list --limit 30"

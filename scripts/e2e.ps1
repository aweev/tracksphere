<#
.SYNOPSIS
  TrackSphere end-to-end proof. Exercises the REAL running stack and asserts
  behaviour, not just status codes. Exits non-zero on any failure.

.DESCRIPTION
  Covers: health / auth / tenant isolation via registration / shipment CRUD /
  signed carrier webhook (accept, idempotent replay, signature rejection) /
  worker-driven jobs & alerts / public portal masking / SSE broadcast /
  Next.js proxy (when the web tier is up).

.EXAMPLE
  ./scripts/e2e.ps1
  ./scripts/e2e.ps1 -ApiBase http://localhost:8080 -WebBase http://localhost:3100
#>
[CmdletBinding()]
param(
  [string]$ApiBase = 'http://localhost:8080',
  [string]$WebBase = 'http://localhost:3100',
  [string]$WebhookSecret = 'dev-carrier-webhook-secret',
  [string]$DemoEmail = 'demo@tracksphere.dev',
  [string]$DemoPassword = 'DemoPassw0rd!',
  [int]$WorkerWaitSeconds = 8
)

$ErrorActionPreference = 'Stop'
$script:Passed = 0
$script:Failed = 0
$runId = Get-Date -Format 'HHmmss'

function Ok   ($msg) { Write-Host "  [PASS] $msg" -ForegroundColor Green; $script:Passed++ }
function Fail ($msg) { Write-Host "  [FAIL] $msg" -ForegroundColor Red;   $script:Failed++ }
function Step ($msg) { Write-Host "`n== $msg" -ForegroundColor Cyan }
function Check ($cond, $msg) { if ($cond) { Ok $msg } else { Fail $msg } }

# -- helpers -------------------------------------------------------------
# All HTTP goes through curl.exe on purpose: Windows PowerShell 5.1's
# Invoke-WebRequest/Invoke-RestMethod lack -SkipHttpErrorCheck and their
# session pooling reported stale statuses for repeated requests, which made
# assertions unreliable. curl is deterministic and ships with Windows 10+.

function Invoke-ApiRaw {
  param([string]$Path, [string]$Method = 'Get', [string]$Body, [hashtable]$Headers)
  $tmp = [System.IO.Path]::GetTempFileName()
  $ca = @('-s', '-o', $tmp, '-w', '%{http_code}', '-X', $Method.ToUpperInvariant(),
          '-H', 'Content-Type: application/json')
  if ($Headers) {
    foreach ($k in $Headers.Keys) { $ca += @('-H', "${k}: $($Headers[$k])") }
  }
  if ($Body) {
    $bodyFile = "$tmp.body"
    [System.IO.File]::WriteAllText($bodyFile, $Body, (New-Object System.Text.UTF8Encoding($false)))
    $ca += @('--data-binary', "@$bodyFile")
  }
  $ca += "$ApiBase$Path"
  $code = & curl.exe @ca
  $content = ''
  if (Test-Path $tmp) { $content = [System.IO.File]::ReadAllText($tmp) }
  Remove-Item $tmp, "$tmp.body" -Force -ErrorAction SilentlyContinue
  return [pscustomobject]@{ StatusCode = [int]$code; Content = $content }
}

# Login/registration returning { Token, Header, Envelope, Raw }.
function New-Session {
  param([string]$Path = '/api/v1/auth/login', [string]$Body)
  $hdrFile = [System.IO.Path]::GetTempFileName()
  $bodyFile = "$hdrFile.body"
  [System.IO.File]::WriteAllText($bodyFile, $Body, (New-Object System.Text.UTF8Encoding($false)))
  $out = & curl.exe -s -D $hdrFile -H 'Content-Type: application/json' --data-binary "@$bodyFile" "$ApiBase$Path"
  $hdr = Get-Content $hdrFile -Raw
  $token = ([regex]::Match($hdr, 'tracksphere_session=([^;]+)')).Groups[1].Value
  Remove-Item $hdrFile, $bodyFile -Force -ErrorAction SilentlyContinue
  $envelope = $null
  try { $envelope = ($out | ConvertFrom-Json) } catch { }
  return [pscustomobject]@{
    Token    = $token
    Header   = "Cookie: tracksphere_session=$token"
    Envelope = $envelope
  }
}

# Authenticated request returning the raw { StatusCode, Content }.
function Api {
  param([string]$Path, [string]$Method = 'Get', $Body, [string]$CookieHeader)
  $json = $null
  if ($Body) { $json = ($Body | ConvertTo-Json -Compress) }
  $headers = @{}
  if ($CookieHeader) { $headers['Cookie'] = ($CookieHeader -replace '^Cookie:\s*', '') }
  return Invoke-ApiRaw $Path $Method -Body $json -Headers $headers
}

# Authenticated request returning the parsed envelope object.
function ApiJson {
  param([string]$Path, [string]$Method = 'Get', $Body, [string]$CookieHeader)
  $raw = Api $Path $Method $Body $CookieHeader
  return ($raw.Content | ConvertFrom-Json)
}

function New-CarrierSignature {
  param([string]$Body, [string]$Secret)
  $hmac = New-Object System.Security.Cryptography.HMACSHA256
  $hmac.Key = [System.Text.Encoding]::UTF8.GetBytes($Secret)
  $bytes = [System.Text.Encoding]::UTF8.GetBytes($Body)
  'sha256=' + ([BitConverter]::ToString($hmac.ComputeHash($bytes)) -replace '-', '').ToLower()
}
function Now-Iso { (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') }

Write-Host "TrackSphere E2E - $ApiBase (run $runId)" -ForegroundColor White

# -- 1. Health -----------------------------------------------------------
Step '1. Liveness & dependencies'
$health = Invoke-ApiRaw '/api/v1/health'
Check ($health.StatusCode -eq 200) 'health endpoint responds 200'
$healthJson = $health.Content | ConvertFrom-Json
Check ($healthJson.data.status -eq 'ok') 'health reports ok'
Check ($healthJson.data.database -eq $true) 'database reachable'

# -- 2. Authentication ---------------------------------------------------
Step '2. Authentication'
$loginBody = @{ email = $DemoEmail; password = $DemoPassword } | ConvertTo-Json -Compress
$login = New-Session -Body $loginBody
Check ($login.Envelope.data.user.email -eq $DemoEmail) "login as $DemoEmail"
Check ($null -ne $login.Token -and $login.Token.Length -gt 20) 'login issued a session cookie'
Check ($login.Envelope.data.user.createdAt -match '^(19|20)\d\d-') 'session user carries a real createdAt'

$me = ApiJson '/api/v1/auth/me' -CookieHeader $login.Header
Check ($me.data.user.role -eq 'owner') 'session resolves to the tenant owner'

$bad = Invoke-ApiRaw '/api/v1/auth/login' 'Post' '{"email":"nobody@example.com","password":"wrong123"}'
Check ($bad.StatusCode -eq 401) 'unknown credentials rejected with 401'

$anon = Invoke-ApiRaw '/api/v1/dashboard'
Check ($anon.StatusCode -eq 401) 'unauthenticated dashboard request rejected with 401'

# -- 3. Tenant isolation -------------------------------------------------
Step '3. Tenant isolation'
$freshEmail = "e2e-$runId@example.com"
$reg = New-Session -Path '/api/v1/auth/register' -Body (@{
  orgName = "E2E Org $runId"; name = 'E2E Bot'; email = $freshEmail; password = 'E2ePassword123'
} | ConvertTo-Json -Compress)
Check ($reg.Envelope.data.tenant.slug -like 'e2e-org*') 'registration provisions a tenant'
Check ($reg.Token.Length -gt 20) 'registration issued a session cookie'

$freshList = ApiJson '/api/v1/shipments' -CookieHeader $reg.Header
Check ([int]$freshList.meta.total -eq 0) 'new tenant sees 0 shipments (RLS isolation)'

$demoList = ApiJson '/api/v1/shipments' -CookieHeader $login.Header
Check ([int]$demoList.meta.total -gt 0) "demo tenant sees its own $($demoList.meta.total) shipments"

$foreignId = $demoList.data[0].id
$probe = Api "/api/v1/shipments/$foreignId" -CookieHeader $reg.Header
Check ($probe.StatusCode -eq 404) 'foreign tenant gets 404 on another tenant''s shipment id'

$probeEvents = Api "/api/v1/shipments/$foreignId/events" -CookieHeader $reg.Header
Check ($probeEvents.StatusCode -eq 404) 'foreign tenant gets 404 on another tenant''s timeline'
Check ($probeEvents.Content -notmatch 'CUSTOMS_HOLD|DEPARTED|ARRIVED') 'foreign tenant receives no event data'

# -- 4. Shipment lifecycle -----------------------------------------------
Step '4. Shipment lifecycle'
$tracking = "TS-E2E-$runId"
$created = ApiJson '/api/v1/shipments' 'Post' @{
  trackingNumber = $tracking; reference = "E2E-$runId"; carrier = 'maersk'
  mode = 'ocean'; origin = 'Shanghai, CN'; destination = 'Lagos, NG'
} $login.Header
Check ($created.data.trackingNumber -eq $tracking) 'shipment created'
Check ($created.data.status -eq 'booked') 'new shipment starts as booked'
Check ($created.data.isPublic -eq $false) 'new shipment is private by default'
$shipmentId = $created.data.id

$dup = Api '/api/v1/shipments' 'Post' @{ trackingNumber = $tracking; carrier = 'maersk'; mode = 'ocean' } $login.Header
Check ($dup.StatusCode -eq 409) 'duplicate tracking number in same tenant rejected with 409'

# Same tracking number in a DIFFERENT tenant must be allowed (no 409 oracle).
$crossDup = Api '/api/v1/shipments' 'Post' @{ trackingNumber = $tracking; carrier = 'maersk'; mode = 'ocean' } $reg.Header
Check ($crossDup.StatusCode -eq 201) 'same tracking number reusable across tenants'

$invalid = Api '/api/v1/shipments' 'Post' '{"trackingNumber":"TS-BAD-1","carrier":"x","mode":"submarine"}' $login.Header
Check ($invalid.StatusCode -eq 400) 'invalid transport mode rejected with 400'

$badLimit = Api '/api/v1/shipments?limit=999' -CookieHeader $login.Header
Check ($badLimit.StatusCode -eq 400) 'out-of-range limit rejected with 400'

$badOffset = Api '/api/v1/shipments?offset=-5' -CookieHeader $login.Header
Check ($badOffset.StatusCode -eq 400) 'negative offset rejected with 400'

$badCarrierBody = @{ trackingNumber = 'TS-NOPE-9999'; eventId = "x-$runId"; code = 'DEPARTED'; occurredAt = (Now-Iso) } | ConvertTo-Json -Compress
$badCarrier = Invoke-ApiRaw '/api/v1/webhooks/carriers/acme-express' 'Post' $badCarrierBody `
  -Headers @{ 'X-TrackSphere-Signature' = (New-CarrierSignature $badCarrierBody $WebhookSecret) }
Check ($badCarrier.StatusCode -eq 400) 'unknown carrier slug rejected with 400'

# Publish to the public portal (explicit opt-in since private-by-default).
$pubRaw = Api "/api/v1/shipments/$shipmentId" 'Patch' @{ isPublic = $true } $login.Header
Check ($pubRaw.StatusCode -eq 200) 'shipment published via PATCH'
Check ($pubRaw.Content -match '"isPublic":true') 'published flag persisted'

$privProbe = Api "/api/v1/shipments/$shipmentId" -CookieHeader $reg.Header
Check ($privProbe.StatusCode -eq 404) 'foreign tenant gets 404 on unpublished id even after publish flag (tenant isolation holds)'

# -- 4b. Bulk batch + idempotency ---------------------------------------
Step '4b. Bulk batch + idempotency'
$batchBody = @(
  @{ trackingNumber = "TS-E2E-B1-$runId"; carrier = 'dhl'; mode = 'air'; origin = 'A'; destination = 'B' },
  @{ trackingNumber = $tracking; carrier = 'maersk'; mode = 'ocean' },
  @{ trackingNumber = 'TS-BAD-X'; carrier = 'x'; mode = 'submarine' }
) | ConvertTo-Json -Compress
$idemKey = "e2e-$runId"
$batch = Invoke-ApiRaw '/api/v1/shipments:batch' 'Post' $batchBody `
  -Headers @{ 'Cookie' = ($login.Header -replace '^Cookie:\s*',''); 'Idempotency-Key' = $idemKey }
Check ($batch.StatusCode -eq 207) 'batch returns 207 multi-status'
Check ($batch.Content -match '"created":1') 'batch created 1 valid item, per-item errors for the rest'
$replay207 = Invoke-ApiRaw '/api/v1/shipments:batch' 'Post' $batchBody `
  -Headers @{ 'Cookie' = ($login.Header -replace '^Cookie:\s*',''); 'Idempotency-Key' = $idemKey }
Check ($replay207.Content -eq $batch.Content) 'idempotent replay returns the stored response'

# -- 4c. API keys (B2B) ---------------------------------------------------
Step '4c. Tenant API keys'
$keyCreated = ApiJson '/api/v1/apikeys' 'Post' @{ name = "e2e-$runId"; role = 'member' } $login.Header
Check (($null -ne $keyCreated.data.key) -and ($keyCreated.data.key -like 'tsk_*')) 'API key minted (raw shown once)'
$bearerList = Invoke-ApiRaw '/api/v1/shipments' 'Get' -Headers @{ 'Authorization' = "Bearer $($keyCreated.data.key)" }
Check ($bearerList.StatusCode -eq 200) 'Bearer API key authenticates B2B reads'
$keyList = Api '/api/v1/apikeys' -CookieHeader $login.Header
Check ($keyList.StatusCode -eq 200) 'owner can list keys (admin surface reachable)'
$agentLogin = New-Session -Body (@{ email = 'agent@tracksphere.dev'; password = 'AgentPassw0rd!' } | ConvertTo-Json -Compress)
$agentKeys = Api '/api/v1/apikeys' -CookieHeader $agentLogin.Header
Check ($agentKeys.StatusCode -eq 403) 'member gets 403 on admin API keys (RBAC enforced)'

# -- 4d. Team (admin) ------------------------------------------------------
Step '4d. Team invite'
$invite = ApiJson '/api/v1/team/invite' 'Post' @{ email = "mate-$runId@example.com"; name = 'Mate'; role = 'member' } $login.Header
Check ($null -ne $invite.data.tempPassword) 'invite returns one-time password'
$team = ApiJson '/api/v1/team' -CookieHeader $login.Header
Check ((@($team.data) | Where-Object { $_.email -like 'mate-*' }).Count -ge 1) 'team lists the invitee'

# -- 4e. Triage routes, MCP gate, recovery codes, notify prefs ---------------
Step '4e. Triage, MCP gate, recovery codes, notify prefs'
$notify = Api "/api/v1/shipments/$shipmentId/notify" 'Post' @{ tracking = $tracking; title = 'E2E update'; message = 'heads up'; customerUpdate = 'hi' } $login.Header
Check ($notify.StatusCode -eq 200) 'one-click customer notify queues (admin route live)'
$notifyMember = Api "/api/v1/shipments/$shipmentId/notify" 'Post' @{ tracking = $tracking; title = 'x'; message = 'y'; customerUpdate = 'z' } $agentLogin.Header
Check ($notifyMember.StatusCode -eq 403) 'member gets 403 on triage notify (admin-only)'
$carrierMail = Api "/api/v1/shipments/$shipmentId/email-carrier" 'Post' @{ tracking = $tracking; title = 'E2E'; message = 'm'; note = 'n' } $login.Header
Check ($carrierMail.StatusCode -eq 200) 'one-click carrier email queues (admin route live)'

$mcp = Api '/api/v1/mcp' 'Post' @{ jsonrpc = '2.0'; id = 1; method = 'tools/list' } $login.Header
Check ($mcp.StatusCode -eq 404) 'MCP 404s while TRACKSPHERE_MCP_ENABLED=0 (no phantom surface)'

$recov = Api '/api/v1/auth/mfa/recovery-codes' 'Post' $null $login.Header
Check ($recov.StatusCode -eq 400) 'recovery codes need enrollment first (route live, 400 not_enrolled)'

$prefs = ApiJson '/api/v1/notify/prefs' -CookieHeader $login.Header
Check ($prefs.data.smsEnabled -eq $false) 'metered SMS defaults OFF (kill-switch closed)'
$patched = ApiJson '/api/v1/notify/prefs' 'Patch' @{ smsEnabled = $true } $login.Header
Check ($patched.data.smsEnabled -eq $true) 'admin can open the SMS switch'
$restored = ApiJson '/api/v1/notify/prefs' 'Patch' @{ smsEnabled = $false } $login.Header
Check ($restored.data.smsEnabled -eq $false) 'switch restores to OFF (no spend left on)'

# -- 5. Carrier webhook ingestion ----------------------------------------
Step '5. Carrier webhook ingestion'
$payload = @{
  trackingNumber = $tracking; eventId = "e2e-evt-$runId"; code = 'DEPARTED'
  description = 'Vessel departed under E2E test'; location = 'Yangshan, CN'
  lat = 30.6; lng = 122.1; occurredAt = (Now-Iso); status = 'in_transit'
  eta = (Get-Date).ToUniversalTime().AddDays(7).ToString('yyyy-MM-ddTHH:mm:ssZ')
} | ConvertTo-Json -Compress
$sig = New-CarrierSignature $payload $WebhookSecret

$hook = Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $payload -Headers @{ 'X-TrackSphere-Signature' = $sig }
Check ($hook.StatusCode -eq 200) 'signed webhook accepted'
Check ($hook.Content -match '"status":"in_transit"') 'shipment advanced to in_transit'

$replay = Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $payload -Headers @{ 'X-TrackSphere-Signature' = $sig }
Check ($replay.Content -match '"duplicate":true') 'replayed webhook is idempotent'

$badsig = Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $payload -Headers @{ 'X-TrackSphere-Signature' = 'sha256=deadbeef' }
Check ($badsig.StatusCode -eq 401) 'tampered signature rejected with 401'

$unknownBody = @{ trackingNumber = 'TS-NOPE-9999'; eventId = "x-$runId"; code = 'DEPARTED'; occurredAt = (Now-Iso) } | ConvertTo-Json -Compress
$unknown = Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $unknownBody `
  -Headers @{ 'X-TrackSphere-Signature' = (New-CarrierSignature $unknownBody $WebhookSecret) }
Check ($unknown.StatusCode -eq 404) 'unknown tracking number rejected with 404'

$notJson = 'this is not json'
$notJsonSig = New-CarrierSignature $notJson $WebhookSecret
$badJson = Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $notJson `
  -Headers @{ 'X-TrackSphere-Signature' = $notJsonSig }
Check ($badJson.StatusCode -eq 400) 'non-JSON webhook body rejected with 400 (audit kept)'

# -- 6. Worker side effects ----------------------------------------------
Step "6. Background workers (waiting ${WorkerWaitSeconds}s)"
Start-Sleep -Seconds $WorkerWaitSeconds

$events = ApiJson "/api/v1/shipments/$shipmentId/events" -CookieHeader $login.Header
Check (@($events.data).Count -ge 1) "timeline recorded the carrier event ($(@($events.data).Count) entries)"

$detail = ApiJson "/api/v1/shipments/$shipmentId" -CookieHeader $login.Header
Check ($detail.data.status -eq 'in_transit') 'status change persisted'
Check ($null -ne $detail.data.eta) 'ETA present (carrier value or heuristic)'

# A customs hold must make the rules engine raise an alert.
$hold = @{
  trackingNumber = $tracking; eventId = "e2e-hold-$runId"; code = 'CUSTOMS_HOLD'
  description = 'Routine customs inspection'; location = 'Lagos Port'; occurredAt = (Now-Iso)
} | ConvertTo-Json -Compress
Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $hold `
  -Headers @{ 'X-TrackSphere-Signature' = (New-CarrierSignature $hold $WebhookSecret) } | Out-Null
Start-Sleep -Seconds $WorkerWaitSeconds

$alerts = ApiJson '/api/v1/alerts?status=open' -CookieHeader $login.Header
$mine = @($alerts.data) | Where-Object { $_.shipmentId -eq $shipmentId }
Check (@($mine).Count -ge 1) 'exception rules engine raised an alert'
if (@($mine).Count -ge 1) {
  $resolved = ApiJson "/api/v1/alerts/$($mine[0].id)/resolve" 'Post' -CookieHeader $login.Header
  Check ($resolved.data.ok -eq $true) 'alert can be resolved by an operator'
}

$dash = ApiJson '/api/v1/dashboard' -CookieHeader $login.Header
Check ($null -ne $dash.data.activeShipments) 'dashboard aggregates respond'

# -- 7. Public portal ----------------------------------------------------
Step '7. Public tracking portal'
$public = ApiJson "/api/v1/track/$tracking"
Check ($public.data.trackingNumber -eq $tracking) 'public portal resolves the tracking number'
Check ($null -eq $public.data.PSObject.Properties['tenantId']) 'public projection omits tenantId'
Check ($null -eq $public.data.PSObject.Properties['reference']) 'public projection omits internal reference'
Check ($null -ne $public.data.events) 'public timeline included'

$missing = Invoke-ApiRaw '/api/v1/track/TS-DOES-NOT-EXIST-0000'
Check ($missing.StatusCode -eq 404) 'unknown tracking number returns 404'

# -- 8. Realtime SSE -----------------------------------------------------
Step '8. Realtime SSE'
$job = Start-Job -ScriptBlock {
  param($cookie, $base)
  curl.exe -s -N --max-time 8 -H $cookie "$base/api/v1/stream"
} -ArgumentList $login.Header, $ApiBase
Start-Sleep -Seconds 2

$sseBody = @{
  trackingNumber = $tracking; eventId = "e2e-sse-$runId"; code = 'ARRIVED'
  description = 'Arrival for SSE assertion'; location = 'Lagos, NG'
  occurredAt = (Now-Iso); status = 'at_customs'
} | ConvertTo-Json -Compress
Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $sseBody `
  -Headers @{ 'X-TrackSphere-Signature' = (New-CarrierSignature $sseBody $WebhookSecret) } | Out-Null

Start-Sleep -Seconds 4
$stream = (Receive-Job $job) -join "`n"
Remove-Job $job -Force
Check ($stream -match 'event: shipment\.updated') 'SSE delivered shipment.updated'
Check ($stream -match 'event: alert\.changed') 'SSE delivered alert.changed'

# -- 10. P2 growth surfaces ------------------------------------------------
Step '10. P2 growth surfaces'

$brandPut = Api '/api/v1/branding' 'Put' @{ company = "E2E Co $runId"; color = '#0033aa' } $login.Header
Check ($brandPut.StatusCode -eq 200) 'brand upsert (PUT) works'
$brandGet = ApiJson '/api/v1/branding' -CookieHeader $login.Header
Check ($brandGet.data.company -eq "E2E Co $runId") 'brand persists'

$sub = Invoke-ApiRaw "/api/v1/track/$tracking/subscribe" 'Post' '{"channel":"email","recipient":"fan@example.com"}'
Check ($sub.StatusCode -eq 201) 'public notify-me subscribe works'
$smsOff = Invoke-ApiRaw "/api/v1/track/$tracking/subscribe" 'Post' '{"channel":"sms","recipient":"+15551234567"}'
Check ($smsOff.StatusCode -eq 409 -and $smsOff.Content -match 'channel_disabled') 'metered SMS refused while the tenant switch is off (P2-4)'
Check ($public.data.brand.color -ne $null) 'public portal carries brand (checked below)'
$public2 = ApiJson "/api/v1/track/$tracking"
Check ($public2.data.brand.company -eq "E2E Co $runId") 'public portal is white-labeled'

$leg = ApiJson "/api/v1/shipments/$shipmentId/legs" 'Post' @{
  carrier = 'dhl'; mode = 'road'; origin = 'Lagos Port'; destination = 'Lagos Island'
} $login.Header
Check ($null -ne $leg.data.id) 'journey leg created'
$legs = ApiJson "/api/v1/shipments/$shipmentId/legs" -CookieHeader $login.Header
Check (@($legs.data).Count -ge 1) 'legs listed'

$tmpDoc = [System.IO.Path]::GetTempFileName() + '.pdf'
[System.IO.File]::WriteAllText($tmpDoc, '%PDF-1.4 e2e')
$docCode = & curl.exe -s -o NUL -w '%{http_code}' -H ($login.Header -replace '^Cookie:\s*', 'Cookie: ') `
  -F "file=@$tmpDoc;type=application/pdf" "$ApiBase/api/v1/shipments/$shipmentId/documents"
Remove-Item $tmpDoc -Force -ErrorAction SilentlyContinue
Check ([int]$docCode -eq 201) 'document upload (PDF) works'
$docs = ApiJson "/api/v1/shipments/$shipmentId/documents" -CookieHeader $login.Header
Check (@($docs.data).Count -ge 1) 'documents listed'
$docId = $docs.data[0].id
$dlCode = & curl.exe -s -o NUL -w '%{http_code}' -H ($login.Header -replace '^Cookie:\s*', 'Cookie: ') `
  "$ApiBase/api/v1/documents/$docId/download"
Check ([int]$dlCode -eq 200) 'document download works'

$audit = ApiJson "/api/v1/shipments/$shipmentId/audit" -CookieHeader $login.Header
Check (@($audit.data).Count -ge 1) 'shipment audit trail records mutations'

$analytics = ApiJson '/api/v1/analytics' -CookieHeader $login.Header
Check ($analytics.data.total -ge 1) 'analytics aggregates respond'
Check ($null -ne $analytics.data.carriers) 'carrier benchmark present'

$billing = ApiJson '/api/v1/billing' -CookieHeader $login.Header
Check ($billing.data.plan -eq 'growth' -or $billing.data.plan -eq 'starter') 'billing state responds with plan'
Check ($billing.data.trialDaysLeft -ge 0) 'trial countdown present'

$mcp = ApiJson '/api/v1/mcp' 'Post' @{
  jsonrpc = '2.0'; id = 1; method = 'tools/call'
  params = @{ name = 'track_shipment'; arguments = @{ trackingNumber = $tracking } }
} $login.Header
Check ($mcp.result.content[0].text -match $tracking) 'MCP track_shipment answers'

$metrics = Invoke-ApiRaw '/api/v1/metrics'
Check ($metrics.StatusCode -eq 200 -and $metrics.Content -match 'tracksphere_jobs_pending') 'prometheus metrics exposed'

$stripeBad = Invoke-ApiRaw '/api/v1/billing/webhook' 'Post' '{}'
Check ($stripeBad.StatusCode -eq 503 -or $stripeBad.StatusCode -eq 401) 'stripe webhook rejects unconfigured/bad signatures'

$carrierSave = ApiJson '/api/v1/carriers' 'Post' @{
  carrier = 'fake'; baseUrl = 'fake://demo'; pollMinutes = 60
} $login.Header
Check ($null -ne $carrierSave.data.id) 'carrier credentials saved'
$pollNow = Api "/api/v1/carriers/fake/poll" 'Post' -CookieHeader $login.Header
Check ($pollNow.StatusCode -eq 202) 'carrier poll trigger accepted'

$export = Invoke-ApiRaw '/api/v1/account/export' -Headers @{ 'Cookie' = ($login.Header -replace '^Cookie:\s*', '') }
Check ($export.StatusCode -eq 200 -and $export.Content -match 'shipments') 'GDPR export dumps tenant data'

# -- 11. P3 intelligence ---------------------------------------------------
Step '11. P3 intelligence'

$detect = ApiJson '/api/v1/carriers/detect?tracking=1Z9999W99999999999' -CookieHeader $login.Header
Check (($detect.data | Where-Object { $_.carrier -eq 'ups' }).Count -ge 1) 'carrier-detect spots UPS 1Z'

$digest = ApiJson '/api/v1/analytics/digest' -CookieHeader $login.Header
Check ($null -ne $digest.data.weekStart) 'weekly digest computes'

$evidence = Api '/api/v1/compliance/evidence' -CookieHeader $login.Header
Check ($evidence.StatusCode -eq 200) 'owner can pull the SOC 2 evidence pack'
Check ($evidence.Content -match 'tenantIsolation') 'evidence pack attests controls'

$agentEvidence = Api '/api/v1/compliance/evidence' -CookieHeader $agentLogin.Header
Check ($agentEvidence.StatusCode -eq 403) 'member gets 403 on evidence (owner-only)'

$statusz = Invoke-ApiRaw '/api/v1/statusz'
Check ($statusz.StatusCode -eq 200 -and $statusz.Content -match 'operational|degraded') 'public status payload responds'
Check ($statusz.Content -match '"backup"') 'statusz reports backup freshness (P1-1)'

$ssoStatus = Invoke-ApiRaw '/api/v1/auth/sso/status'
Check ($ssoStatus.StatusCode -eq 200 -and $ssoStatus.Content -match 'google') 'SSO status advertises providers'

$analytics2 = ApiJson '/api/v1/analytics' -CookieHeader $login.Header
Check ($null -ne $analytics2.data.PSObject.Properties['etaAccuracyPct']) 'analytics scores ETA accuracy'

$suezTracking = "TS-E2E-SUEZ-$runId"
$suez = ApiJson '/api/v1/shipments' 'Post' @{
  trackingNumber = $suezTracking; carrier = 'maersk'; mode = 'ocean'
  origin = 'Suez Canal, EG'; destination = 'Rotterdam, NL'
} $login.Header
$suezPayload = @{
  trackingNumber = $suezTracking; eventId = "e2e-suez-$runId"; code = 'DEPARTED'
  description = 'Transiting the canal'; location = 'Suez, EG'
  occurredAt = (Now-Iso); status = 'in_transit'
} | ConvertTo-Json -Compress
Invoke-ApiRaw '/api/v1/webhooks/carriers/maersk' 'Post' $suezPayload `
  -Headers @{ 'X-TrackSphere-Signature' = (New-CarrierSignature $suezPayload $WebhookSecret) } | Out-Null
Start-Sleep -Seconds $WorkerWaitSeconds
$suezAlerts = ApiJson '/api/v1/alerts?status=open' -CookieHeader $login.Header
$suezHit = @($suezAlerts.data) | Where-Object { $_.shipmentId -eq $suez.data.id -and $_.kind -eq 'disruption' }
Check (@($suezHit).Count -ge 1) 'disruption corridor flagged on Suez lane'

# -- 9. Next.js frontend proxy (optional) --------------------------------
Step '9. Next.js frontend proxy'
$webProbe = 0
try { $webProbe = [int](& curl.exe -s -o NUL -w '%{http_code}' "$WebBase/api/v1/health") } catch { $webProbe = 0 }

if ($webProbe -eq 200) {
  $webHealthRaw = (& curl.exe -s "$WebBase/api/v1/health") | ConvertFrom-Json
  Check ($webHealthRaw.data.status -eq 'ok') 'web tier proxies /api/v1 to the Go API'

  $webHdr = [System.IO.Path]::GetTempFileName()
  $webBody = "$webHdr.body"
  [System.IO.File]::WriteAllText($webBody, $loginBody, (New-Object System.Text.UTF8Encoding($false)))
  $webLoginOut = & curl.exe -s -D $webHdr -H 'Content-Type: application/json' --data-binary "@$webBody" "$WebBase/api/v1/auth/login"
  $webToken = ([regex]::Match((Get-Content $webHdr -Raw), 'tracksphere_session=([^;]+)')).Groups[1].Value
  Remove-Item $webHdr, $webBody -Force -ErrorAction SilentlyContinue
  Check (($webLoginOut | ConvertFrom-Json).data.user.email -eq $DemoEmail) 'login works through the Next proxy (cookie round-trip)'

  $webDashRaw = (& curl.exe -s -H "Cookie: tracksphere_session=$webToken" "$WebBase/api/v1/dashboard") | ConvertFrom-Json
  Check ($null -ne $webDashRaw.data.activeShipments) 'authenticated dashboard fetch through the proxy'

  $webPage = & curl.exe -s -o NUL -w '%{http_code}' "$WebBase/login"
  Check ([int]$webPage -eq 200) 'Next serves the login page'
} else {
  Write-Host '  [SKIP] web tier not reachable (start it with: .\scripts\dev.ps1 stack)' -ForegroundColor Yellow
}

# -- Summary -------------------------------------------------------------
$verdictColor = 'Green'
if ($script:Failed -gt 0) { $verdictColor = 'Red' }
Write-Host ''
Write-Host '--------------------------------------------' -ForegroundColor White
Write-Host "PASSED: $script:Passed   FAILED: $script:Failed" -ForegroundColor $verdictColor
Write-Host '--------------------------------------------' -ForegroundColor White
if ($script:Failed -gt 0) { exit 1 }
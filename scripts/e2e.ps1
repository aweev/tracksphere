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
$shipmentId = $created.data.id

$dup = Api '/api/v1/shipments' 'Post' @{ trackingNumber = $tracking; carrier = 'maersk'; mode = 'ocean' } $login.Header
Check ($dup.StatusCode -eq 409) 'duplicate tracking number rejected with 409'

$invalid = Api '/api/v1/shipments' 'Post' '{"trackingNumber":"TS-BAD-1","carrier":"x","mode":"submarine"}' $login.Header
Check ($invalid.StatusCode -eq 400) 'invalid transport mode rejected with 400'

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
<#
.SYNOPSIS
  TrackSphere local development helper (Windows/PowerShell).

.EXAMPLE
  ./scripts/dev.ps1 db-up        # start Postgres in Docker (port 5433)
  ./scripts/dev.ps1 migrate      # apply + record migrations
  ./scripts/dev.ps1 seed         # load demo tenant and shipments
  ./scripts/dev.ps1 rls-test     # prove tenant isolation
  ./scripts/dev.ps1 e2e          # full end-to-end proof
  ./scripts/dev.ps1 stack        # api + worker + web in the background
  ./scripts/dev.ps1 stop         # stop the background processes
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('db-up', 'db-down', 'db-reset', 'migrate', 'seed', 'rls-test',
    'build', 'test', 'vet', 'api', 'worker', 'web', 'stack', 'e2e', 'stop', 'check')]
  [string]$Task = 'stack',

  [string]$Container = 'tracksphere-pg',
  [int]$DbPort = 5433,
  [int]$ApiPort = 8080,
  [int]$WebPort = 3100
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$ownerUrl = "postgres://tracksphere:tracksphere@localhost:$DbPort/tracksphere?sslmode=disable"
$appUrl = "postgres://tracksphere_app:tracksphere_app@localhost:$DbPort/tracksphere?sslmode=disable"

function Set-ServerEnv {
  $env:TRACKSPHERE_DATABASE_URL = $appUrl
  $env:TRACKSPHERE_MIGRATIONS_URL = $ownerUrl
  $env:TRACKSPHERE_SECRET_KEY = 'local-dev-secret-key-change-me-000000000000000000'
  $env:TRACKSPHERE_CARRIER_WEBHOOK_SECRET = 'dev-carrier-webhook-secret'
  $env:TRACKSPHERE_HTTP_ADDR = ":$ApiPort"
}

switch ($Task) {
  'db-up' {
    docker rm -f $Container 2>$null | Out-Null
    docker run -d --name $Container `
      -e POSTGRES_USER=tracksphere -e POSTGRES_PASSWORD=tracksphere `
      -e POSTGRES_DB=tracksphere -p "${DbPort}:5432" postgres:16 | Out-Null
    Write-Host "postgres listening on $DbPort (container $Container)" -ForegroundColor Green
  }
  'db-down' { docker rm -f $Container | Out-Null; Write-Host 'postgres removed' }
  'db-reset' {
    docker exec $Container psql -U tracksphere -d tracksphere `
      -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO tracksphere;'
  }
  'migrate' {
    $env:TRACKSPHERE_MIGRATIONS_URL = $ownerUrl
    $env:TRACKSPHERE_SECRET_KEY = 'x'
    $env:TRACKSPHERE_CARRIER_WEBHOOK_SECRET = 'x'
    go run ./cmd/migrate
  }
  'seed' {
    $env:TRACKSPHERE_SECRET_KEY = 'local-dev-secret-key-change-me-000000000000000000'
    $env:TRACKSPHERE_CARRIER_WEBHOOK_SECRET = 'dev-carrier-webhook-secret'
    go run ./cmd/seed @args
  }
  'rls-test' {
    Get-Content 'scripts/rls_test.sql' -Raw |
      docker exec -i $Container psql -U tracksphere_app -d tracksphere -v ON_ERROR_STOP=1
  }
  'build' { go build -o bin/ ./cmd/... ; Write-Host 'binaries in ./bin' }
  'test' { go test ./... -count=1 }
  'vet' { go vet ./... }
  'check' { go vet ./...; go test ./... -count=1; Push-Location web; npm run build; Pop-Location }
  'api' { Set-ServerEnv; go run ./cmd/api }
  'worker' { Set-ServerEnv; go run ./cmd/worker }
  'web' { Push-Location web; npm run dev -- -p $WebPort; Pop-Location }
  'e2e' { & (Join-Path $PSScriptRoot 'e2e.ps1') -ApiBase "http://localhost:$ApiPort" }
  'stack' {
    New-Item -ItemType Directory -Force -Path logs | Out-Null
    $env:TRACKSPHERE_DATABASE_URL = $appUrl
    $env:TRACKSPHERE_MIGRATIONS_URL = $ownerUrl
    $env:TRACKSPHERE_SECRET_KEY = 'local-dev-secret-key-change-me-000000000000000000'
    $env:TRACKSPHERE_CARRIER_WEBHOOK_SECRET = 'dev-carrier-webhook-secret'
    Start-Process cmd.exe -ArgumentList '/c', 'go', 'run', './cmd/api' `
      -RedirectStandardOutput 'logs/api.log' -RedirectStandardError 'logs/api.err.log' -WindowStyle Hidden
    Start-Process cmd.exe -ArgumentList '/c', 'go', 'run', './cmd/worker' `
      -RedirectStandardOutput 'logs/worker.log' -RedirectStandardError 'logs/worker.err.log' -WindowStyle Hidden
    Start-Process cmd.exe -ArgumentList '/c', 'npm', 'run', 'dev', '--', '-p', "$WebPort" `
      -WorkingDirectory (Join-Path $root 'web') `
      -RedirectStandardOutput 'web/dev.log' -RedirectStandardError 'web/dev.err.log' -WindowStyle Hidden
    Start-Sleep -Seconds 15
    Write-Host "api   http://localhost:$ApiPort/api/v1/health" -ForegroundColor Green
    Write-Host "web   http://localhost:$WebPort" -ForegroundColor Green
    Write-Host 'logs: .\logs\api.log, .\logs\worker.log, .\web\dev.log'
    Write-Host "stop with: ./scripts/dev.ps1 stop" -ForegroundColor Yellow
  }
  'stop' {
    Get-Process api, worker -ErrorAction SilentlyContinue | Stop-Process -Force
    Get-Process node -ErrorAction SilentlyContinue |
      Where-Object { $_.Path -like '*node*' } | Stop-Process -Force -ErrorAction SilentlyContinue
    Write-Host 'api/worker/web stopped'
  }
}
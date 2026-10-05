#!/usr/bin/env bash
set -u

BASE_URL="${TRACKSPHERE_PUBLIC_URL:-${BASE_URL:-http://localhost:8080}}"
TIMEOUT="${TIMEOUT:-10}"
SKIP_DB="${SKIP_DB:-0}"

if [[ $# -gt 0 ]]; then
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --base-url)
        BASE_URL="$2"; shift 2 ;;
      --skip-db)
        SKIP_DB=1; shift ;;
      --timeout)
        TIMEOUT="$2"; shift 2 ;;
      -h|--help)
        echo "Usage: $0 [--base-url URL] [--skip-db] [--timeout SEC]"
        exit 0 ;;
      *)
        echo "Unknown arg: $1" >&2
        exit 2 ;;
    esac
  done
fi

trimmed="${BASE_URL%/}"

check_json() {
  local url="$1"
  local name="$2"
  local body
  if ! body=$(curl --fail --silent --show-error --max-time "$TIMEOUT" -H 'Accept: application/json' "$url"); then
    echo "FAIL: $name endpoint unavailable at $url" >&2
    return 1
  fi
  if ! python3 - "$body" <<'PY'
import json, sys
text = sys.argv[1]
try:
    json.loads(text)
except Exception as exc:
    print(f"FAIL: payload is not valid JSON: {exc}", file=sys.stderr)
    raise SystemExit(1)
PY
  then
    return 1
  fi
  echo "OK: $name -> $url"
}

check_endpoint() {
  local url="$1"
  local name="$2"
  check_json "$url" "$name"
}

if ! command -v curl >/dev/null 2>&1; then
  echo "FAIL: curl is required for the production smoke check" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 is required to validate JSON payloads" >&2
  exit 1
fi

check_endpoint "$trimmed/api/v1/health" "health"
check_endpoint "$trimmed/api/v1/statusz" "statusz"

if [[ "$SKIP_DB" -eq 0 ]]; then
  if [[ -z "${TRACKSPHERE_DATABASE_URL:-}" ]]; then
    echo "FAIL: TRACKSPHERE_DATABASE_URL is required unless --skip-db is used" >&2
    exit 1
  fi
  if ! command -v psql >/dev/null 2>&1; then
    echo "FAIL: psql is required for database smoke check" >&2
    exit 1
  fi
  if ! PGPASSWORD="$(python3 - <<'PY'
import os
u = os.environ.get('TRACKSPHERE_DATABASE_URL','')
if not u:
    raise SystemExit(1)
# Minimal parser for postgres://user:pass@host/db
if '://' not in u:
    raise SystemExit(1)
rest = u.split('://', 1)[1]
userinfo, rest = rest.split('@', 1)
user, password = userinfo.split(':', 1)
print(password)
PY
)" psql "$TRACKSPHERE_DATABASE_URL" -v ON_ERROR_STOP=1 -Atqc 'SELECT 1;' >/tmp/tracksphere_prod_smoke.sql.out 2>/tmp/tracksphere_prod_smoke.sql.err; then
    echo "FAIL: database smoke check failed" >&2
    cat /tmp/tracksphere_prod_smoke.sql.err >&2 || true
    exit 1
  fi
  echo "OK: database -> ${TRACKSPHERE_DATABASE_URL%/?*}"
fi

echo "PASS: production smoke checks succeeded against $trimmed"
exit 0

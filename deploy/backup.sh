#!/usr/bin/env bash
# deploy/backup.sh — nightly pg_dump sidecar for TrackSphere (P1-1).
# Runs inside the `backup` compose service: dumps the cluster to /backups,
# keeps the last N dumps, and records the run in backup_runs so /statusz
# reports live backup freshness.
#
# Required env: PGHOST PGUSER PGPASSWORD PGDATABASE BACKUP_DIR (default /backups)
# Optional: BACKUP_KEEP (default 7), BACKUP_INTERVAL_S (default 86400).
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/backups}"
KEEP="${BACKUP_KEEP:-7}"
INTERVAL="${BACKUP_INTERVAL_S:-86400}"

mkdir -p "$BACKUP_DIR"

record() { # record <ok true|false> <path> <bytes> <error>
  psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -v ON_ERROR_STOP=1 -c \
    "INSERT INTO backup_runs (finished_at, ok, path, bytes, error) VALUES (now(), $1, '$2', $3, '$4')" \
    > /dev/null || echo "backup ledger write failed (migrations pending?)" >&2
}

while true; do
  STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
  FILE="$BACKUP_DIR/tracksphere-$STAMP.dump"
  echo "[backup] dumping to $FILE"
  if pg_dump -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -Fc -f "$FILE"; then
    BYTES="$(wc -c < "$FILE" | tr -d ' ')"
    record true "$FILE" "$BYTES" ""
    echo "[backup] ok ($BYTES bytes)"
  else
    record false "$FILE" 0 "pg_dump failed"
    echo "[backup] FAILED" >&2
    rm -f "$FILE"
  fi
  # Retention: keep newest $KEEP dumps.
  # shellcheck disable=SC2012
  ls -1t "$BACKUP_DIR"/tracksphere-*.dump 2>/dev/null | tail -n +"$((KEEP + 1))" | xargs -r rm -f
  echo "[backup] sleeping ${INTERVAL}s"
  sleep "$INTERVAL"
done

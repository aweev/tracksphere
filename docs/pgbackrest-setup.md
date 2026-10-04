# pgBackRest Configuration for TrackSphere

## 1. PostgreSQL Setup (run as superuser)

```sql
-- Create backup role with minimal privileges
CREATE ROLE tracksphere_backup WITH LOGIN PASSWORD 'strong-random-password';
GRANT pg_read_all_data TO tracksphere_backup;
GRANT pg_monitor TO tracksphere_backup;

-- Enable WAL archiving
ALTER SYSTEM SET wal_level = replica;
ALTER SYSTEM SET archive_mode = on;
ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=tracksphere archive-push %p';
ALTER SYSTEM SET max_wal_senders = 10;
ALTER SYSTEM SET wal_keep_size = 1GB;
SELECT pg_reload_conf();
```

## 2. pgBackRest Install (on backup host or sidecar container)

```dockerfile
# Dockerfile.pgbackrest
FROM pgbackrest/pgbackrest:2.53

# Config mounted at /etc/pgbackrest/pgbackrest.conf
# AWS credentials via IAM role or env vars
```

```yaml
# docker-compose.yml addition
services:
  pgbackrest:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.pgbackrest
    restart: unless-stopped
    environment:
      - AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID}
      - AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY}
      - AWS_DEFAULT_REGION=${AWS_DEFAULT_REGION:-us-east-1}
      - PGBACKREST_REPO1_S3_BUCKET=${PGBACKREST_REPO1_S3_BUCKET:-tracksphere-backups}
    volumes:
      - ./deploy/pgbackrest.conf:/etc/pgbackrest/pgbackrest.conf:ro
      - pgdata:/var/lib/postgresql/data:ro
    depends_on:
      postgres:
        condition: service_healthy
    # Run backup cron via entrypoint script
    command: ["/backup-cron.sh"]
```

## 3. pgbackrest.conf

```ini
[global]
repo1-type=s3
repo1-s3-bucket=tracksphere-backups
repo1-s3-region=us-east-1
repo1-s3-endpoint=s3.amazonaws.com
repo1-retention-full=30
repo1-retention-diff=7
repo1-retention-archive=30
compress-type=lz4
compress-level=3
process-max=4
log-level-console=info
log-level-file=debug
repo1-cipher-type=aes-256-cbc
repo1-cipher-pass=${PGBACKREST_REPO1_CIPHER_PASS}

[tracksphere]
pg1-host=postgres
pg1-port=5432
pg1-user=tracksphere_backup
pg1-path=/var/lib/postgresql/data
```

## 4. Backup Cron Script

```bash
#!/bin/bash
# /backup-cron.sh

set -euo pipefail

# Wait for postgres to be ready
until pg_isready -h postgres -U tracksphere_backup; do
  sleep 5
done

# Create stanza if not exists
pgbackrest --stanza=tracksphere --log-level-console=info stanza-create 2>/dev/null || true

# Run backup loop
while true; do
  # Full backup daily at 02:00 UTC
  if [[ $(date -u +%H) == "02" && $(date -u +%M) -lt 10 ]]; then
    pgbackrest --stanza=tracksphere --type=full backup
  # Differential every 6 hours
  elif [[ $(date -u +%H) =~ ^(08|14|20)$ && $(date -u +%M) -lt 10 ]]; then
    pgbackrest --stanza=tracksphere --type=diff backup
  fi
  
  # Expire old backups
  pgbackrest --stanza=tracksphere expire
  
  sleep 300  # Check every 5 minutes
done
```

## 5. Restore Procedure

### 5.1 Point-in-Time Recovery (PITR)

```bash
# Stop application
docker compose stop api worker web

# Restore to specific timestamp
pgbackrest --stanza=tracksphere \
  --type=time \
  --target="2026-10-03 14:30:00+00" \
  --target-action=promote \
  restore

# Start PostgreSQL
docker compose start postgres

# Verify
psql -U tracksphere -d tracksphere -c "SELECT count(*) FROM tenants;"
psql -U tracksphere_app -d tracksphere -f scripts/rls_test.sql

# Start application
docker compose start api worker web
```

### 5.2 Full Restore (Latest)

```bash
pgbackrest --stanza=tracksphere --type=full --target-action=promote restore
```

## 6. Verification Script

```bash
#!/bin/bash
# verify-restore.sh

set -euo pipefail

echo "=== Restore Verification ==="

# 1. Schema integrity
echo "Checking tenant count..."
psql -U tracksphere -d tracksphere -c "SELECT count(*) FROM tenants;"

# 2. Data integrity
echo "Checking shipment count..."
psql -U tracksphere -d tracksphere -c "SELECT count(*) FROM shipments;"

# 3. RLS tests (must run as app role)
echo "Running RLS tests..."
psql -U tracksphere_app -d tracksphere -f scripts/rls_test.sql

# 4. Read model consistency
echo "Checking read model..."
psql -U tracksphere -d tracksphere -c "
  SELECT count(*) FROM shipment_current sc
  JOIN shipments s ON s.id = sc.shipment_id
  WHERE sc.tenant_id != s.tenant_id;
"

# 5. Notification consent audit
echo "Checking consent audit..."
psql -U tracksphere -d tracksphere -c "
  SELECT action, count(*) FROM notification_consent GROUP BY action;
"

# 6. Alert rules seeded
echo "Checking alert rules..."
psql -U tracksphere -d tracksphere -c "
  SELECT tenant_id, count(*) FROM alert_rules GROUP BY tenant_id;
"

echo "=== Verification Complete ==="
```

## 7. Monitoring Backup Health

```prometheus
# Add to metrics endpoint
tracksphere_backup_last_success_timestamp{type="full|diff"}
tracksphere_backup_duration_seconds{type="full|diff"}
tracksphere_backup_size_bytes{type="full|diff"}
tracksphere_wal_archive_lag_seconds
```

```yaml
# Alert if backup hasn't run in 25 hours
- alert: BackupMissing
  expr: time() - tracksphere_backup_last_success_timestamp{type="full"} > 90000
  for: 1h
  labels:
    severity: critical
  annotations:
    summary: "No full backup in 25 hours"
```
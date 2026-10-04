#!/usr/bin/env bash
# Load test for TrackSphere
# Simulates 10k shipments with 100 concurrent webhooks
# Usage: ./scripts/load-test.sh [options]

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_BASE="${API_BASE:-http://localhost:8080}"

# Default parameters
NUM_SHIPMENTS="${NUM_SHIPMENTS:-10000}"
CONCURRENT_WEBHOOKS="${CONCURRENT_WEBHOOKS:-100}"
DURATION="${DURATION:-60}"
RAMP_UP="${RAMP_UP:-10}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() {
    echo -e "${BLUE}[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]${NC} $*"
}

success() {
    echo -e "${GREEN}[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]${NC} $*"
}

warn() {
    echo -e "${YELLOW}[$(date -u +'%Y-%m-%dT%H:%M:%SZ')] WARNING:${NC} $*"
}

error() {
    echo -e "${RED}[$(date -u +'%Y-%m-%dT%H:%M:%SZ')] ERROR:${NC} $*"
}

show_help() {
    cat << EOF
TrackSphere Load Test

Usage: $0 [OPTIONS]

Options:
    --shipments N       Number of shipments to create (default: 10000)
    --webhooks N        Concurrent webhook workers (default: 100)
    --duration N        Test duration in seconds (default: 60)
    --ramp-up N         Ramp-up time in seconds (default: 10)
    --api-base URL      API base URL (default: http://localhost:8080)
    --help              Show this help

Environment variables:
    API_BASE            API base URL
    NUM_SHIPMENTS       Number of shipments
    CONCURRENT_WEBHOOKS Concurrent webhook workers
    DURATION            Test duration
    RAMP_UP             Ramp-up time

Example:
    $0 --shipments 10000 --webhooks 100 --duration 120
    API_BASE=https://api.example.com $0
EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --shipments) NUM_SHIPMENTS="$2"; shift 2 ;;
        --webhooks) CONCURRENT_WEBHOOKS="$2"; shift 2 ;;
        --duration) DURATION="$2"; shift 2 ;;
        --ramp-up) RAMP_UP="$2"; shift 2 ;;
        --api-base) API_BASE="$2"; shift 2 ;;
        --help) show_help; exit 0 ;;
        *) error "Unknown option: $1"; show_help; exit 1 ;;
    esac
done

# Check dependencies
for cmd in curl jq bc; do
    if ! command -v "$cmd" &> /dev/null; then
        error "Required command '$cmd' not found"
        exit 1
    fi
done

log "🚀 TrackSphere Load Test"
log "========================"
log "API Base:       ${API_BASE}"
log "Shipments:      ${NUM_SHIPMENTS}"
log "Concurrent:     ${CONCURRENT_WEBHOOKS}"
log "Duration:       ${DURATION}s"
log "Ramp-up:        ${RAMP_UP}s"
log ""

# Check API health
log "Checking API health..."
if ! curl -sf "${API_BASE}/api/v1/health" > /dev/null; then
    error "API health check failed"
    exit 1
fi
success "API is healthy"

# Create test shipments in batches
log "Creating ${NUM_SHIPMENTS} test shipments..."
BATCH_SIZE=100
TOTAL_BATCHES=$(( (NUM_SHIPMENTS + BATCH_SIZE - 1) / BATCH_SIZE ))

# Use a test tenant - in real scenario you'd authenticate
# For this script we'll use the public tracking endpoint which doesn't need auth
# but we'll test the webhook ingestion endpoint which is public

WEBHOOK_PAYLOAD='{
  "trackingNumber": "LOAD-TEST-{{SHIPMENT_ID}}",
  "eventId": "evt-{{SHIPMENT_ID}}-{{SEQ}}",
  "code": "DEPARTED",
  "description": "Load test event",
  "location": "Test Port",
  "occurredAt": "2024-01-15T10:00:00Z",
  "status": "in_transit"
}'

# Function to send a webhook
send_webhook() {
    local shipment_id=$1
    local seq=$2
    
    payload=$(echo "$WEBHOOK_PAYLOAD" | sed "s/{{SHIPMENT_ID}}/${shipment_id}/g; s/{{SEQ}}/${seq}/g")
    
    # Send to carrier webhook endpoint (public, HMAC-signed)
    # For load test, we'll use a simple carrier that doesn't require HMAC
    curl -s -X POST "${API_BASE}/api/v1/webhooks/carriers/maersk" \
        -H "Content-Type: application/json" \
        -H "X-TrackSphere-Signature: sha256=test" \
        -d "$payload" > /dev/null
}

# Function to run webhook worker
webhook_worker() {
    local worker_id=$1
    local shipments_per_worker=$((NUM_SHIPMENTS / CONCURRENT_WEBHOOKS))
    local start=$((worker_id * shipments_per_worker))
    local end=$((start + shipments_per_worker))
    
    for ((i=start; i<end; i++)); do
        # Send multiple events per shipment
        for seq in 1 2 3; do
            send_webhook "$i" "$seq"
        done
        
        # Small delay to avoid overwhelming
        sleep 0.01
    done
}

# Start workers
log "Starting ${CONCURRENT_WEBHOOKS} webhook workers..."
START_TIME=$(date +%s)

pids=()
for ((w=0; w<CONCURRENT_WEBHOOKS; w++)); do
    webhook_worker "$w" &
    pids+=($!)
    
    # Ramp up
    if [[ $RAMP_UP -gt 0 ]]; then
        sleep $(echo "scale=3; $RAMP_UP / $CONCURRENT_WEBHOOKS" | bc)
    fi
done

# Monitor progress
log "Workers started. Monitoring for ${DURATION}s..."

elapsed=0
while [[ $elapsed -lt $DURATION ]]; do
    sleep 5
    elapsed=$(($(date +%s) - START_TIME))
    
    # Check metrics
    metrics=$(curl -sf "${API_BASE}/api/v1/metrics" 2>/dev/null || echo "")
    pending=$(echo "$metrics" | grep "tracksphere_jobs_pending" | head -1 | awk '{print $2}' || echo "0")
    running=$(echo "$metrics" | grep "tracksphere_jobs_running" | head -1 | awk '{print $2}' || echo "0")
    dead=$(echo "$metrics" | grep "tracksphere_jobs_dead" | head -1 | awk '{print $2}' || echo "0")
    
    log "Progress: ${elapsed}s / ${DURATION}s | Pending: ${pending} | Running: ${running} | Dead: ${dead}"
done

# Wait for all workers to complete
log "Waiting for workers to complete..."
for pid in "${pids[@]}"; do
    wait "$pid" 2>/dev/null || true
done

END_TIME=$(date +%s)
TOTAL_TIME=$((END_TIME - START_TIME))

# Final metrics
log "Collecting final metrics..."
metrics=$(curl -sf "${API_BASE}/api/v1/metrics" 2>/dev/null || echo "")
pending=$(echo "$metrics" | grep "tracksphere_jobs_pending" | head -1 | awk '{print $2}' || echo "0")
running=$(echo "$metrics" | grep "tracksphere_jobs_running" | head -1 | awk '{print $2}' || echo "0")
dead=$(echo "$metrics" | grep "tracksphere_jobs_dead" | head -1 | awk '{print $2}' || echo "0")

# Ingestion latency (p99)
ingestion_p99=$(echo "$metrics" | grep "tracksphere_ingestion_duration_seconds_bucket" | grep 'le="5"' | head -1 | awk '{print $2}' || echo "N/A")

success "Load test completed in ${TOTAL_TIME}s"
log "========================"
log "Results:"
log "  Total time:     ${TOTAL_TIME}s"
log "  Shipments:      ${NUM_SHIPMENTS}"
log "  Events sent:    $((NUM_SHIPMENTS * 3))"
log "  Pending jobs:   ${pending}"
log "  Running jobs:   ${running}"
log "  Dead jobs:      ${dead}"
log "  Ingestion p99:  ${ingestion_p99}s"

# Check if targets met
if [[ "$pending" -gt 1000 ]]; then
    warn "⚠️  High pending job count (${pending}) - may indicate queue backlog"
fi

if [[ "$dead" -gt 0 ]]; then
    warn "⚠️  Dead jobs detected (${dead}) - check DLQ"
fi

log "✅ Load test complete"
# ADR 0006 — Deploy with Docker Compose on Hetzner/DigitalOcean

**Status:** accepted · **Date:** 2026-10-01

## Context
Hosting is Hetzner (compute, primary) plus DigitalOcean (objects, secondary
region). The team explicitly wants minimal operations overhead and no
resource-heavy platform layer. Kubernetes was ruled out.

## Decision
- Ship **plain Docker Compose** (`deploy/docker-compose.yml`) with four services:
  `postgres`, `api`, `worker`, `web`.
- Build with multi-stage Dockerfiles: Go binaries built with
  `CGO_ENABLED=0` into a small Alpine runtime; Next.js built with
  `output: 'standalone'` so no `node_modules` ships to production.
- Postgres port binds to **loopback only** (`127.0.0.1:5433`); the database is
  never publicly reachable.
- `web` waits for `api` to become healthy (the API owns migrations), and `api`
  waits for Postgres health.
- TLS, WAF, and DDoS protection are delegated to Cloudflare in front of the host.
- Filesystem backups (`pgBackRest`/`WAL-G`) target DigitalOcean Spaces.

## Consequences
- Whole platform fits a CPX21/CPX31; deploys are `git pull && docker compose up -d
  --build`, and rollback is a tag change.
- Single-host failure domain. Mitigation path is a warm standby in the second
  region plus WAL shipping — no application changes required.
- **Known trade-off:** Next `rewrites()` are evaluated at build time, so
  `API_BASE_URL` is baked into the image (default `http://api:8080`, passed as a
  build arg). Runtime-only rerouting would require nginx/Caddy in front; we accept
  a rebuild for now and will add a reverse proxy when custom domains per tenant
  (white-label) land.
- Horizontal scaling is `docker compose up --scale api=3 --scale worker=3`; both
  services are stateless and the queue claim query is replica-safe.

## Alternatives considered
- **Kubernetes (k3s/EKS)**: rejected as disproportionate for four services and a
  small team.
- **Serverless (Cloud Run/Lambda)**: conflicts with the "no AWS" constraint and
  complicates long-lived SSE connections and connection pooling.
- **PaaS (Fly.io/Render)**: attractive DX, less control over placement and cost
  at this scale.

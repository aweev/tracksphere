# Regions & data residency (P3)

TrackSphere is single-region per deployment: one Postgres, one Compose
stack, one `TRACKSPHERE_REGION` label. Residency is a deployment choice, not
a code branch.

## Labels

| Value | Meaning |
|---|---|
| `us` (default) | Americas deployment |
| `eu` | EU deployment (GDPR Art. 44+: data stays in the Union) |

The region ships in `/api/v1/health`, `/api/v1/statusz` and the SOC 2
evidence pack (`dataResidency`), so auditors and customers can verify it at
runtime — not from a slide.

## Standing up EU

```bash
# Same Compose, EU host + EU Postgres volume. Only these differ:
TRACKSPHERE_REGION=eu
POSTGRES_PASSWORD=...   # fresh secret, never copied from US
TRACKSPHERE_SECRET_KEY=...        # fresh
TRACKSPHERE_CARRIER_WEBHOOK_SECRET=...
```

1. Provision the host with the provider's EU datacenter (e.g. Hetzner
   Falkenstein, DO Frankfurt/Amsterdam).
2. `docker compose up -d --build`, run `docs/STAGING.md` §3 verbatim.
3. Point `eu.tracksphere.example.com` (Cloudflare) at it.
4. Record the sub-processor + region in the customer DPA (template with
   legal; the evidence pack's `dataResidency` field is the technical proof).

## Rules

- Regions never replicate to each other (no cross-border transfer by
  construction). A tenant lives in exactly one region.
- Backups stay in-region (same-provider object storage, same jurisdiction).
- `GET /account/export` + `DELETE /account` give Art. 20/17 portability and
  erasure without ops tickets.

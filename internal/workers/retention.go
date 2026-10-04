package workers

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionSweep prunes append-only audit tables past their windows.
// jobs has its own archiver (queue.ArchiveOld); everything else lands here.
// Runs daily from the worker. Counts are logged by the caller.
func RetentionSweep(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var total int64
	queries := []string{
		// Processed inbox rows (keep failures for forensics).
		`DELETE FROM webhook_inbox WHERE processed = true AND received_at < now() - interval '90 days'`,
		// Old notification audit rows.
		`DELETE FROM notifications WHERE created_at < now() - interval '180 days'`,
		// Auth audit older than a year.
		`DELETE FROM auth_events WHERE created_at < now() - interval '365 days'`,
		// Delivered outbound rows (keep failures 90d via updated? created).
		`DELETE FROM webhook_deliveries WHERE status = 'delivered' AND created_at < now() - interval '90 days'`,
		// Stale idempotency keys (24h replay window + margin).
		`DELETE FROM idempotency_keys WHERE created_at < now() - interval '7 days'`,
	}
	for _, q := range queries {
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

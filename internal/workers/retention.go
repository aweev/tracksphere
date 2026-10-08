package workers

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

// RetentionSweep prunes append-only audit tables past their windows.
// jobs has its own archiver (queue.ArchiveOld); everything else lands here.
// Runs daily from the worker. Counts are logged by the caller.
func RetentionSweep(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var total int64

	// webhook_inbox and auth_events carry no RLS by design, so they prune
	// globally in one statement each.
	global := []string{
		// Inbox rows past 90 days, failures included: tampered-signature
		// floods mint rows (audit-before-verify), so "keep failures forever"
		// is an unbounded disk grant to attackers. 90d retains forensics.
		`DELETE FROM webhook_inbox WHERE received_at < now() - interval '90 days'
		  AND (processed = true OR error IS NOT NULL)`,
		// Auth audit older than a year.
		`DELETE FROM auth_events WHERE created_at < now() - interval '365 days'`,
		// Backup ledger past 90 days (statusz reads max(finished_at)).
		`DELETE FROM backup_runs WHERE finished_at < now() - interval '90 days'`,
	}
	for _, q := range global {
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}

	// Everything below is FORCE RLS. These used to run on the raw pool with no
	// tenant pin, which deletes exactly zero rows: the request succeeds, the
	// count is logged as if something happened, and the compliance document
	// advertises retention windows that were never enforced. Pin each tenant
	// in turn instead.
	perTenant := []string{
		// Old notification audit rows.
		`DELETE FROM notifications WHERE created_at < now() - interval '180 days'`,
		// Delivered outbound rows (keep failures 90d via updated? created).
		`DELETE FROM webhook_deliveries WHERE status = 'delivered' AND created_at < now() - interval '90 days'`,
		// Stale idempotency keys (24h replay window + margin).
		`DELETE FROM idempotency_keys WHERE created_at < now() - interval '7 days'`,
	}
	tenants, err := listTenantIDs(ctx, pool)
	if err != nil {
		return total, err
	}
	for _, tenantID := range tenants {
		var pruned int64
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			for _, q := range perTenant {
				tag, err := tx.Exec(ctx, q)
				if err != nil {
					return err
				}
				pruned += tag.RowsAffected()
			}
			return nil
		}); err != nil {
			return total, err
		}
		total += pruned
	}
	return total, nil
}

// listTenantIDs resolves every tenant under the system flag. It is a bootstrap
// lookup in the same class as the carrier-webhook resolver: no tenant context
// exists yet, so the tenants policy admits the read only via app.system.
func listTenantIDs(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

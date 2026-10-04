// Command seed loads development/demo data: one tenant, demo users, shipments
// in various states with timelines and alerts. Idempotent — safe to re-run
// (skips when the demo tenant slug exists).
//
// Usage: seed [--reset]   (--reset wipes the demo tenant first)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/db"
)

const demoSlug = "acme-logistics"

func main() {
	reset := len(os.Args) > 1 && os.Args[1] == "--reset"

	cfg, err := config.Load()
	if err != nil {
		fatal("config", err)
	}
	ctx := context.Background()

	if err := db.Migrate(ctx, cfg.MigrationsURL); err != nil {
		fatal("migrate", err)
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.WorkerConcurrency, cfg.APIWorkers)
	if err != nil {
		fatal("connect", err)
	}
	defer pool.Close()

	// --reset: delete demo tenant under the system flag (bootstrap path —
	// no tenant context exists for a row we're about to remove).
	if reset {
		if cfg.Env == "production" {
			fatal("reset", fmt.Errorf("refusing --reset in production (would delete tenant %q)", demoSlug))
		}
		if err := withSystem(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM tenants WHERE slug=$1`, demoSlug)
			return err
		}); err != nil {
			fatal("reset", err)
		}
		fmt.Println("reset: demo tenant removed")
	}

	var exists bool
	if err := withSystem(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM tenants WHERE slug=$1)`, demoSlug).Scan(&exists)
	}); err != nil {
		fatal("check", err)
	}
	if exists {
		fmt.Println("seed: already present, nothing to do (use --reset to rebuild)")
		return
	}

	tenantID, err := seedTenantAndUsers(ctx, pool, cfg.SecretKeys)
	if err != nil {
		fatal("tenant", err)
	}
	n, err := seedShipments(ctx, pool, tenantID)
	if err != nil {
		fatal("shipments", err)
	}
	fmt.Printf("seed: tenant %q created with %d shipments\n", demoSlug, n)
	fmt.Println("login → demo@tracksphere.dev / DemoPassw0rd!")
}

// seedTenantAndUsers creates the demo org plus owner and member accounts.
// The owner has a known TOTP secret (sealed) with MFA DISABLED so local login
// works without an authenticator; flip totp_enabled manually to test MFA.
func seedTenantAndUsers(ctx context.Context, pool *pgxpool.Pool, secretKeys [][]byte) (uuid.UUID, error) {
	var tenantID uuid.UUID
	// Tenant creation is a bootstrap write: system flag, no tenant context.
	err := withSystem(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO tenants (name, slug, plan) VALUES ('Acme Logistics', $1, 'growth')
			RETURNING id`, demoSlug).Scan(&tenantID); err != nil {
			return err
		}
		// Pin the freshly created tenant for the remainder of the tx (users
		// have no RLS, but pinning keeps the transaction least-privileged and
		// explicit about intent).
		return db.SetTenant(ctx, tx, tenantID)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert tenant: %w", err)
	}

	users := []struct {
		email, name, role, password string
	}{
		{"demo@tracksphere.dev", "Dara Ops", "owner", "DemoPassw0rd!"},
		{"agent@tracksphere.dev", "Agent Kay", "member", "AgentPassw0rd!"},
	}
	for _, u := range users {
		hash, err := auth.HashPassword(u.password)
		if err != nil {
			return uuid.Nil, err
		}
		// Well-known dev-only TOTP secret (sealed with the same key the API
		// uses so MFA can be enabled against it). totp_enabled stays false.
		sealed, err := auth.SealMulti(secretKeys, []byte("JBSWY3DPEHPK3PXP"))
		if err != nil {
			return uuid.Nil, err
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (tenant_id, email, name, password_hash, role, totp_secret)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			tenantID, u.email, u.name, hash, u.role, sealed); err != nil {
			return uuid.Nil, fmt.Errorf("insert user %s: %w", u.email, err)
		}
	}
	return tenantID, nil
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "seed: %s: %v\n", msg, err)
	os.Exit(1)
}

// now is a seam so seeded timestamps stay relative to runtime.
var now = func() time.Time { return time.Now() }

// withSystem runs fn in a transaction holding the RLS system flag. Used for
// bootstrap operations (tenant create/delete, existence checks) that
// necessarily happen before/without a tenant context.
func withSystem(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
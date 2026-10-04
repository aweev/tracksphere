package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationLockID int64 = 0x5452414b535048 // "TRACKSPH"

// Migrate applies all embedded *.sql migrations in filename order, exactly
// once each, under a Postgres advisory lock so concurrent api/worker boots
// cannot race. Migrations are plain SQL files named 000001_description.sql.
func Migrate(ctx context.Context, migrationsURL string) error {
	pool, err := Open(ctx, migrationsURL, 4, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Release()

	// The pool's AfterConnect installs statement_timeout='5s' on every
	// connection. A migration connection must never inherit that. The
	// advisory-lock wait below blocks while another replica is migrating, and
	// index builds on a real dataset routinely exceed five seconds; either
	// would abort the migration with "canceling statement due to statement
	// timeout", and since cmd/api and cmd/worker os.Exit(1) on a migration
	// error, every trailing replica would crash-loop on deploy. This once
	// bit the 000009 GIN trigram builds. The lock_timeout bound stays so a
	// genuinely stuck lock still fails loudly instead of wedging boot.
	if _, err := conn.Exec(ctx, `SET statement_timeout = '0'; SET lock_timeout = '30s'`); err != nil {
		return fmt.Errorf("relax migration timeouts: %w", err)
	}

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID) }()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if noTransaction(body) {
			if err := applyUntransacted(ctx, conn, string(body), version); err != nil {
				return err
			}
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", version, err)
		}
	}
	return nil
}

// noTransaction reports whether a migration opts out of the per-file
// transaction. The marker `-- migrate:no-transaction` must be the first line
// of the file. Such a migration is needed for statements Postgres refuses to
// run inside a transaction block (CREATE INDEX CONCURRENTLY, partitioning
// ATTACH of a live table) and for anything that must hold its locks
// transaction-by-transaction instead of all at once.
func noTransaction(body []byte) bool {
	first, _, _ := strings.Cut(string(body), "\n")
	return strings.TrimSpace(first) == "-- migrate:no-transaction"
}

// applyUntransacted runs a no-transaction migration directly on the migration
// connection. Unlike the transactional path there is no rollback: if the body
// fails, whatever statements already committed stay committed, and the version
// is not recorded, so the next boot re-runs the whole file. No-transaction
// migrations MUST therefore be written re-runnable (IF NOT EXISTS everywhere,
// ADD CONSTRAINT ... NOT VALID + separate VALIDATE, index builds that can be
// safely repeated) — otherwise a failure leaves a half-applied schema that can
// never advance.
func applyUntransacted(ctx context.Context, conn *pgxpool.Conn, body, version string) error {
	if _, err := conn.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply %s: %w", version, err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("record %s: %w", version, err)
	}
	return nil
}

// AppliedVersions reports which migration versions are recorded (used by tests
// to assert the schema is current).
func AppliedVersions(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LatestVersion returns the highest migration version embedded in the binary.
func LatestVersion() (string, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		return "", fmt.Errorf("no embedded migrations")
	}
	sort.Strings(names)
	n := names[len(names)-1]
	return strings.TrimSuffix(strings.TrimPrefix(n, "migrations/"), ".sql"), nil
}

// ParseVersion extracts the numeric prefix of a migration file name.
func ParseVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	return strconv.Atoi(parts[0])
}

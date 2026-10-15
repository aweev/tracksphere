package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/notify"
)

// Send-after-commit outbox (P0 fix).
//
// WHY: every send path used to INSERT the audit row and call the provider
// Send inside ONE tenant transaction. Holding a DB transaction open across
// SMTP/Twilio network I/O is wrong in both directions: a slow provider
// holds row locks until statement_timeout kills the statement, and a
// Send-OK + tx-abort replays the job into a DUPLICATE send with a second
// audit row. The pattern is now:
//
//  1. claimSendTx — short, DB-only, inside the business transaction.
//     Inserts the audit row with a deterministic dedupe_key; ON CONFLICT the
//     existing row is re-examined. Returns owned=true only when this attempt
//     must transmit (new row, or claimed-but-unsent row from a crashed try).
//  2. flushSends — AFTER the business transaction commits. Transmits each
//     owned message, then stamps sent_at in its own short transaction.
//  3. Retry safety — a job retry re-claims: sent rows are skipped, unsent
//     rows are re-owned and transmitted. The only residual duplicate window
//     is a crash between provider-accept and the sent_at stamp (narrow,
//     logged, metric-counted); providers without native idempotency cannot
//     do better than at-least-once there.

// pendingSend is one message claimed in-transaction, transmitted post-commit.
type pendingSend struct {
	TenantID   uuid.UUID
	ShipmentID uuid.UUID // uuid.Nil when the message is tenant-level (digests)
	Channel    string
	To         string
	Subject    string
	Body       string
	Provider   string
	DedupeKey  string
}

// sendKey derives a deterministic idempotency key. Parts must identify one
// LOGICAL notification: retries of the same job share every part, distinct
// notifications differ in at least one (nonce, alert id, week start…).
func sendKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:16])
}

// claimSendTx inserts the delivery audit row. owned tells the caller whether
// this attempt must transmit after commit; fresh tells whether this attempt
// created the row (false on a retry that re-owns an unsent row — budget and
// ledger side-effects must only happen on fresh claims, never on replays).
func claimSendTx(ctx context.Context, tx pgx.Tx, s pendingSend) (owned, fresh bool, err error) {
	var shipID any
	if s.ShipmentID != uuid.Nil {
		shipID = s.ShipmentID
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO notifications
			(tenant_id, shipment_id, channel, recipient, subject, body, provider, dedupe_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'sent')
		ON CONFLICT (tenant_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`,
		s.TenantID, shipID, s.Channel, s.To, s.Subject, s.Body, s.Provider, s.DedupeKey)
	if err != nil {
		return false, false, err
	}
	if tag.RowsAffected() == 1 {
		return true, true, nil
	}
	// Conflict: someone claimed this key before. Re-own it only if it never
	// got transmitted (a crashed first attempt); a sent row stays sent.
	var sent bool
	if err := tx.QueryRow(ctx, `
		SELECT sent_at IS NOT NULL FROM notifications
		WHERE tenant_id=$1 AND dedupe_key=$2`, s.TenantID, s.DedupeKey).Scan(&sent); err != nil {
		return false, false, err
	}
	return !sent, false, nil
}

// flushSends transmits owned messages AFTER the claiming transaction has
// committed, then stamps sent_at per message in short tenant transactions.
// A failed Send returns an error so the job retries; the claim row stays
// unsent and is re-owned on retry (never silently dropped, never double
// audit-rowed).
func flushSends(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, sends []pendingSend) error {
	var failed int
	for _, s := range sends {
		sender := notify.ForChannel(log, s.Channel)
		if err := sender.Send(ctx, s.Channel, s.To, s.Subject, s.Body); err != nil {
			log.Warn("outbox send failed, will retry", "channel", s.Channel,
				"tenant", s.TenantID, "err", err)
			failed++
			continue
		}
		if err := db.WithTenant(ctx, pool, s.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE notifications SET sent_at=now()
				WHERE tenant_id=$1 AND dedupe_key=$2 AND sent_at IS NULL`,
				s.TenantID, s.DedupeKey)
			return err
		}); err != nil {
			// Transmitted but unstamped: the retry will resend (narrow crash
			// window, loudly logged). Never mark locally — the ledger is
			// the source of truth, not memory.
			log.Error("outbox sent but stamp failed, retry may duplicate",
				"tenant", s.TenantID, "key", s.DedupeKey, "err", err)
			failed++
			continue
		}
	}
	if failed > 0 {
		return &sendError{failed: failed, total: len(sends)}
	}
	return nil
}

type sendError struct {
	failed, total int
}

func (e *sendError) Error() string {
	return fmt.Sprintf("outbox: %d of %d sends failed", e.failed, e.total)
}

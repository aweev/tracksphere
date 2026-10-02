package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

// buildVersion is stamped by the linker in release builds (-X).
var buildVersion = "dev"

// timeoutContext derives a deadline-bound context from the request context.
func timeoutContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// listAlerts loads alerts for a tenant with the given status ("" = all).
func (s *Server) listAlerts(ctx context.Context, tenantID uuid.UUID, status string) ([]model.Alert, error) {
	var out []model.Alert
	err := db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		query := `
			SELECT id, shipment_id, kind, severity, title, message, status,
			       created_at, resolved_at
			FROM alerts`
		args := []any{}
		if status != "" && status != "all" {
			args = append(args, status)
			query += ` WHERE status=$1`
		}
		query += ` ORDER BY created_at DESC LIMIT 100`

		rs, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var a model.Alert
			if err := rs.Scan(&a.ID, &a.ShipmentID, &a.Kind, &a.Severity,
				&a.Title, &a.Message, &a.Status, &a.CreatedAt, &a.ResolvedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rs.Err()
	})
	if out == nil {
		out = make([]model.Alert, 0)
	}
	return out, err
}

// resolveAlert marks an alert resolved (tenant-scoped, idempotent).
func (s *Server) resolveAlert(ctx context.Context, tenantID, userID, alertID uuid.UUID) error {
	return db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE alerts SET status='resolved', resolved_at=now(), resolved_by=$2
			WHERE id=$1 AND status='open'`, alertID, userID)
		return err
	})
}
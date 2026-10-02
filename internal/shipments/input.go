package shipments

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

// ListInput filters the shipments list.
type ListInput struct {
	Status  string
	Carrier string
	Search  string // matches tracking number or reference
	Limit   int
	Offset  int
}

// List returns a page plus the total matching count (tenant-scoped).
func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, in ListInput) ([]model.Shipment, int64, error) {
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	var (
		rows  []model.Shipment
		total int64
	)
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		where := []string{"1=1"}
		args := []any{}
		if in.Status != "" {
			args = append(args, in.Status)
			where = append(where, fmt.Sprintf("status=$%d", len(args)))
		}
		if in.Carrier != "" {
			args = append(args, in.Carrier)
			where = append(where, fmt.Sprintf("carrier=$%d", len(args)))
		}
		if in.Search != "" {
			args = append(args, "%"+strings.ToLower(in.Search)+"%")
			where = append(where, fmt.Sprintf(
				"(lower(tracking_number) LIKE $%d OR lower(reference) LIKE $%d)",
				len(args), len(args)))
		}
		cond := strings.Join(where, " AND ")

		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM shipments WHERE `+cond, args...).Scan(&total); err != nil {
			return err
		}

		args = append(args, in.Limit, in.Offset)
		rs, err := tx.Query(ctx, `
			SELECT `+shipmentCols+` FROM shipments WHERE `+cond+`
			ORDER BY created_at DESC
			LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)),
			args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			s, err := scanShipment(rs)
			if err != nil {
				return err
			}
			rows = append(rows, *s)
		}
		return rs.Err()
	})
	return rows, total, err
}

// Events returns the timeline of one shipment (tenant-scoped).
func (r *Repository) Events(ctx context.Context, tenantID, shipmentID uuid.UUID) ([]model.ShipmentEvent, error) {
	var out []model.ShipmentEvent
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = queryEvents(ctx, tx, shipmentID)
		return err
	})
	return out, err
}

// ByTrackingNumber resolves the public portal lookup. Runs with
// app.public_access='on' (no tenant) — the RLS branch only unlocks
// is_public=true rows; private shipments return ErrNotFound.
func (r *Repository) ByTrackingNumber(ctx context.Context, tracking string) (*model.Shipment, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetPublicAccess(ctx, tx); err != nil {
		return nil, err
	}
	ship, err := scanShipment(tx.QueryRow(ctx, `
		SELECT `+shipmentCols+` FROM shipments
		WHERE tracking_number=$1 AND is_public=true`, strings.TrimSpace(tracking)))
	if err != nil {
		return nil, err
	}
	return ship, tx.Commit(ctx)
}

// PublicEvents is the public-portal timeline (same public_access contract;
// the policy re-verifies the parent shipment is public).
func (r *Repository) PublicEvents(ctx context.Context, shipmentID uuid.UUID) ([]model.ShipmentEvent, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetPublicAccess(ctx, tx); err != nil {
		return nil, err
	}
	events, err := queryEvents(ctx, tx, shipmentID)
	if err != nil {
		return nil, err
	}
	return events, tx.Commit(ctx)
}

// eventQuerier is satisfied by pgx.Tx and *pgxpool.Pool.
type eventQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryEvents(ctx context.Context, q eventQuerier, shipmentID uuid.UUID) ([]model.ShipmentEvent, error) {
	rs, err := q.Query(ctx, `
		SELECT id, shipment_id, carrier, code, description, location,
		       lat, lng, occurred_at, received_at, source
		FROM shipment_events WHERE shipment_id=$1
		ORDER BY occurred_at ASC`, shipmentID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []model.ShipmentEvent
	for rs.Next() {
		var e model.ShipmentEvent
		if err := rs.Scan(&e.ID, &e.ShipmentID, &e.Carrier, &e.Code,
			&e.Description, &e.Location, &e.Lat, &e.Lng,
			&e.OccurredAt, &e.ReceivedAt, &e.Source); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// Dashboard aggregates stats for the tenant dashboard (tenant-scoped).
func (r *Repository) Dashboard(ctx context.Context, tenantID uuid.UUID) (*model.DashboardStats, error) {
	var out model.DashboardStats
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
				coalesce((SELECT active_shipments    FROM tenant_dashboard WHERE tenant_id=$1),0),
				coalesce((SELECT delivered_shipments FROM tenant_dashboard WHERE tenant_id=$1),0),
				coalesce((SELECT exception_shipments FROM tenant_dashboard WHERE tenant_id=$1),0),
				coalesce((SELECT overdue_shipments   FROM tenant_dashboard WHERE tenant_id=$1),0),
				coalesce((SELECT count(*) FROM alerts WHERE tenant_id=$1 AND status='open'),0)`,
			tenantID).Scan(&out.ActiveShipments, &out.DeliveredShipments,
			&out.ExceptionShipments, &out.OverdueShipments, &out.OpenAlerts)
	})
	return &out, err
}
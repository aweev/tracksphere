package shipments

import (
	"context"
	"encoding/json"
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
	// Sort orders the result. "risk" (default for the control tower) puts the
	// most consequential work first; "recent" is the old created_at DESC.
	Sort string
	// HasPosition restricts to shipments with a known coordinates pair, for the
	// fleet map. Implemented as an EXISTS rather than a join so the count stays
	// a single indexed scan.
	HasPosition bool
	// RiskTier filters to a single risk tier.
	RiskTier string
}

// listSelect joins the read model so the list carries the risk score, staleness,
// ETA provenance and customer-notified flag without a per-row subquery.
const listSelect = `
	SELECT s.id, s.tenant_id, s.tracking_number, s.reference, s.carrier, s.mode,
	       s.origin, s.destination, s.status, s.eta, s.shipped_at, s.delivered_at,
	       s.is_public, s.created_at, s.updated_at,
	       c.risk_score, c.risk_tier, c.risk_breakdown, c.stale_hours, c.open_alerts, c.critical_alerts,
	       c.eta_source, c.eta_confidence, c.value_at_risk, c.customer_notified,
	       c.dwell_hours, c.expected_dwell_hours, c.dwell_ratio,
	       (SELECT e.lat FROM shipment_events e
	         WHERE e.shipment_id = s.id AND e.lat IS NOT NULL AND e.lng IS NOT NULL
	         ORDER BY e.occurred_at DESC LIMIT 1),
	       (SELECT e.lng FROM shipment_events e
	         WHERE e.shipment_id = s.id AND e.lat IS NOT NULL AND e.lng IS NOT NULL
	         ORDER BY e.occurred_at DESC LIMIT 1)
	FROM shipments s
	LEFT JOIN shipment_current c ON c.shipment_id = s.id`

func scanListRow(rs pgx.Rows) (*model.Shipment, error) {
	var s model.Shipment
	// shipment_current is LEFT JOINed, so all of its columns are NULL-able.
	// Each uses a pointer temp rather than the model's own pointer field,
	// because pgx cannot decode NULL into **T.
	var etaSource *string
	var openAlerts, criticalAlerts *int
	var riskScore *int
	var riskTier *string
	var breakdown []byte
	var staleHours, valueAtRisk *float64
	var customerNotified *bool
	if err := rs.Scan(&s.ID, &s.TenantID, &s.TrackingNumber, &s.Reference,
		&s.Carrier, &s.Mode, &s.Origin, &s.Destination, &s.Status, &s.ETA,
		&s.ShippedAt, &s.DeliveredAt, &s.IsPublic, &s.CreatedAt, &s.UpdatedAt,
		&riskScore, &riskTier, &breakdown, &staleHours, &openAlerts, &criticalAlerts,
		&etaSource, &s.ETAConfidence, &valueAtRisk, &customerNotified,
		&s.DwellHours, &s.ExpectedDwellHours, &s.DwellRatio,
		&s.Lat, &s.Lng); err != nil {
		return nil, err
	}
	if etaSource != nil {
		s.ETASource = *etaSource
	}
	s.RiskScore, s.RiskTier, s.StaleHours = riskScore, riskTier, staleHours
	s.OpenAlerts, s.CriticalAlerts = openAlerts, criticalAlerts
	s.ValueAtRisk, s.CustomerNotified = valueAtRisk, customerNotified
	// Passthrough of exactly what Score() computed. '{}' (rows predating the
	// column or never refreshed) decodes to a zero breakdown, which renders
	// as "no risk factors" — honest for a row with no computed terms.
	if len(breakdown) > 0 {
		s.RiskBreakdown = append(json.RawMessage(nil), breakdown...)
	}
	return &s, nil
}

// List returns a page plus the total matching count (tenant-scoped).
func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, in ListInput) ([]model.Shipment, int64, error) {
	if in.Limit <= 0 || in.Limit > 250 {
		in.Limit = 25
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	var (
		rows  []model.Shipment
		total int64
	)
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		where := []string{"1=1"}
		args := []any{}
		// Each filter appends its value then references its 1-based position, so
		// the SQL never contains a user-controlled fragment.
		if in.Status != "" {
			args = append(args, in.Status)
			where = append(where, fmt.Sprintf("s.status=$%d", len(args)))
		}
		if in.Carrier != "" {
			args = append(args, in.Carrier)
			where = append(where, fmt.Sprintf("s.carrier=$%d", len(args)))
		}
		if in.RiskTier != "" {
			args = append(args, in.RiskTier)
			where = append(where, fmt.Sprintf("c.risk_tier=$%d", len(args)))
		}
		if in.Search != "" {
			args = append(args, "%"+strings.ToLower(in.Search)+"%")
			where = append(where, fmt.Sprintf(
				"(lower(s.tracking_number) LIKE $%d OR lower(s.reference) LIKE $%d)",
				len(args), len(args)))
		}
		if in.HasPosition {
			where = append(where, `EXISTS (
				SELECT 1 FROM shipment_events e
				WHERE e.shipment_id = s.id AND e.lat IS NOT NULL AND e.lng IS NOT NULL)`)
		}
		cond := strings.Join(where, " AND ")

		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM shipments s
			 LEFT JOIN shipment_current c ON c.shipment_id = s.id
			 WHERE `+cond, args...).Scan(&total); err != nil {
			return err
		}

		// Sort. Risk-first is the operational ordering: a to-do list by
		// consequence, not a database view by enum. Shipments with no read-model
		// row yet (never swept) sort last rather than disappearing.
		order := "s.created_at DESC"
		switch in.Sort {
		case "risk":
			order = `coalesce(c.risk_score, -1) DESC, s.updated_at DESC`
		case "stale":
			order = `c.stale_hours DESC NULLS FIRST`
		case "eta":
			order = `s.eta ASC NULLS LAST`
		}

		args = append(args, in.Limit, in.Offset)
		rs, err := tx.Query(ctx,
			listSelect+` WHERE `+cond+`
			ORDER BY `+order+`
			LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)),
			args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			s, err := scanListRow(rs)
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
// Tracking numbers are UNIQUE per tenant, so the same number can exist in
// several tenants (forwarding/3PL). The portal is unauthenticated and has no
// tenant context, so ties break by most-recently-updated. Carrier-scoped
// public URLs (/track/{carrier}/{tracking}) land with white-label portals.
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
		WHERE tracking_number=$1 AND is_public=true
		ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(tracking)))
	if err != nil {
		return nil, err
	}
	return ship, tx.Commit(ctx)
}

// GetByTracking resolves a tracking number inside one tenant (authenticated
// surfaces: MCP, ops tools). Unlike ByTrackingNumber it never crosses tenants:
// the lookup runs under WithTenant so FORCE RLS admits only the caller's rows
// and anything else is ErrNotFound (no cross-tenant oracle).
func (r *Repository) GetByTracking(ctx context.Context, tenantID uuid.UUID, tracking string) (*model.Shipment, error) {
	var out *model.Shipment
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		s, err := scanShipment(tx.QueryRow(ctx, `
			SELECT `+shipmentCols+` FROM shipments
			WHERE tracking_number=$1
			ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(tracking)))
		if err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
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

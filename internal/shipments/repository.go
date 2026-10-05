// Package shipments owns the shipment aggregate: persistence, the event
// ingestion transaction (webhook → timeline → status → jobs → NOTIFY) and
// query APIs for the dashboard and public portal.
package shipments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

// ErrNotFound is returned when a shipment does not exist in the caller's tenant.
var ErrNotFound = errors.New("shipment not found")

// ErrDuplicateTracking is returned when tracking number already exists.
var ErrDuplicateTracking = errors.New("tracking number already exists")

// Repository persists shipments. All tenant-scoped methods go through
// db.WithTenant so FORCE RLS is satisfied.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds a repository to the pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const shipmentCols = `id, tenant_id, tracking_number, reference, carrier, mode,
	origin, destination, status, eta, shipped_at, delivered_at, is_public,
	created_at, updated_at, value_at_risk`

func scanShipment(row pgx.Row) (*model.Shipment, error) {
	var s model.Shipment
	err := row.Scan(&s.ID, &s.TenantID, &s.TrackingNumber, &s.Reference, &s.Carrier,
		&s.Mode, &s.Origin, &s.Destination, &s.Status, &s.ETA, &s.ShippedAt,
		&s.DeliveredAt, &s.IsPublic, &s.CreatedAt, &s.UpdatedAt, &s.ValueAtRisk)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateInput is the payload for creating a shipment.
type CreateInput struct {
	TrackingNumber string
	Reference      string
	Carrier        string
	Mode           string
	Origin         string
	Destination    string
	IsPublic       *bool
	// Declared cargo value in the tenant's reporting currency. Nil means the
	// forwarder did not supply one, which is distinct from zero and must never
	// be imputed — see the ValueKnown contract on readmodel.Input.
	ValueAtRisk *float64
}

// ErrInvalidInput marks validation failures surfaced as HTTP 400.
var ErrInvalidInput = errors.New("invalid input")

// Validate checks CreateInput against domain rules.
func (in CreateInput) Validate() error {
	if strings.TrimSpace(in.TrackingNumber) == "" {
		return fmt.Errorf("%w: trackingNumber is required", ErrInvalidInput)
	}
	if !model.ValidMode(in.Mode) {
		return fmt.Errorf("%w: mode must be ocean|air|road|rail", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Carrier) == "" {
		return fmt.Errorf("%w: carrier is required", ErrInvalidInput)
	}
	if in.ValueAtRisk != nil && *in.ValueAtRisk < 0 {
		return fmt.Errorf("%w: valueAtRisk cannot be negative", ErrInvalidInput)
	}
	return nil
}

// Create inserts a shipment for tenantID. New shipments are private unless
// IsPublic is explicitly set (secure default since 000006).
func (r *Repository) Create(ctx context.Context, tenantID, userID uuid.UUID, in CreateInput) (*model.Shipment, error) {
	var out *model.Shipment
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		isPublic := false
		if in.IsPublic != nil {
			isPublic = *in.IsPublic
		}
		var createdBy any = userID
		if userID == uuid.Nil {
			createdBy = nil // system paths (commerce webhooks) have no user
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO shipments
				(tenant_id, tracking_number, reference, carrier, mode,
				 origin, destination, status, is_public, created_by, value_at_risk)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'booked',$8,$9,$10)
			RETURNING `+shipmentCols,
			tenantID, strings.TrimSpace(in.TrackingNumber), in.Reference,
			in.Carrier, in.Mode, in.Origin, in.Destination, isPublic, createdBy,
			in.ValueAtRisk)
		s, err := scanShipment(row)
		if err != nil {
			return err
		}
		out = s
		return nil
	})
	if err != nil && isUniqueViolation(err) {
		return nil, ErrDuplicateTracking
	}
	return out, err
}

// Get fetches one shipment inside a tenant.
func (r *Repository) Get(ctx context.Context, tenantID, id uuid.UUID) (*model.Shipment, error) {
	var out *model.Shipment
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		s, err := scanShipment(tx.QueryRow(ctx,
			`SELECT `+shipmentCols+` FROM shipments WHERE id=$1`, id))
		if err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}

// UpdateInput is the mutable subset for PATCH /shipments/{id}.
// Pointers distinguish "absent" from zero values.
type UpdateInput struct {
	IsPublic *bool
	Status   *string
	// Nil leaves the stored value untouched; an explicit pointer (even to a
	// small number) replaces it. There is no way to clear back to unknown via
	// the API — unknown is the absence of data, not a state to return to.
	ValueAtRisk *float64
}

// Validate checks UpdateInput against domain rules.
func (in UpdateInput) Validate() error {
	if in.IsPublic == nil && in.Status == nil && in.ValueAtRisk == nil {
		return fmt.Errorf("%w: nothing to update (isPublic, status, valueAtRisk)", ErrInvalidInput)
	}
	if in.Status != nil && !model.ValidStatus(*in.Status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidInput, *in.Status)
	}
	if in.ValueAtRisk != nil && *in.ValueAtRisk < 0 {
		return fmt.Errorf("%w: valueAtRisk cannot be negative", ErrInvalidInput)
	}
	return nil
}

// Update patches visibility and/or status for one tenant shipment.
// Returns ErrNotFound when the id is unknown or belongs to another tenant
// (RLS withholds the row → zero rows affected → 404, no enumeration).
func (r *Repository) Update(ctx context.Context, tenantID, id uuid.UUID, in UpdateInput) (*model.Shipment, error) {
	var out *model.Shipment
	err := db.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		sets := []string{}
		args := []any{id}
		if in.IsPublic != nil {
			args = append(args, *in.IsPublic)
			sets = append(sets, fmt.Sprintf("is_public=$%d", len(args)))
		}
		if in.Status != nil {
			args = append(args, *in.Status)
			sets = append(sets, fmt.Sprintf("status=$%d", len(args)))
		}
		if in.ValueAtRisk != nil {
			args = append(args, *in.ValueAtRisk)
			sets = append(sets, fmt.Sprintf("value_at_risk=$%d", len(args)))
		}
		s, err := scanShipment(tx.QueryRow(ctx,
			`UPDATE shipments SET `+strings.Join(sets, ", ")+
				`, updated_at=now(), needs_refresh=true WHERE id=$1 RETURNING `+shipmentCols,
			args...))
		if err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

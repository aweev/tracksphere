package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

type demoEvent struct {
	code, desc, loc string
	ago             time.Duration
	status          string
	eta             *time.Duration // optional ETA revision (+duration from now)
}

type demoAlert struct {
	kind, severity, title, msg string
}

type demoShipment struct {
	tracking, ref, carrier, mode, origin, dest, status string
	events                                              []demoEvent
	etaIn                                              time.Duration // eta = now + etaIn
	openAlert                                          *demoAlert
}

var demoShipments = []demoShipment{
	{
		tracking: "TS-8842-LAG", ref: "PO-2026-0114", carrier: "maersk",
		mode: "ocean", origin: "Shanghai, CN", dest: "Lagos, NG",
		status: "in_transit", etaIn: 72 * time.Hour,
		events: []demoEvent{
			{"BOOKED", "Booking confirmed", "Shanghai, CN", 96 * time.Hour, "", nil},
			{"DEPARTED", "Vessel departed origin port", "Yangshan Terminal", 48 * time.Hour, "in_transit", nil},
		},
	},
	{
		tracking: "TS-5519-FRA", ref: "PO-2026-0098", carrier: "dhl",
		mode: "air", origin: "Atlanta, US", dest: "Frankfurt, DE",
		status: "at_customs", etaIn: 9 * time.Hour,
		events: []demoEvent{
			{"BOOKED", "Shipment created", "Atlanta, US", 30 * time.Hour, "", nil},
			{"DEPARTED", "Flight departed", "ATL", 20 * time.Hour, "in_transit", nil},
			{"ARRIVED", "Arrived at destination airport", "FRA", 4 * time.Hour, "at_customs", nil},
			{"CUSTOMS_HOLD", "Routine customs inspection", "FRA CargoCity", 2 * time.Hour, "", nil},
		},
		openAlert: &demoAlert{
			"customs_hold", "critical", "Customs hold detected",
			"Carrier reported a customs hold. Clearance documents may be required.",
		},
	},
	{
		tracking: "TS-2207-LOS", ref: "PO-2026-0131", carrier: "fedex",
		mode: "road", origin: "Abuja, NG", dest: "Lagos, NG",
		status: "out_for_delivery", etaIn: 3 * time.Hour,
		events: []demoEvent{
			{"BOOKED", "Label created", "Abuja, NG", 26 * time.Hour, "", nil},
			{"DEPARTED", "Departed sort facility", "Abuja Hub", 10 * time.Hour, "in_transit", nil},
			{"OUT_FOR_DELIVERY", "With delivery courier", "Lagos Island", 1 * time.Hour, "out_for_delivery", nil},
		},
	},
	{
		tracking: "TS-1094-NBO", ref: "PO-2026-0077", carrier: "maersk",
		mode: "ocean", origin: "Rotterdam, NL", dest: "Nairobi, KE",
		status: "delivered", etaIn: 0,
		events: []demoEvent{
			{"BOOKED", "Booking confirmed", "Rotterdam, NL", 240 * time.Hour, "", nil},
			{"DEPARTED", "Vessel departed", "Rotterdam", 190 * time.Hour, "in_transit", nil},
			{"ARRIVED", "Arrived at Mombasa", "Mombasa, KE", 60 * time.Hour, "at_customs", nil},
			{"DELIVERED", "Delivered to consignee", "Nairobi, KE", 6 * time.Hour, "delivered", nil},
		},
	},
	{
		tracking: "TS-7731-IST", ref: "PO-2026-0142", carrier: "cma-cgm",
		mode: "ocean", origin: "Ho Chi Minh, VN", dest: "Istanbul, TR",
		status: "exception", etaIn: -2 * time.Hour, // already past due
		events: []demoEvent{
			{"BOOKED", "Booking confirmed", "HCMC, VN", 300 * time.Hour, "", nil},
			{"DEPARTED", "Vessel departed", "Cat Lai", 220 * time.Hour, "in_transit", nil},
			{"ETA_REVISED", "Rerouted due to weather", "South China Sea", 8 * time.Hour, "exception", nil},
		},
		openAlert: &demoAlert{
			"delay", "critical", "Shipment in exception",
			"Carrier flagged this shipment as an exception.",
		},
	},
	{
		tracking: "TS-6620-YUL", ref: "PO-2026-0150", carrier: "ups",
		mode: "air", origin: "Chicago, US", dest: "Montreal, CA",
		status: "booked", etaIn: 96 * time.Hour,
		events: []demoEvent{
			{"BOOKED", "Shipment created", "Chicago, US", 2 * time.Hour, "", nil},
		},
	},
}

// seedShipments inserts demo shipments with timelines and open alerts.
// All writes run with the tenant pinned so FORCE RLS is satisfied.
func seedShipments(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (int, error) {
	var ownerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE tenant_id=$1 AND role='owner'`, tenantID).
		Scan(&ownerID); err != nil {
		return 0, fmt.Errorf("find owner: %w", err)
	}

	inserted := 0
	for _, ds := range demoShipments {
		var eta any
		if ds.status == "delivered" {
			eta = nil
		} else if ds.etaIn != 0 {
			eta = now().Add(ds.etaIn)
		} else {
			eta = now().Add(48 * time.Hour)
		}

		err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var shipmentID uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO shipments
					(tenant_id, tracking_number, reference, carrier, mode,
					 origin, destination, status, eta, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				RETURNING id`,
				tenantID, ds.tracking, ds.ref, ds.carrier, ds.mode,
				ds.origin, ds.dest, ds.status, eta, ownerID).
				Scan(&shipmentID); err != nil {
				return err
			}

			for i, ev := range ds.events {
				occurred := now().Add(-ev.ago)
				if _, err := tx.Exec(ctx, `
					INSERT INTO shipment_events
						(tenant_id, shipment_id, carrier, code, description, location,
						 occurred_at, source, dedup_key)
					VALUES ($1,$2,$3,$4,$5,$6,$7,'system',$8)`,
					tenantID, shipmentID, ds.carrier, ev.code, ev.desc, ev.loc,
					occurred, fmt.Sprintf("seed:%s:%d", ds.tracking, i)); err != nil {
					return err
				}
			}

			if ds.openAlert != nil {
				if _, err := tx.Exec(ctx, `
					INSERT INTO alerts (tenant_id, shipment_id, kind, severity, title, message)
					VALUES ($1,$2,$3,$4,$5,$6)
					ON CONFLICT (shipment_id, kind) WHERE status='open' DO NOTHING`,
					tenantID, shipmentID, ds.openAlert.kind, ds.openAlert.severity,
					ds.openAlert.title, ds.openAlert.msg); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return inserted, fmt.Errorf("shipment %s: %w", ds.tracking, err)
		}
		inserted++
	}
	return inserted, nil
}
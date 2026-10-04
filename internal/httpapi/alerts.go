package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/readmodel"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// buildVersion is stamped by the linker in release builds (-X).
var buildVersion = "dev"

// slaMinutesFor returns the tenant's SLA target for a severity, in minutes.
// Used when an alert is raised so the queue shows a countdown, not an age.
func slaMinutesFor(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, severity string) int {
	// Column is chosen from a fixed switch below, never from request input.
	col := "sla_info_minutes"
	switch severity {
	case "critical":
		col = "sla_critical_minutes"
	case "warning":
		col = "sla_warning_minutes"
	}
	var mins int
	if err := tx.QueryRow(ctx, `SELECT `+col+` FROM tenants WHERE id=$1`, tenantID).Scan(&mins); err != nil {
		return 1440
	}
	if mins <= 0 {
		return 1440
	}
	return mins
}

// timeoutContext derives a deadline-bound context from the request context.
func timeoutContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// listAlerts loads alerts for a tenant with the given status ("" = all),
// enriched from the shipment_current read model so the exception queue can show
// age, SLA pressure and risk without N+1 fetches.
//
// The queue is ordered the way an operator works it: worst severity first, then
// soonest due. snoozed_until is excluded from the default view but reachable
// with ?include=snoozed — deferring work must not mean hiding it.
func (s *Server) listAlerts(ctx context.Context, tenantID uuid.UUID, status string) ([]model.Alert, error) {
	return s.listAlertsFiltered(ctx, tenantID, status, false)
}

func (s *Server) listAlertsFiltered(ctx context.Context, tenantID uuid.UUID, status string, includeSnoozed bool) ([]model.Alert, error) {
	out := []model.Alert{}
	err := db.WithTenant(ctx, poolOf(s), tenantID, func(tx pgx.Tx) error {
		query := `
			SELECT a.id, a.shipment_id, a.kind, a.severity, a.title, a.message, a.status,
			       a.created_at, a.detected_at, a.last_seen_at, a.resolved_at,
			       a.assigned_to, au.name, a.assigned_at, a.acknowledged_at,
			       a.due_at, a.escalated_at, a.snoozed_until,
			       a.root_cause, a.note, a.resolution, a.resolved_by, a.value_at_risk,
			       s.tracking_number, s.status, s.eta,
			       sc.risk_score, sc.risk_tier, sc.stale_hours,
			       sc.open_alerts, sc.customer_notified
			FROM alerts a
			JOIN shipments s ON s.id = a.shipment_id
			LEFT JOIN users au ON au.id = a.assigned_to
			LEFT JOIN shipment_current sc ON sc.shipment_id = a.shipment_id`
		args := []any{}
		where := []string{}
		if status != "" && status != "all" {
			args = append(args, status)
			where = append(where, "a.status=$"+strconv.Itoa(len(args)))
		}
		if !includeSnoozed {
			where = append(where, "(a.snoozed_until IS NULL OR a.snoozed_until < now())")
		}
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ")
		}
		query += ` ORDER BY
		    CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		    a.due_at NULLS LAST,
		    a.detected_at DESC
		  LIMIT 200`

		rs, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var a model.Alert
			// root_cause, note and resolution are all nullable columns: an
			// untouched exception has none of them set.
			var assigneeName, rootCause, note, resolution *string
			// shipment_current is LEFT JOINed, so every column from it is
			// NULL-able: a shipment the sweep has not reached yet must not fail
			// the whole queue query. Each gets a pointer temp rather than
			// scanning into the model's own pointer field, because pgx cannot
			// decode NULL into **T.
			var openAlerts *int
			var riskScore *int
			var riskTier *string
			var staleHours *float64
			var customerNotified *bool
			if err := rs.Scan(&a.ID, &a.ShipmentID, &a.Kind, &a.Severity,
				&a.Title, &a.Message, &a.Status, &a.CreatedAt, &a.DetectedAt, &a.LastSeenAt,
				&a.ResolvedAt, &a.AssignedTo, &assigneeName, &a.AssignedAt, &a.AcknowledgedAt,
				&a.DueAt, &a.EscalatedAt, &a.SnoozedUntil,
				&rootCause, &note, &resolution, &a.ResolvedBy, &a.ValueAtRisk,
				&a.TrackingNumber, &a.ShipmentStatus, &a.ShipmentETA,
				&riskScore, &riskTier, &staleHours,
				&openAlerts, &customerNotified); err != nil {
				return err
			}
			if openAlerts != nil {
				a.OpenAlertCount = *openAlerts
			}
			a.RiskScore, a.StaleHours = riskScore, staleHours
			// RiskTier and CustomerNotified are value fields in the contract;
			// a missing read-model row means "no tier yet" and "not told",
			// which are the safe zero values, so absent stays absent.
			if riskTier != nil {
				a.RiskTier = *riskTier
			}
			if customerNotified != nil {
				a.CustomerNotified = *customerNotified
			}
			if assigneeName != nil {
				a.AssignedToName = *assigneeName
			}
			if rootCause != nil {
				a.RootCause = *rootCause
			}
			if note != nil {
				a.Note = *note
			}
			if resolution != nil {
				a.Resolution = *resolution
			}
			out = append(out, a)
		}
		return rs.Err()
	})
	return out, err
}

// poolOf is a readability alias; the tenant-scoped transaction helper is the
// only way a tenant query may run (RLS is FORCE'd — see ADR 0004).
func poolOf(s *Server) *pgxpool.Pool { return s.pool }

// resolveAlert marks an alert resolved (tenant-scoped, idempotent).
// Returns the parent shipment id for audit, or shipments.ErrNotFound when
// the id is unknown or belongs to another tenant (RLS withholds the row →
// 0 rows → 404, no enumeration).
//
// The SLA loop closes properly here: due_at, resolution and resolved_by are all
// set, and the shipment's customer_notified flag is cleared so the queue knows
// the customer still has not been told.
func (s *Server) resolveAlert(ctx context.Context, tenantID, userID, alertID uuid.UUID, resolution string) (uuid.UUID, error) {
	var shipmentID uuid.UUID
	if resolution == "" {
		resolution = "condition_cleared"
	}
	err := db.WithTenant(ctx, poolOf(s), tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE alerts
			SET status='resolved', resolved_at=now(), resolved_by=$2,
			    resolution=$3, snoozed_until=NULL
			WHERE id=$1 AND status='open' RETURNING shipment_id`, alertID, userID, resolution).
			Scan(&shipmentID)
		if err == nil {
			// Re-derive the read model so open-alert counts and the risk
			// score reflect the closure immediately.
			if err := refreshShipmentCurrent(ctx, tx, shipmentID); err != nil {
				return err
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM alerts WHERE id=$1)`, alertID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return shipments.ErrNotFound
		}
		return tx.QueryRow(ctx, `SELECT shipment_id FROM alerts WHERE id=$1`, alertID).Scan(&shipmentID)
	})
	return shipmentID, err
}

// assignAlertRequest is the body of POST /alerts/{id}/assign.
type assignAlertRequest struct {
	// UserID assigns to a specific teammate. Empty self-assigns.
	UserID *uuid.UUID `json:"userId,omitempty"`
	// SnoozeMinutes defers the alert without closing it. Deferring must not
	// mean pretending the problem does not exist.
	SnoozeMinutes int    `json:"snoozeMinutes,omitempty"`
	RootCause     string `json:"rootCause,omitempty"`
	Note          string `json:"note,omitempty"`
}

// handleAssignAlert POST /api/v1/alerts/{id}/assign
//
// Claiming an exception is the difference between a work queue and a list.
// Unowned work is invisible work: in a team, an alert that belongs to everyone
// is worked by nobody.
func (s *Server) handleAssignAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid alert id")
		return
	}
	var req assignAlertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	target := user.ID
	if req.UserID != nil && *req.UserID != uuid.Nil {
		target = *req.UserID
	}
	if req.RootCause != "" && !validRootCause(req.RootCause) {
		writeError(w, http.StatusBadRequest, "invalid_input", "Unknown root cause")
		return
	}
	if req.SnoozeMinutes < 0 || req.SnoozeMinutes > 60*24*30 {
		writeError(w, http.StatusBadRequest, "invalid_input", "snoozeMinutes out of range")
		return
	}

	var (
		shipmentID uuid.UUID
		snooze     *time.Time
	)
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		// The assignee must be in this tenant. Without this check an admin
		// could assign work to a user id from another tenant (ids are not
		// guessable, but this is cheap and makes the invariant explicit).
		if req.UserID != nil && *req.UserID != uuid.Nil {
			var sameTenant bool
			if err := tx.QueryRow(r.Context(),
				`SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND tenant_id=$2)`,
				target, user.TenantID).Scan(&sameTenant); err != nil {
				return err
			}
			if !sameTenant {
				return shipments.ErrNotFound
			}
		}
		if req.SnoozeMinutes > 0 {
			t := time.Now().Add(time.Duration(req.SnoozeMinutes) * time.Minute)
			snooze = &t
		}
		err := tx.QueryRow(r.Context(), `
			UPDATE alerts
			SET assigned_to=$2, assigned_at=now(), acknowledged_at=COALESCE(acknowledged_at, now()),
			    snoozed_until=COALESCE($3, snoozed_until),
			    root_cause=COALESCE(NULLIF($4,''), root_cause),
			    note=COALESCE(NULLIF($5,''), note)
			WHERE id=$1 AND status='open'
			RETURNING shipment_id`, id, target, snooze, req.RootCause, req.Note).
			Scan(&shipmentID)
		if err == pgx.ErrNoRows {
			return shipments.ErrNotFound
		}
		return err
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, shipmentID, "alert.assign", map[string]any{
		"alertId": id.String(), "assignedTo": target.String(),
		"snoozeMinutes": req.SnoozeMinutes, "rootCause": req.RootCause,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "assignedTo": target})
}

type noteAlertRequest struct {
	Note      string `json:"note"`
	RootCause string `json:"rootCause,omitempty"`
}

// handleNoteAlert POST /api/v1/alerts/{id}/note — internal context for the team.
func (s *Server) handleNoteAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid alert id")
		return
	}
	var req noteAlertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" || len(note) > 2000 {
		writeError(w, http.StatusBadRequest, "invalid_input", "note required (max 2000 chars)")
		return
	}
	if req.RootCause != "" && !validRootCause(req.RootCause) {
		writeError(w, http.StatusBadRequest, "invalid_input", "Unknown root cause")
		return
	}
	user := currentUser(r)
	var shipmentID uuid.UUID
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			UPDATE alerts
			SET note=$2,
			    root_cause=COALESCE(NULLIF($3,''), root_cause),
			    acknowledged_at=COALESCE(acknowledged_at, now())
			WHERE id=$1 AND status='open'
			RETURNING shipment_id`, id, note, req.RootCause).Scan(&shipmentID)
		if err == pgx.ErrNoRows {
			return shipments.ErrNotFound
		}
		return err
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, shipmentID, "alert.note", map[string]any{
		"alertId": id.String(), "rootCause": req.RootCause,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var rootCauses = map[string]bool{
	"documentation": true, "customs_duty": true, "customs_processing": true,
	"carrier_delay": true, "port_congestion": true, "weather": true,
	"capacity": true, "missed_connection": true, "blank_sailing": true,
	"carrier_error": true, "shipper_delay": true, "other": true,
}

func validRootCause(s string) bool { return rootCauses[s] }

// handleResolveAlertRequest carries the closure classification.
func (s *Server) resolveHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid alert id")
		return
	}
	var req assignAlertRequest
	// The body is optional: a plain "resolve" needs no payload, and rejecting an
	// empty body would break the one-click path in the queue.
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	if req.RootCause != "" && !validRootCause(req.RootCause) {
		writeError(w, http.StatusBadRequest, "invalid_input", "Unknown root cause")
		return
	}
	user := currentUser(r)
	shipmentID, err := s.resolveAlertWithCause(r.Context(), user.TenantID, user.ID, id, req.RootCause, req.Note)
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, shipmentID, "alert.resolve", map[string]any{
		"alertId": id.String(), "rootCause": req.RootCause,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// resolveAlertWithCause closes an alert and records why. The root cause is the
// closure feedback that feeds carrier scorecards and ETA calibration — without
// it the operator's work produces no number and the loop never closes.
func (s *Server) resolveAlertWithCause(ctx context.Context, tenantID, userID, alertID uuid.UUID, rootCause, note string) (uuid.UUID, error) {
	var shipmentID uuid.UUID
	err := db.WithTenant(ctx, poolOf(s), tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE alerts
			SET status='resolved', resolved_at=now(), resolved_by=$2,
			    resolution='customer_notified',
			    root_cause=COALESCE(NULLIF($3,''), root_cause),
			    note=COALESCE(NULLIF($4,''), note),
			    snoozed_until=NULL
			WHERE id=$1 AND status='open'
			RETURNING shipment_id`, alertID, userID, rootCause, note).Scan(&shipmentID)
		if err == pgx.ErrNoRows {
			return shipments.ErrNotFound
		}
		if err != nil {
			return err
		}
		if rootCause == "" {
			// Record the omission rather than dropping the signal.
			_, _ = tx.Exec(ctx,
				`UPDATE alerts SET root_cause='other' WHERE id=$1 AND root_cause IS NULL`, alertID)
		}
		return refreshShipmentCurrent(ctx, tx, shipmentID)
	})
	return shipmentID, err
}

// refreshShipmentCurrent recomputes the read-model row after an alert changes,
// so open-alert counts and the risk score are never stale behind the UI.
func refreshShipmentCurrent(ctx context.Context, tx pgx.Tx, shipmentID uuid.UUID) error {
	return readmodel.Refresh(ctx, tx, shipmentID)
}

// notifyAlert broadcasts an alert change so open dashboards update.
func notifyAlertChange(ctx context.Context, tx pgx.Tx, tenantID, shipmentID uuid.UUID) error {
	notice, _ := json.Marshal(map[string]any{
		"type": "alert.changed", "tenant_id": tenantID, "shipment_id": shipmentID,
	})
	_, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice))
	return err
}
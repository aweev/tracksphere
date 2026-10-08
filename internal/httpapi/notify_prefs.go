package httpapi

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// Notification prefs: quiet hours, digest cadence, interrupt caps, and the
// metered-channel kill-switch (sms/whatsapp default OFF — Twilio spend and
// per-country consent exposure require an explicit admin opt-in).

type notifyPrefsView struct {
	Timezone        string `json:"timezone"`
	QuietStart      int    `json:"quietHoursStart"`
	QuietEnd        int    `json:"quietHoursEnd"`
	DigestHour      int    `json:"digestHour"`
	InterruptCap    int    `json:"interruptHourlyCap"`
	CooldownMinutes int    `json:"perShipmentCooldownMinutes"`
	SMSEnabled      bool   `json:"smsEnabled"`
	WhatsAppEnabled bool   `json:"whatsappEnabled"`
}

func scanPrefs(row pgx.Row) (notifyPrefsView, error) {
	var v notifyPrefsView
	err := row.Scan(&v.Timezone, &v.QuietStart, &v.QuietEnd, &v.DigestHour,
		&v.InterruptCap, &v.CooldownMinutes, &v.SMSEnabled, &v.WhatsAppEnabled)
	return v, err
}

const prefsCols = `timezone, quiet_hours_start, quiet_hours_end, digest_hour,
	interrupt_hourly_cap, per_shipment_cooldown_minutes, sms_enabled, whatsapp_enabled`

// handleGetNotifyPrefs GET /api/v1/notify/prefs (member+) — tenant prefs with
// schema defaults when the sweep has not seeded a row yet.
func (s *Server) handleGetNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var v notifyPrefsView
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		var err error
		v, err = scanPrefs(tx.QueryRow(r.Context(),
			`SELECT `+prefsCols+` FROM notification_prefs WHERE tenant_id=$1`, user.TenantID))
		return err
	})
	if err != nil {
		// No row yet (fresh tenant before first sweep): report defaults.
		v = notifyPrefsView{Timezone: "UTC", QuietStart: 22, QuietEnd: 7,
			DigestHour: 8, InterruptCap: 5, CooldownMinutes: 360}
	}
	writeJSON(w, http.StatusOK, v)
}

type notifyPrefsPatch struct {
	SMSEnabled      *bool `json:"smsEnabled"`
	WhatsAppEnabled *bool `json:"whatsappEnabled"`
}

// handleUpdateNotifyPrefs PATCH /api/v1/notify/prefs (admin+) — currently the
// metered-channel switch only. Quiet hours/caps stay sweep-managed until a UI
// needs them; growing this struct later is backward compatible.
func (s *Server) handleUpdateNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	var req notifyPrefsPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	var v notifyPrefsView
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO notification_prefs (tenant_id) VALUES ($1)
			ON CONFLICT (tenant_id) DO NOTHING`, user.TenantID); err != nil {
			return err
		}
		sets := `updated_at=now()`
		args := []any{user.TenantID}
		if req.SMSEnabled != nil {
			args = append(args, *req.SMSEnabled)
			sets += `, sms_enabled=$` + strconv.Itoa(len(args))
		}
		if req.WhatsAppEnabled != nil {
			args = append(args, *req.WhatsAppEnabled)
			sets += `, whatsapp_enabled=$` + strconv.Itoa(len(args))
		}
		var err error
		v, err = scanPrefs(tx.QueryRow(r.Context(),
			`UPDATE notification_prefs SET `+sets+` WHERE tenant_id=$1 RETURNING `+prefsCols, args...))
		return err
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	// No shipment_audit row: prefs are tenant-level, and the row's updated_at
	// is the change record.
	writeJSON(w, http.StatusOK, v)
}

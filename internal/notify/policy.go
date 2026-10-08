package notify

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Metered reports whether a channel costs money per message (and therefore
// demands both verified consent and an explicit tenant opt-in).
func Metered(channel string) bool {
	return channel == "sms" || channel == "whatsapp"
}

// ChannelAllowed checks the tenant kill-switch for metered channels
// (notification_prefs.sms_enabled/whatsapp_enabled, default OFF). Email and
// unknown channels are unmetered and always allowed. A missing prefs row
// means disabled. tx must already be tenant-pinned: prefs is FORCE RLS and
// reads fail closed without the pin.
func ChannelAllowed(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, channel string) bool {
	if !Metered(channel) {
		return true
	}
	col := "sms_enabled"
	if channel == "whatsapp" {
		col = "whatsapp_enabled"
	}
	var on bool
	if err := tx.QueryRow(ctx,
		`SELECT `+col+` FROM notification_prefs WHERE tenant_id=$1`, tenantID).Scan(&on); err != nil || !on {
		return false
	}
	return true
}

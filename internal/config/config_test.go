package config

import (
	"testing"
)

// Production must never auto-migrate: schema moves only via the explicit
// migrate service after a verified backup.
func TestProductionRequiresAutoMigrateZero(t *testing.T) {
	t.Setenv("TRACKSPHERE_ENV", "production")
	t.Setenv("TRACKSPHERE_AUTO_MIGRATE", "1")
	t.Setenv("TRACKSPHERE_DATABASE_URL", "postgres://x/x?sslmode=disable")
	t.Setenv("TRACKSPHERE_SECRET_KEY", "01234567890123456789012345678901")
	t.Setenv("TRACKSPHERE_CARRIER_WEBHOOK_SECRET", "01234567890123456789012345678901")
	if _, err := Load(); err == nil {
		t.Fatal("expected Load to reject AUTO_MIGRATE=1 in production")
	}
	t.Setenv("TRACKSPHERE_AUTO_MIGRATE", "0")
	if _, err := Load(); err != nil {
		t.Fatalf("AUTO_MIGRATE=0 in production must load: %v", err)
	}
}

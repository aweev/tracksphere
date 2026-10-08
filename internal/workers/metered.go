package workers

import (
	"context"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/notify"
)

// meteredAllowed is the worker-side entry to the shared channel policy
// (notify.ChannelAllowed, internal/notify/policy.go): one definition of
// "metered" and one kill-switch read, shared with the subscribe path in
// httpapi. tx must already be tenant-pinned.
func meteredAllowed(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, channel string) bool {
	return notify.ChannelAllowed(ctx, tx, tenantID, channel)
}

// publicBaseURL is the externally visible origin used in customer-facing
// links. From TRACKSPHERE_PUBLIC_URL (the API cannot infer its own origin
// behind Cloudflare); empty when unconfigured — links degrade to relative
// paths rather than a fictional domain.
func publicBaseURL() string {
	return strings.TrimRight(os.Getenv("TRACKSPHERE_PUBLIC_URL"), "/")
}

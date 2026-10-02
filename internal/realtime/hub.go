// Package realtime implements the Server-Sent Events hub fed by PostgreSQL
// LISTEN/NOTIFY. API instances subscribe to the 'tracksphere' channel; each
// message carries a tenant_id so the hub can fan out only to that tenant's
// subscribers (multi-tenant fan-out isolation).
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is the wire format pushed to browsers (SSE `data:` payload).
type Event struct {
	Type       string          `json:"type"` // shipment.updated | alert.created | ...
	TenantID   uuid.UUID       `json:"-"`
	ShipmentID *uuid.UUID      `json:"shipmentId,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type subscriber struct {
	id     uuid.UUID
	tenant uuid.UUID
	ch     chan Event
}

// Hub manages SSE subscribers.
type Hub struct {
	mu   sync.RWMutex
	subs map[uuid.UUID]*subscriber // by subscriber id
	log  *slog.Logger
}

// NewHub creates an empty hub.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{subs: map[uuid.UUID]*subscriber{}, log: log}
}

// Subscribe registers a listener for one tenant. The returned channel is
// closed when the context is cancelled. Buffer prevents slow clients from
// blocking the fan-out; overflow drops the subscriber (browser auto-reconnects).
func (h *Hub) Subscribe(ctx context.Context, tenantID uuid.UUID) <-chan Event {
	sub := &subscriber{id: uuid.New(), tenant: tenantID, ch: make(chan Event, 64)}
	h.mu.Lock()
	h.subs[sub.id] = sub
	h.mu.Unlock()
	h.log.Debug("sse subscribe", "tenant", tenantID, "sub", sub.id, "total", len(h.subs))

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if cur, ok := h.subs[sub.id]; ok {
			delete(h.subs, sub.id)
			close(cur.ch)
		}
		h.mu.Unlock()
	}()
	return sub.ch
}

// Publish fans an event out to all subscribers of its tenant.
func (h *Hub) Publish(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, sub := range h.subs {
		if sub.tenant != ev.TenantID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer: drop subscriber to protect the hub.
			go h.drop(sub.id)
		}
	}
}

func (h *Hub) drop(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(sub.ch)
	}
}

// Count reports active subscribers (exposed at /api/v1/health).
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// payload from pg_notify
type notifyEnvelope struct {
	Type      string     `json:"type"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	ShipmentID *uuid.UUID `json:"shipment_id,omitempty"`
	Data      any        `json:"data,omitempty"`
}

// Run listens on the Postgres channel until ctx is cancelled, republishing
// every notification to the hub. Reconnects automatically on failure.
func (h *Hub) Run(ctx context.Context, pool *pgxpool.Pool) {
	for ctx.Err() == nil {
		if err := h.listenOnce(ctx, pool); err != nil && ctx.Err() == nil {
			h.log.Warn("realtime listener lost, reconnecting", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `LISTEN tracksphere`); err != nil {
		return err
	}
	h.log.Info("realtime listening on channel 'tracksphere'")
	defer h.log.Info("realtime listener detached")

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var env notifyEnvelope
		if err := json.Unmarshal([]byte(notification.Payload), &env); err != nil {
			h.log.Warn("bad realtime payload", "err", err)
			continue
		}
		data, _ := json.Marshal(env.Data)
		h.Publish(Event{
			Type:       env.Type,
			TenantID:   env.TenantID,
			ShipmentID: env.ShipmentID,
			Payload:    data,
		})
	}
}

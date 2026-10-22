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

	"github.com/tracksphere/tracksphere/internal/metrics"
)

// Event is the wire format pushed to browsers (SSE `data:` payload).
type Event struct {
	Type       string          `json:"type"` // shipment.updated | alert.created | ...
	TenantID   uuid.UUID       `json:"-"`
	ShipmentID *uuid.UUID      `json:"shipmentId,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type subscriber struct {
	id        uuid.UUID
	tenant    uuid.UUID
	shipment  *uuid.UUID
	userAgent string
	ipHash    string
	ch        chan Event
}

// Hub manages SSE subscribers.
type Hub struct {
	mu   sync.RWMutex
	subs map[uuid.UUID]*subscriber // by subscriber id
	log  *slog.Logger
	pool *pgxpool.Pool // for recording subscriptions
}

// NewHub creates an empty hub.
func NewHub(log *slog.Logger, pool *pgxpool.Pool) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		subs: map[uuid.UUID]*subscriber{},
		log:  log,
		pool: pool,
	}
}

// Subscribe registers a listener for one tenant. The returned channel is
// closed when the context is cancelled. Buffer prevents slow clients from
// blocking the fan-out; overflow drops the subscriber (browser auto-reconnects).
func (h *Hub) Subscribe(ctx context.Context, tenantID uuid.UUID, userAgent, ipHash string) <-chan Event {
	return h.subscribe(ctx, tenantID, nil, userAgent, ipHash)
}

// SubscribeShipment registers a listener for a single shipment within a tenant.
// Used by the public tracking portal, which has resolved exactly one shipment
// and must never see any other customer's events.
func (h *Hub) SubscribeShipment(ctx context.Context, tenantID, shipmentID uuid.UUID, userAgent, ipHash string) <-chan Event {
	return h.subscribe(ctx, tenantID, &shipmentID, userAgent, ipHash)
}

func (h *Hub) subscribe(ctx context.Context, tenantID uuid.UUID, shipment *uuid.UUID, userAgent, ipHash string) <-chan Event {
	sub := &subscriber{
		id:        uuid.New(),
		tenant:    tenantID,
		shipment:  shipment,
		userAgent: userAgent,
		ipHash:    ipHash,
		ch:        make(chan Event, 64),
	}
	h.mu.Lock()
	h.subs[sub.id] = sub
	total := len(h.subs)
	h.mu.Unlock()
	metrics.SSESubscribersActive.WithLabelValues(tenantID.String()).Inc()
	h.log.Debug("sse subscribe", "tenant", tenantID, "shipment", shipment, "sub", sub.id, "total", total)

	// Record subscription in DB (best-effort, async)
	if h.pool != nil {
		go h.recordSubscription(sub)
	}

	go func() {
		<-ctx.Done()
		h.remove(sub.id)
		// Record disconnect
		if h.pool != nil {
			go h.recordDisconnect(sub.id)
		}
	}()
	return sub.ch
}

// remove deletes a subscriber and decrements the per-tenant gauge. Both exit
// paths (context cancellation and the slow-consumer drop) must go through it
// so the gauge can never drift from the live map.
func (h *Hub) remove(id uuid.UUID) {
	h.mu.Lock()
	sub, ok := h.subs[id]
	if ok {
		delete(h.subs, id)
		close(sub.ch)
	}
	h.mu.Unlock()
	if ok {
		metrics.SSESubscribersActive.WithLabelValues(sub.tenant.String()).Dec()
	}
}

func (h *Hub) recordSubscription(sub *subscriber) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shipmentID := ""
	if sub.shipment != nil {
		shipmentID = sub.shipment.String()
	}
	_, err := h.pool.Exec(ctx, `
		INSERT INTO sse_subscriptions (id, tenant_id, shipment_id, subscriber_id, user_agent, ip_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING`,
		sub.id, sub.tenant, shipmentID, sub.id, sub.userAgent, sub.ipHash)
	if err != nil {
		h.log.Debug("sse subscription record failed", "err", err)
	}
}

func (h *Hub) recordDisconnect(subID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.pool.Exec(ctx, `
		UPDATE sse_subscriptions SET disconnected_at = now()
		WHERE subscriber_id = $1`, subID)
	if err != nil {
		h.log.Debug("sse disconnect record failed", "err", err)
	}
}

// Publish fans an event out to all subscribers of its tenant. Fan-out is
// non-blocking; a slow consumer (channel full) is dropped. The fan-out timer
// and published counter cover the server-side segment of "SSE is a hint":
// delivery to the browser additionally depends on its reconnect loop.
func (h *Hub) Publish(ev Event) {
	start := time.Now()
	h.mu.RLock()
	for _, sub := range h.subs {
		if sub.tenant != ev.TenantID {
			continue
		}
		// Shipment-scoped subscribers only see their own shipment.
		if sub.shipment != nil {
			if ev.ShipmentID == nil || *ev.ShipmentID != *sub.shipment {
				continue
			}
		}
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer: drop subscriber to protect the hub.
			go h.drop(sub.id)
		}
	}
	h.mu.RUnlock()
	metrics.SSEEventsPublishedTotal.WithLabelValues(ev.Type, ev.TenantID.String()).Inc()
	metrics.SSEDeliveryDuration.Observe(time.Since(start).Seconds())
}

func (h *Hub) drop(id uuid.UUID) {
	h.remove(id)
}

// Count reports active subscribers (exposed at /api/v1/health).
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// payload from pg_notify
type notifyEnvelope struct {
	Type       string     `json:"type"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	ShipmentID *uuid.UUID `json:"shipment_id,omitempty"`
	Data       any        `json:"data,omitempty"`
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

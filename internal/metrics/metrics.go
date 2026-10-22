package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Ingestion metrics
	IngestionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tracksphere_ingestion_duration_seconds",
		Help:    "Time from carrier webhook receipt to shipment visibility in API",
		Buckets: prometheus.DefBuckets,
	}, []string{"carrier", "source"})

	WebhookReceivedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_webhook_received_total",
		Help: "Total carrier webhooks received",
	}, []string{"carrier", "status"})

	// API metrics (emitted from API)
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tracksphere_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// SSE metrics. DeliveryDuration measures hub fan-out latency: the time a
	// published event takes to be handed to every local subscriber channel
	// (non-blocking by design — slow consumers are dropped, not awaited). The
	// end-to-end notify→browser segment additionally depends on the browser's
	// EventSource reconnect loop and cannot be measured server-side.
	SSESubscribersActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tracksphere_sse_subscribers_active",
		Help: "Active SSE subscribers per tenant",
	}, []string{"tenant"})

	SSEDeliveryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tracksphere_sse_fanout_duration_seconds",
		Help:    "Time to fan a published event out to local subscriber channels",
		Buckets: prometheus.DefBuckets,
	})

	SSEEventsPublishedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_sse_events_published_total",
		Help: "Realtime events fanned out (or attempted) by the hub",
	}, []string{"type", "tenant"})

	// Job duration is observed around handler execution in the queue runner.
	// Job-count gauges deliberately do NOT exist here: per-tenant queue depth,
	// job age and DLQ aging are query-derived at scrape time from the live
	// tables in httpapi/metrics.go — lifetime in-process counters would be
	// duplicates that reset on every restart and report the wrong namespace.
	JobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tracksphere_job_duration_seconds",
		Help:    "Job handler execution duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})

	SweepDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tracksphere_sweep_duration_seconds",
		Help:    "Time from sweep lease acquisition to all tenants processed",
		Buckets: prometheus.DefBuckets,
	})

	SweepTenantsProcessedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tracksphere_sweep_tenants_processed_total",
		Help: "Total tenants processed in sweep",
	})

	SweepAlertsRaisedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_sweep_alerts_raised_total",
		Help: "Total alerts raised by sweep",
	}, []string{"severity"})

	SweepAlertsClearedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tracksphere_sweep_alerts_cleared_total",
		Help: "Total alerts auto-resolved by sweep",
	})

	SweepCeilingHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_sweep_ceiling_hits_total",
		Help: "Sweep passes that hit the per-tenant window ceiling and deferred shipments",
	}, []string{"tenant"})

	// Notification metrics
	NotificationsAttemptedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_notifications_attempted_total",
		Help: "Total notifications attempted",
	}, []string{"channel", "tenant"})

	NotificationsSentTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_notifications_sent_total",
		Help: "Total notifications sent",
	}, []string{"channel", "tenant"})

	NotificationsFailedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_notifications_failed_total",
		Help: "Total notifications failed",
	}, []string{"channel", "tenant", "error"})

	InterruptsSentTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_interrupts_sent_total",
		Help: "Total interrupts sent",
	}, []string{"tenant", "severity"})

	InterruptsQueuedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_interrupts_queued_total",
		Help: "Total interrupts queued to digest",
	}, []string{"tenant", "reason"})

	DigestsFlushedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_digests_flushed_total",
		Help: "Total digests flushed",
	}, []string{"tenant"})

	// Database metrics
	DBPoolConnsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tracksphere_db_pool_connections_active",
		Help: "Database pool connections checked out",
	})

	DBPoolConnsIdle = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tracksphere_db_pool_connections_idle",
		Help: "Database pool connections idle",
	})

	DBPoolConnsMax = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tracksphere_db_pool_connections_max",
		Help: "Database pool max connections",
	})

	// Circuit breaker metrics
	CircuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tracksphere_circuit_breaker_state",
		Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
	}, []string{"client"})

	CircuitBreakerFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_circuit_breaker_failures_total",
		Help: "Total circuit breaker failures",
	}, []string{"client"})

	CircuitBreakerTripsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tracksphere_circuit_breaker_trips_total",
		Help: "Total circuit breaker trips to open state",
	}, []string{"client"})
)

// UpdateDBPoolMetrics updates the DB pool metrics from pool stats.
// This should be called periodically from the API process.
func UpdateDBPoolMetrics(acquired, idle, total, max int) {
	_ = total
	DBPoolConnsActive.Set(float64(acquired))
	DBPoolConnsIdle.Set(float64(idle))
	DBPoolConnsMax.Set(float64(max))
}

func RecordCircuitBreakerState(client string, state int) {
	CircuitBreakerState.WithLabelValues(client).Set(float64(state))
}

func RecordCircuitBreakerFailure(client string) {
	CircuitBreakerFailuresTotal.WithLabelValues(client).Inc()
}

func RecordCircuitBreakerTrip(client string) {
	CircuitBreakerTripsTotal.WithLabelValues(client).Inc()
}

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// handleMetrics GET /api/v1/metrics — Prometheus exposition for the one thing
// operators page on at 3am: queue depth, dead letters, SSE fan-out, pool
// saturation.
//
// Exposes global counters only by default. With `?per_tenant=true` exposes
// per-tenant breakdown (restricted to admin+). Never emits tenant-identifying
// labels in default mode so scraping cannot leak customer activity.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.ExposeMetrics {
		writeError(w, http.StatusNotFound, "not_found", "Not available")
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	perTenant := r.URL.Query().Get("per_tenant") == "true"

	// Always emit the standard Prometheus gauges/counters first.
	promhttp.Handler().ServeHTTP(w, r)

	// Preserve the DB-backed tenant metrics as an extension of the default scrape.
	if perTenant && s.requireRoleForMetrics(r, "admin") {
		if _, err := w.Write([]byte("\n")); err != nil {
			s.log.Error("write metrics separator failed", "err", err)
			return
		}
		var out strings.Builder
		s.writePerTenantMetrics(&out, r.Context())
		if out.Len() > 0 {
			if _, err := w.Write([]byte(out.String())); err != nil {
				s.log.Error("write per-tenant metrics failed", "err", err)
			}
		}
	}
}

func (s *Server) requireRoleForMetrics(r *http.Request, minRole string) bool {
	// Check Authorization header for API key
	if u := s.apiKeyUser(r); u != nil {
		return roleRank(u.Role) >= roleRank(minRole)
	}
	// Check session cookie
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	user, err := s.sessionUser(r.Context(), cookie.Value)
	if err != nil || user == nil {
		return false
	}
	return roleRank(user.Role) >= roleRank(minRole)
}

func (s *Server) writePerTenantMetrics(out *strings.Builder, ctx context.Context) {
	// Queue depth by tenant and kind
	rows, err := s.pool.Query(ctx, `
		SELECT j.tenant_id, j.kind, j.status, count(*)
		FROM jobs j
		WHERE j.status IN ('pending', 'running', 'dead')
		GROUP BY j.tenant_id, j.kind, j.status
	`)
	if err != nil {
		s.log.Error("per-tenant metrics query failed", "err", err)
		return
	}
	defer rows.Close()

	type tenantMetric struct {
		tenantID string
		kind     string
		status   string
		count    int64
	}
	var metrics []tenantMetric
	for rows.Next() {
		var m tenantMetric
		if err := rows.Scan(&m.tenantID, &m.kind, &m.status, &m.count); err != nil {
			continue
		}
		metrics = append(metrics, m)
	}

	// Sort for consistent output
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].tenantID != metrics[j].tenantID {
			return metrics[i].tenantID < metrics[j].tenantID
		}
		if metrics[i].kind != metrics[j].kind {
			return metrics[i].kind < metrics[j].kind
		}
		return metrics[i].status < metrics[j].status
	})

	for _, m := range metrics {
		writeGauge(out, "tracksphere_jobs_pending", "Jobs waiting to run",
			m.count, map[string]string{
				"tenant_id": m.tenantID,
				"kind":      m.kind,
			})
	}

	// DLQ aging per tenant
	dlqRows, err := s.pool.Query(ctx, `
		SELECT j.tenant_id, j.kind,
		       extract(epoch from (now() - j.updated_at)) as age_seconds
		FROM jobs j
		WHERE j.status = 'dead'
		ORDER BY j.tenant_id, age_seconds DESC
	`)
	if err != nil {
		s.log.Error("dlq aging query failed", "err", err)
		return
	}
	defer dlqRows.Close()

	type dlqMetric struct {
		tenantID string
		kind     string
		maxAge   float64
	}
	dlqMap := make(map[string]dlqMetric)
	for dlqRows.Next() {
		var tenantID, kind string
		var ageSeconds float64
		if err := dlqRows.Scan(&tenantID, &kind, &ageSeconds); err != nil {
			continue
		}
		key := tenantID + "|" + kind
		if existing, ok := dlqMap[key]; !ok || ageSeconds > existing.maxAge {
			dlqMap[key] = dlqMetric{tenantID: tenantID, kind: kind, maxAge: ageSeconds}
		}
	}

	for _, m := range dlqMap {
		writeGauge(out, "tracksphere_job_age_seconds", "Oldest dead job age in seconds",
			int64(m.maxAge), map[string]string{
				"tenant_id": m.tenantID,
				"kind":      m.kind,
				"status":    "dead",
			})
	}

	// SSE subscribers per tenant
	subRows, err := s.pool.Query(ctx, `
		SELECT tenant_id, count(*) as sub_count
		FROM sse_subscriptions
		GROUP BY tenant_id
	`)
	if err != nil {
		// Table might not exist yet (migration 000021)
		s.log.Debug("sse_subscriptions table not found", "err", err)
	} else {
		defer subRows.Close()
		for subRows.Next() {
			var tenantID string
			var count int64
			if err := subRows.Scan(&tenantID, &count); err != nil {
				continue
			}
			writeGauge(out, "tracksphere_sse_subscribers", "SSE subscribers per tenant",
				count, map[string]string{"tenant_id": tenantID})
		}
	}

	// Notification interrupts per tenant (last hour)
	intRows, err := s.pool.Query(ctx, `
		SELECT tenant_id, severity, count(*)
		FROM notification_interrupts
		WHERE created_at > now() - interval '1 hour'
		GROUP BY tenant_id, severity
	`)
	if err != nil {
		s.log.Error("interrupts query failed", "err", err)
	} else {
		defer intRows.Close()
		for intRows.Next() {
			var tenantID, severity string
			var count int64
			if err := intRows.Scan(&tenantID, &severity, &count); err != nil {
				continue
			}
			writeGauge(out, "tracksphere_interrupts_sent_total", "Interrupts sent in last hour",
				count, map[string]string{"tenant_id": tenantID, "severity": severity})
		}
	}

	// Active shipments per tenant
	shipRows, err := s.pool.Query(ctx, `
		SELECT tenant_id, count(*)
		FROM shipments
		WHERE status NOT IN ('delivered', 'cancelled')
		GROUP BY tenant_id
	`)
	if err != nil {
		s.log.Error("active shipments query failed", "err", err)
	} else {
		defer shipRows.Close()
		for shipRows.Next() {
			var tenantID string
			var count int64
			if err := shipRows.Scan(&tenantID, &count); err != nil {
				continue
			}
			writeGauge(out, "tracksphere_active_shipments", "Active shipments per tenant",
				count, map[string]string{"tenant_id": tenantID})
		}
	}
}

func writeGauge(out *strings.Builder, name, help string, value int64, labels map[string]string) {
	labelStr := ""
	if len(labels) > 0 {
		var parts []string
		for k, v := range labels {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, k, v))
		}
		labelStr = "{" + strings.Join(parts, ",") + "}"
	}
	fmt.Fprintf(out, `# HELP %s %s\n# TYPE %s gauge\n%s%s %d\n`,
		name, help, name, name, labelStr, value)
}

func writeGaugeSimple(out *strings.Builder, name, help string, value int64) {
	fmt.Fprintf(out, `# HELP %s %s\n# TYPE %s gauge\n%s %d\n`,
		name, help, name, name, value)
}

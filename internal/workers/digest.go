package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/httpclient"
	"github.com/tracksphere/tracksphere/internal/intel"
	"github.com/tracksphere/tracksphere/internal/queue"
)

// ReportWeeklyJob is enqueued per tenant by the weekly scheduler.
const ReportWeeklyJob = "report.weekly"

// ReportDailyJob is enqueued per tenant by the daily scheduler.
const ReportDailyJob = "report.daily"

// DigestFor computes the tenant's weekly ops summary.
func DigestFor(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (*intel.Digest, error) {
	d := &intel.Digest{WeekStart: time.Now().UTC().Truncate(24 * time.Hour)}
	err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status='delivered' AND delivered_at >= $1),
			       count(*) FILTER (WHERE status='exception'),
			       count(*) FILTER (WHERE status NOT IN ('delivered','cancelled')
			         AND updated_at < now() - interval '48 hours')`,
			weekAgo).Scan(&d.Delivered, &d.Exceptions, &d.StaleShipments); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE status='open'`).Scan(&d.OpenAlerts); err != nil {
			return err
		}
		var onTime, delivered int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status='delivered' AND delivered_at >= $1),
			       count(*) FILTER (WHERE status='delivered' AND delivered_at >= $1
			         AND (delivered_at IS NULL OR eta IS NULL OR delivered_at <= eta))`,
			weekAgo).Scan(&delivered, &onTime); err != nil {
			return err
		}
		if delivered > 0 {
			p := float64(onTime) / float64(delivered) * 100
			d.OnTimePct = &p
		}
		rows, err := tx.Query(ctx, `
			SELECT carrier, count(*) FROM shipments
			WHERE created_at >= $1 GROUP BY carrier ORDER BY count(*) DESC LIMIT 5`, weekAgo)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c intel.CarrierLine
			if err := rows.Scan(&c.Carrier, &c.Total); err != nil {
				return err
			}
			d.TopCarriers = append(d.TopCarriers, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if d.TopCarriers == nil {
		d.TopCarriers = []intel.CarrierLine{}
	}
	return d, nil
}

// DailySummary computes the tenant's daily ops summary for the daily digest.
func DailySummary(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (*intel.DailySummary, error) {
	d := &intel.DailySummary{Date: time.Now().UTC().Truncate(24 * time.Hour)}
	err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		dayAgo := time.Now().UTC().Add(-24 * time.Hour)
		
		// Shipments created today
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM shipments WHERE created_at >= $1`, dayAgo).
			Scan(&d.ShipmentsCreated); err != nil {
			return err
		}
		
		// Shipments delivered today
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM shipments 
			WHERE status='delivered' AND delivered_at >= $1`, dayAgo).
			Scan(&d.ShipmentsDelivered); err != nil {
			return err
		}
		
		// Active shipments (not delivered/cancelled)
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM shipments 
			WHERE status NOT IN ('delivered','cancelled')`).
			Scan(&d.ActiveShipments); err != nil {
			return err
		}
		
		// Exceptions raised today
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM alerts 
			WHERE status='open' AND detected_at >= $1`, dayAgo).
			Scan(&d.ExceptionsRaised); err != nil {
			return err
		}
		
		// Critical alerts open
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM alerts 
			WHERE status='open' AND severity='critical'`).
			Scan(&d.CriticalAlertsOpen); err != nil {
			return err
		}
		
		// Alerts resolved today
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM alerts 
			WHERE status='resolved' AND resolved_at >= $1`, dayAgo).
			Scan(&d.AlertsResolved); err != nil {
			return err
		}
		
		// Average resolution time (hours)
		var avgHours *float64
		if err := tx.QueryRow(ctx, `
			SELECT avg(extract(epoch from (resolved_at - detected_at))/3600)
			FROM alerts WHERE status='resolved' AND resolved_at >= $1`, dayAgo).
			Scan(&avgHours); err != nil && err != pgx.ErrNoRows {
			return err
		}
		d.AvgResolutionHours = avgHours
		
		// Top carrier by volume
		if err := tx.QueryRow(ctx, `
			SELECT carrier FROM shipments
			WHERE created_at >= $1
			GROUP BY carrier ORDER BY count(*) DESC LIMIT 1`, dayAgo).
			Scan(&d.TopCarrier); err != nil && err != pgx.ErrNoRows {
			return err
		}
		
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// EnqueueDigests fans out one report.weekly per tenant (system read, like
// the carrier scheduler). Runs weekly from the worker.
func EnqueueDigests(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM tenants`)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := queue.EnqueueTx(ctx, tx, ReportWeeklyJob,
			map[string]any{"tenantId": id.String()}, time.Time{}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// EnqueueDailyReports schedules daily summaries for all tenants.
func EnqueueDailyReports(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM tenants`)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := queue.EnqueueTx(ctx, tx, ReportDailyJob,
			map[string]any{"tenantId": id.String()}, time.Time{}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// HandleDigest delivers the weekly digest as an owner email notification.
func HandleDigest(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			TenantID uuid.UUID `json:"tenantId"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil
		}
		d, err := DigestFor(ctx, pool, p.TenantID)
		if err != nil {
			return err
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			var ownerEmail string
			_ = tx.QueryRow(ctx, `
				SELECT email FROM users WHERE tenant_id=$1 AND role='owner' AND is_active=true
				ORDER BY created_at ASC LIMIT 1`).Scan(&ownerEmail)
			if ownerEmail == "" {
				return nil
			}
			ot := "—"
			if d.OnTimePct != nil {
				ot = fmt.Sprintf("%.1f%%", *d.OnTimePct)
			}
			subject := "TrackSphere weekly digest"
			msg := fmt.Sprintf(
				"Week of %s\nDelivered: %d · On-time: %s\nExceptions: %d · Open alerts: %d · Stale (>48h): %d",
				d.WeekStart.Format("2006-01-02"), d.Delivered, ot,
				d.Exceptions, d.OpenAlerts, d.StaleShipments)
			_, err := tx.Exec(ctx, `
				INSERT INTO notifications (tenant_id, channel, recipient, subject, body, provider)
				VALUES ($1,'email',$2,$3,$4,'log')`,
				p.TenantID, ownerEmail, subject, msg)
			if err != nil {
				return err
			}
			log.Info("weekly digest", "tenant", p.TenantID, "delivered", d.Delivered)
			return nil
		})
	}
}

// HandleDailyReport delivers the daily summary via email + Slack/Teams webhook.
func HandleDailyReport(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	slackWebhook := os.Getenv("TRACKSPHERE_SLACK_WEBHOOK_URL")
	teamsWebhook := os.Getenv("TRACKSPHERE_TEAMS_WEBHOOK_URL")
	
	client := httpclient.NewClient(httpclient.NotificationProviderClient)
	
	return func(ctx context.Context, body []byte) error {
		var p struct {
			TenantID uuid.UUID `json:"tenantId"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil
		}
		d, err := DailySummary(ctx, pool, p.TenantID)
		if err != nil {
			return err
		}
		
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			// Get all admin/owner emails for delivery
			rows, err := tx.Query(ctx, `
				SELECT email FROM users 
				WHERE tenant_id=$1 AND role IN ('owner','admin') AND is_active=true`)
			if err != nil {
				return err
			}
			defer rows.Close()
			
			var recipients []string
			for rows.Next() {
				var email string
				if err := rows.Scan(&email); err != nil {
					continue
				}
				recipients = append(recipients, email)
			}
			
			if len(recipients) == 0 {
				return nil
			}
			
			avgRes := "—"
			if d.AvgResolutionHours != nil {
				avgRes = fmt.Sprintf("%.1fh", *d.AvgResolutionHours)
			}
			
			subject := fmt.Sprintf("TrackSphere daily summary — %s", d.Date.Format("2006-01-02"))
			msg := fmt.Sprintf(
				"Daily Summary for %s\n\n"+
					"📦 Shipments created: %d\n"+
					"✅ Shipments delivered: %d\n"+
					"🚢 Active shipments: %d\n"+
					"⚠️ Exceptions raised: %d\n"+
					"🔴 Critical alerts open: %d\n"+
					"✅ Alerts resolved: %d\n"+
					"⏱ Avg resolution time: %s\n"+
					"🏆 Top carrier: %s",
				d.Date.Format("2006-01-02"),
				d.ShipmentsCreated, d.ShipmentsDelivered, d.ActiveShipments,
				d.ExceptionsRaised, d.CriticalAlertsOpen, d.AlertsResolved,
				avgRes, d.TopCarrier)
			
			// Send email to each recipient
			for _, email := range recipients {
				_, err := tx.Exec(ctx, `
					INSERT INTO notifications (tenant_id, channel, recipient, subject, body, provider)
					VALUES ($1,'email',$2,$3,$4,'log')`,
					p.TenantID, email, subject, msg)
				if err != nil {
					log.Warn("daily report email insert failed", "tenant", p.TenantID, "err", err)
				}
			}
			
			// Send to Slack webhook if configured
			if slackWebhook != "" && d.CriticalAlertsOpen > 0 {
				go sendSlackWebhook(slackWebhook, client, d, subject, msg)
			}
			
			// Send to Teams webhook if configured
			if teamsWebhook != "" && d.CriticalAlertsOpen > 0 {
				go sendTeamsWebhook(teamsWebhook, client, d, subject, msg)
			}
			
			log.Info("daily report sent", "tenant", p.TenantID, "recipients", len(recipients))
			return nil
		})
	}
}

// sendSlackWebhook sends a formatted message to Slack
func sendSlackWebhook(webhookURL string, client *httpclient.Client, d *intel.DailySummary, subject, msg string) {
	payload := map[string]any{
		"text": subject,
		"blocks": []map[string]any{
			{
				"type": "header",
				"text": map[string]any{
					"type": "plain_text",
					"text": "📊 TrackSphere Daily Summary",
					"emoji": true,
				},
			},
			{
				"type": "section",
				"fields": []map[string]any{
					{"type": "mrkdwn", "text": fmt.Sprintf("*Date:*\n%s", d.Date.Format("2006-01-02"))},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Active Shipments:*\n%d", d.ActiveShipments)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Created Today:*\n%d", d.ShipmentsCreated)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Delivered Today:*\n%d", d.ShipmentsDelivered)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Exceptions Raised:*\n%d", d.ExceptionsRaised)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Critical Alerts Open:*\n%d", d.CriticalAlertsOpen)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Alerts Resolved:*\n%d", d.AlertsResolved)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Avg Resolution:*\n%s", func() string {
						if d.AvgResolutionHours != nil {
							return fmt.Sprintf("%.1fh", *d.AvgResolutionHours)
						}
						return "—"
					}())},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Top Carrier:*\n%s", d.TopCarrier)},
				},
			},
		},
	}
	
	if d.CriticalAlertsOpen > 0 {
		payload["blocks"] = append(payload["blocks"].([]map[string]any), map[string]any{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": fmt.Sprintf(":red_circle: *%d critical alerts open* — requires attention", d.CriticalAlertsOpen),
			},
		})
	}
	
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, webhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := client.Do(req)
	if err != nil {
		// Log but don't fail
		return
	}
	defer resp.Body.Close()
}

// sendTeamsWebhook sends a formatted message to Microsoft Teams
func sendTeamsWebhook(webhookURL string, client *httpclient.Client, d *intel.DailySummary, subject, msg string) {
	payload := map[string]any{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": "FF6B00",
		"summary":    subject,
		"sections": []map[string]any{
			{
				"activityTitle": "📊 TrackSphere Daily Summary",
				"activitySubtitle": d.Date.Format("2006-01-02"),
				"facts": []map[string]any{
					{"name": "Active Shipments", "value": fmt.Sprintf("%d", d.ActiveShipments)},
					{"name": "Created Today", "value": fmt.Sprintf("%d", d.ShipmentsCreated)},
					{"name": "Delivered Today", "value": fmt.Sprintf("%d", d.ShipmentsDelivered)},
					{"name": "Exceptions Raised", "value": fmt.Sprintf("%d", d.ExceptionsRaised)},
					{"name": "Critical Alerts Open", "value": fmt.Sprintf("%d", d.CriticalAlertsOpen)},
					{"name": "Alerts Resolved", "value": fmt.Sprintf("%d", d.AlertsResolved)},
					{"name": "Avg Resolution", "value": func() string {
						if d.AvgResolutionHours != nil {
							return fmt.Sprintf("%.1fh", *d.AvgResolutionHours)
						}
						return "—"
					}()},
					{"name": "Top Carrier", "value": d.TopCarrier},
				},
			},
		},
	}
	
	if d.CriticalAlertsOpen > 0 {
		payload["themeColor"] = "FF0000"
		payload["sections"] = append(payload["sections"].([]map[string]any), map[string]any{
			"activityTitle": ":red_circle: Critical Alerts Require Attention",
			"text": fmt.Sprintf("%d critical alerts are currently open", d.CriticalAlertsOpen),
		})
	}
	
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, webhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

// EnqueueDailyReports schedules daily summaries for all tenants (legacy name)
func EnqueueDailyReportsLegacy(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return EnqueueDailyReports(ctx, pool)
}
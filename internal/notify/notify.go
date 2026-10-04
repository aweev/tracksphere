// Package notify implements P2 outbound providers: the Sender interface,
// a registry, and real implementations selectable via environment.
//
//   TRACKSPHERE_NOTIFY_DEFAULT=log|print    (print = stdout, for staging debug)
//   SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, SMTP_FROM   → email provider
//   TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM      → sms + whatsapp
//   TRACKSPHERE_NOTIFY_WEBHOOK_URL                          → POST JSON copy
//
// Anything unconfigured falls back to `log` (structured log + DB audit row),
// so staging works with zero secrets and production degrades loudly, never
// silently.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"

	"github.com/tracksphere/tracksphere/internal/httpclient"
)

// Sender delivers one message. Implementations must be safe to retry
// (the queue redelivers on error until max_attempts → DLQ).
type Sender interface {
	Name() string
	Send(ctx context.Context, channel, recipient, subject, body string) error
}

// Configured reports which providers have credentials present.
func Configured() map[string]bool {
	_, smtpOK := os.LookupEnv("SMTP_HOST")
	_, twilioOK := os.LookupEnv("TWILIO_ACCOUNT_SID")
	_, hookOK := os.LookupEnv("TRACKSPHERE_NOTIFY_WEBHOOK_URL")
	return map[string]bool{
		"log": true, "print": true,
		"smtp": smtpOK, "twilio": twilioOK, "webhook": hookOK,
	}
}

// Default returns the configured default sender (or log).
func Default(log *slog.Logger) Sender {
	switch strings.ToLower(os.Getenv("TRACKSPHERE_NOTIFY_DEFAULT")) {
	case "print":
		return printSender{}
	case "webhook":
		if u := os.Getenv("TRACKSPHERE_NOTIFY_WEBHOOK_URL"); u != "" {
			return webhookSender{url: u, client: httpclient.NewClient(httpclient.NotificationProviderClient)}
		}
	}
	return logSender{log: log}
}

// ForChannel picks the sender for a channel: email→smtp, sms/whatsapp→
// twilio, falling back to Default. Unknown channels use Default.
func ForChannel(log *slog.Logger, channel string) Sender {
	switch channel {
	case "email":
		if os.Getenv("SMTP_HOST") != "" {
			return smtpSender{fromEnv: true}
		}
	case "sms", "whatsapp":
		if os.Getenv("TWILIO_ACCOUNT_SID") != "" {
			return twilioSender{client: httpclient.NewClient(httpclient.NotificationProviderClient)}
		}
	}
	return Default(log)
}

// ── Implementations ──

type logSender struct{ log *slog.Logger }

func (s logSender) Name() string { return "log" }
func (s logSender) Send(_ context.Context, channel, recipient, subject, _ string) error {
	s.log.Info("notification", "provider", "log",
		"channel", channel, "recipient", recipient, "subject", subject)
	return nil
}

type printSender struct{}

func (printSender) Name() string { return "print" }
func (printSender) Send(_ context.Context, channel, recipient, subject, body string) error {
	fmt.Printf("[notify] %s → %s: %s\n%s\n", channel, recipient, subject, body)
	return nil
}

type smtpSender struct{ fromEnv bool }

func (smtpSender) Name() string { return "smtp" }
func (s smtpSender) Send(_ context.Context, channel, recipient, subject, body string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from := os.Getenv("SMTP_FROM")
	auth := smtp.PlainAuth("", os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS"), host)
	msg := "To: " + recipient + "\r\nSubject: " + subject +
		"\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	return smtp.SendMail(host+":"+port, auth, from, []string{recipient}, []byte(msg))
}

type twilioSender struct{ client *httpclient.Client }

func (twilioSender) Name() string { return "twilio" }
func (s twilioSender) Send(ctx context.Context, channel, recipient, subject, body string) error {
	sid := os.Getenv("TWILIO_ACCOUNT_SID")
	token := os.Getenv("TWILIO_AUTH_TOKEN")
	from := os.Getenv("TWILIO_FROM")
	text := subject + "\n" + body
	if channel == "whatsapp" {
		if !strings.HasPrefix(recipient, "whatsapp:") {
			recipient = "whatsapp:" + recipient
		}
		if !strings.HasPrefix(from, "whatsapp:") {
			from = "whatsapp:" + from
		}
	}
	form := url.Values{"To": {recipient}, "From": {from}, "Body": {text}}
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		"https://api.twilio.com/2010-04-01/Accounts/"+sid+"/Messages.json",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(sid, token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("twilio: %s", resp.Status)
	}
	return nil
}

type webhookSender struct {
	url    string
	client *httpclient.Client
}

func (webhookSender) Name() string { return "webhook" }
func (s webhookSender) Send(ctx context.Context, channel, recipient, subject, body string) error {
	raw, _ := json.Marshal(map[string]string{
		"channel": channel, "recipient": recipient, "subject": subject, "body": body,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify webhook: %s", resp.Status)
	}
	return nil
}
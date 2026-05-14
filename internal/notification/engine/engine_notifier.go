// Package engine wraps argoproj/notifications-engine to deliver Kapro promotion
// events to 15+ providers (Slack, Teams, PagerDuty, OpsGenie, Webhook, etc.).
//
// Provider credentials live in a K8s Secret (default: kapro-notifications-secret).
// Channel destinations still come from GatePolicy.spec.notifications, keeping
// the policy as the single source of truth for *where* to notify.
//
// Secret key convention:
//
//	slack-token        → Slack bot token  (for type: slack entries)
//	pagerduty-token    → PagerDuty API token
//	opsgenie-api-key   → OpsGenie API key
//	(teams and webhook URLs come directly from policy spec.url, no secret needed)
//
// Email (type: email) reads SMTP credentials from the secret referenced in
// spec.email.smtpSecretRef. Keys: host, port, username, password, from.
// Set key "tls: true" for direct TLS (port 465); default is STARTTLS (port 587).
package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/argoproj/notifications-engine/pkg/services"

	"kapro.io/kapro/pkg/notification"
)

// sendTimeout is the per-provider deadline. Keeps notifications non-blocking.
const sendTimeout = 15 * time.Second

// secretTTL is how long the cached secret is considered fresh.
const secretTTL = 60 * time.Second

// Notifier wraps argoproj/notifications-engine for rich multi-provider delivery.
//
// Wire it up in main.go:
//
//	notifier := &engine.Notifier{
//	    SecretName: "kapro-notifications-secret",
//	    Namespace:  "kapro-system",
//	    Client:     mgr.GetClient(),
//	}
type Notifier struct {
	// SecretName is the K8s secret containing provider credentials.
	SecretName string
	// Namespace where the secret lives.
	Namespace string
	// Client reads the secret from the API server.
	Client client.Client

	mu          sync.Mutex
	cachedData  map[string]string
	cacheExpiry time.Time
}

// compile-time check: Notifier must satisfy notification.Notifier.
var _ notification.Notifier = &Notifier{}

// Notify fans out the event to all channels in the NotificationPolicy.
// It spawns goroutines for each provider and never blocks the caller.
// All errors are logged; none propagate.
func (n *Notifier) Notify(ctx context.Context, event notification.Event, policy notification.NotificationPolicy) {
	if len(policy.Channels) == 0 {
		return
	}

	logger := log.FromContext(ctx)

	secretData, err := n.loadSecret(ctx)
	if err != nil {
		logger.Error(fmt.Errorf("notification: %w", err), "failed to load notifications secret — skipping")
		return
	}

	msg := buildMessage(event)

	for _, ch := range policy.Channels {
		if !ch.MatchesEvent(event.Type) {
			continue
		}
		ch := ch // capture for goroutine
		go func() {
			sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
			defer cancel()

			if err := dispatch(sendCtx, ch, secretData, msg, event); err != nil {
				logger.Error(fmt.Errorf("notification: %w", err),
					"provider send failed", "type", ch.Type)
			} else {
				logger.Info("notification sent", "type", ch.Type, "phase", event.Phase)
			}
		}()
	}
}

// dispatch routes a single Channel to the appropriate engine service.
func dispatch(ctx context.Context, ch notification.Channel, secret map[string]string, msg string, event notification.Event) error {
	notif := services.Notification{Message: msg}

	switch ch.Type {
	case "slack":
		token := secret["slack-token"]
		if token == "" {
			return fmt.Errorf("slack-token not found in notifications secret")
		}
		svc := services.NewSlackService(services.SlackOptions{Token: token})
		return svc.Send(notif, services.Destination{Service: "slack", Recipient: ch.Target})

	case "teams":
		url := ch.Target
		if url == "" {
			url = secret["teams-webhook"]
		}
		if url == "" {
			return fmt.Errorf("teams notification requires channel.target or teams-webhook in secret")
		}
		svc := services.NewTeamsService(services.TeamsOptions{
			RecipientUrls: map[string]string{"default": url},
		})
		return svc.Send(notif, services.Destination{Service: "teams", Recipient: "default"})

	case "webhook":
		if ch.Target == "" {
			return fmt.Errorf("webhook notification requires a target URL")
		}
		svc := services.NewWebhookService(services.WebhookOptions{URL: ch.Target})
		return svc.Send(notif, services.Destination{Service: "webhook", Recipient: ""})

	case "email":
		if ch.Email == nil {
			return fmt.Errorf("email notification requires EmailConfig")
		}
		return sendEmailFromConfig(ctx, ch.Email, secret, event)

	case "pagerduty":
		token := secret["pagerduty-token"]
		if token == "" {
			return fmt.Errorf("pagerduty-token not found in notifications secret")
		}
		serviceID := ch.Target
		if serviceID == "" {
			return fmt.Errorf("pagerduty notification requires channel.target (PagerDuty service ID)")
		}
		svc := services.NewPagerdutyService(services.PagerdutyOptions{
			Token:     token,
			ServiceID: serviceID,
		})
		severity := "info"
		if event.IsFailure {
			severity = "critical"
		}
		notif.Pagerduty = &services.PagerDutyNotification{
			Title:   fmt.Sprintf("Kapro | %s | %s", event.Target, event.Phase),
			Body:    msg,
			Urgency: severity,
		}
		return svc.Send(notif, services.Destination{Service: "pagerduty", Recipient: serviceID})

	case "opsgenie":
		apiKey := secret["opsgenie-api-key"]
		if apiKey == "" {
			return fmt.Errorf("opsgenie-api-key not found in notifications secret")
		}
		team := ch.Target
		if team == "" {
			team = "default"
		}
		svc := services.NewOpsgenieService(services.OpsgenieOptions{
			ApiKeys: map[string]string{team: apiKey},
		})
		priority := "P3"
		if event.IsFailure {
			priority = "P1"
		}
		notif.Opsgenie = &services.OpsgenieNotification{
			Description: msg,
			Priority:    priority,
		}
		return svc.Send(notif, services.Destination{Service: "opsgenie", Recipient: team})

	default:
		return fmt.Errorf("unknown notification type %q", ch.Type)
	}
}

// loadSecret returns the decoded secret data, using a 60s in-memory cache to
// avoid hammering the API server on every reconcile.
func (n *Notifier) loadSecret(ctx context.Context) (map[string]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if time.Now().Before(n.cacheExpiry) && n.cachedData != nil {
		return n.cachedData, nil
	}

	var secret corev1.Secret
	key := client.ObjectKey{Name: n.SecretName, Namespace: n.Namespace}
	if err := n.Client.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", n.Namespace, n.SecretName, err)
	}

	data := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		data[k] = string(v)
	}

	n.cachedData = data
	n.cacheExpiry = time.Now().Add(secretTTL)
	return data, nil
}

// buildMessage produces a human-readable notification line from the event.
func buildMessage(e notification.Event) string {
	emoji := "🚀"
	if e.IsFailure {
		emoji = "❌"
	}
	msg := fmt.Sprintf("%s Kapro | %s → %s | version %s", emoji, e.Target, e.Phase, e.Version)
	if e.Release != "" {
		msg += " | release " + e.Release
	}
	if e.Message != "" {
		msg += " | " + e.Message
	}
	if e.ApproveURL != "" {
		msg += fmt.Sprintf("\n✅ Approve: %s\n❌ Reject:  %s", e.ApproveURL, e.RejectURL)
	}
	return msg
}

// sendEmailFromConfig delivers an HTML approval email via SMTP.
// SMTP host/port/credentials come from the Secret named by cfg.SMTPSecretRef
// (already loaded into secret map by loadSecret). Email addresses come from cfg.
// Secret keys: host, port (default 587), username, password, from, tls (optional "true").
func sendEmailFromConfig(ctx context.Context, cfg *notification.EmailConfig, secret map[string]string, event notification.Event) error {
	if len(cfg.To) == 0 {
		return fmt.Errorf("email: no recipients configured")
	}

	host := secret["host"]
	if host == "" {
		return fmt.Errorf("email: 'host' key missing from SMTP secret %q", cfg.SMTPSecretRef)
	}
	port := secret["port"]
	if port == "" {
		port = "587"
	}
	username := secret["username"]
	password := secret["password"]
	from := cfg.From
	if from == "" {
		from = secret["from"]
	}
	if from == "" {
		from = "kapro@" + host
	}
	useTLS := secret["tls"] == "true"

	subject, body := buildEmailContent(event)

	var buf bytes.Buffer
	buf.WriteString("From: " + from + "\r\n")
	buf.WriteString("To: " + joinStrings(cfg.To, ", ") + "\r\n")
	buf.WriteString("Subject: " + subject + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)

	addr := net.JoinHostPort(host, port)
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	if useTLS {
		// Direct TLS (port 465)
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("email: TLS dial %s: %w", addr, err)
		}
		defer func() { _ = conn.Close() }()
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("email: SMTP client: %w", err)
		}
		defer c.Quit() //nolint:errcheck
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("email: SMTP auth: %w", err)
			}
		}
		return smtpSend(c, from, cfg.To, buf.Bytes())
	}

	// STARTTLS (port 587) — standard corporate relay
	return smtp.SendMail(addr, auth, from, cfg.To, buf.Bytes())
}

func smtpSend(c *smtp.Client, from string, to []string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return fmt.Errorf("email: RCPT TO %s: %w", addr, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("email: write body: %w", err)
	}
	return w.Close()
}

// buildEmailContent returns a subject line and an HTML body for the event.
func buildEmailContent(e notification.Event) (subject, body string) {
	subject = fmt.Sprintf("[Kapro] %s: %s → %s requires approval", e.Release, e.Target, e.Version)

	approveSection := ""
	if e.ApproveURL != "" {
		approveSection = fmt.Sprintf(`
<p style="margin:24px 0">
  <a href="%s" style="background:#22c55e;color:white;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;margin-right:12px">
    ✅ Approve
  </a>
  <a href="%s" style="background:#ef4444;color:white;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold">
    ❌ Reject
  </a>
</p>
<p style="color:#6b7280;font-size:12px">These links expire in 48 hours.</p>`,
			e.ApproveURL, e.RejectURL)
	}

	body = fmt.Sprintf(`<!DOCTYPE html>
<html><body style="font-family:sans-serif;max-width:600px;margin:auto;padding:24px">
<h2 style="color:#1d4ed8">Kapro — Approval Required</h2>
<table style="border-collapse:collapse;width:100%%">
  <tr><td style="padding:8px;color:#6b7280">Release</td><td style="padding:8px;font-weight:bold">%s</td></tr>
  <tr><td style="padding:8px;color:#6b7280">Target</td><td style="padding:8px;font-weight:bold">%s</td></tr>
  <tr><td style="padding:8px;color:#6b7280">Version</td><td style="padding:8px;font-weight:bold">%s</td></tr>
</table>
%s
</body></html>`, e.Release, e.Target, e.Version, approveSection)

	return subject, body
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

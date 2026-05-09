// Package notification provides the lightweight built-in Kapro notification dispatcher.
// The KNI interface lives in kapro.io/kapro/pkg/notification.
//
// Built-in notifiers: Slack, Webhook (zero extra deps).
// Rich notifier:      engine/ using argoproj/notifications-engine (15+ providers).
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	pkgnotification "kapro.io/kapro/pkg/notification"
)

// Re-export KNI types for backward compat within the module.
type (
	Event              = pkgnotification.Event
	Notifier           = pkgnotification.Notifier
	NotificationPolicy = pkgnotification.NotificationPolicy
	Channel            = pkgnotification.Channel
)

// compile-time check: Dispatcher must satisfy Notifier.
var _ Notifier = &Dispatcher{}

// Dispatcher fans out a promotion event to all channels in the NotificationPolicy.
// It handles the two built-in types (slack, webhook) — zero extra dependencies.
// For richer providers (PagerDuty, OpsGenie, Teams, email) use engine.Notifier.
type Dispatcher struct {
	// HTTPClient is used for Slack and webhook calls. Defaults to a 10s timeout client.
	HTTPClient *http.Client
}

// Notify sends the event to all channels in the policy.
// Errors are logged but not returned — a notification failure must never block a promotion.
func (d *Dispatcher) Notify(ctx context.Context, event Event, policy NotificationPolicy) {
	if len(policy.Channels) == 0 {
		return
	}

	log := log.FromContext(ctx)
	client := d.httpClient()

	for _, ch := range policy.Channels {
		var err error
		switch ch.Type {
		case "slack":
			err = sendSlack(ctx, client, ch.Target, event)
		case "webhook":
			err = sendWebhook(ctx, client, ch.Target, event)
		default:
			log.Info("unknown notification type, skipping", "type", ch.Type)
			continue
		}
		if err != nil {
			// Never block on notification failure.
			log.Error(err, "notification failed", "type", ch.Type, "target", ch.Target)
		} else {
			log.Info("notification sent", "type", ch.Type, "phase", event.Phase)
		}
	}
}

func (d *Dispatcher) httpClient() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// sendSlack sends a formatted message to a Slack incoming webhook URL.
// spec.Channel is treated as the webhook URL for Slack (Slack incoming webhooks are URL-based).
func sendSlack(ctx context.Context, client *http.Client, webhookURL string, event Event) error {
	if webhookURL == "" {
		return fmt.Errorf("slack webhook URL is empty")
	}

	emoji := ":rocket:"
	if event.IsFailure {
		emoji = ":x:"
	}

	text := fmt.Sprintf("%s *Kapro* | `%s` → `%s` | version `%s` | %s",
		emoji, event.Target, event.Phase, event.Version, event.Message)

	payload := map[string]string{"text": text}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

// sendWebhook sends a JSON payload to a generic HTTP webhook endpoint.
func sendWebhook(ctx context.Context, client *http.Client, url string, event Event) error {
	if url == "" {
		return fmt.Errorf("webhook URL is empty")
	}

	body, _ := json.Marshal(event)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

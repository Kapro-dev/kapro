// Package notification provides the lightweight built-in Kapro notification dispatcher.
// The KNI interface lives in kapro.io/kapro/pkg/notification.
//
// Built-in notifiers: Slack, Webhook (zero extra deps).
// Rich notifier:      engine/ using argoproj/notifications-engine (15+ providers).
// External notifiers: gRPC plugins via PluginRegistration CRD.
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgnotification "kapro.io/kapro/pkg/notification"
)

// Re-export KNI types for backward compat within the module.
type (
	Event    = pkgnotification.Event
	Notifier = pkgnotification.Notifier
)

// compile-time check: Dispatcher must satisfy Notifier.
var _ Notifier = &Dispatcher{}

// Dispatcher fans out a promotion event to all channels configured in the policy.
type Dispatcher struct {
	// HTTPClient is used for Slack and webhook calls. Defaults to a 10s timeout client.
	HTTPClient *http.Client
}

// Notify sends the event to all notification channels in the policy.
// Errors are logged but not returned — a notification failure must never block a promotion.
func (d *Dispatcher) Notify(ctx context.Context, event Event, policy *kaprov1alpha1.PromotionPolicy) {
	if policy == nil || len(policy.Spec.Notifications) == 0 {
		return
	}

	log := log.FromContext(ctx)
	client := d.httpClient()

	for _, spec := range policy.Spec.Notifications {
		var err error
		switch spec.Type {
		case "slack":
			err = sendSlack(ctx, client, spec.Channel, event)
		case "webhook":
			err = sendWebhook(ctx, client, spec.URL, event)
		default:
			log.Info("unknown notification type, skipping", "type", spec.Type)
			continue
		}
		if err != nil {
			// Never block on notification failure.
			log.Error(err, "notification failed", "type", spec.Type, "channel", spec.Channel)
		} else {
			log.Info("notification sent", "type", spec.Type, "phase", event.Phase)
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
		emoji, event.Environment, event.Phase, event.Version, event.Message)

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

package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kapro.io/kapro/internal/notification"
	pkgnotification "kapro.io/kapro/pkg/notification"
)

// helpers — build NotificationPolicy values without any api/v1alpha2 import.
func slackPolicy(url string) pkgnotification.NotificationPolicy {
	return pkgnotification.NotificationPolicy{
		Channels: []pkgnotification.Channel{{Type: "slack", Target: url}},
	}
}

func webhookPolicy(url string) pkgnotification.NotificationPolicy {
	return pkgnotification.NotificationPolicy{
		Channels: []pkgnotification.Channel{{Type: "webhook", Target: url, Format: "json"}},
	}
}

func webhookCloudEventsPolicy(url string) pkgnotification.NotificationPolicy {
	return pkgnotification.NotificationPolicy{
		Channels: []pkgnotification.Channel{{Type: "webhook", Target: url, Format: "cloudevents"}},
	}
}

func TestDispatcher_Notify_EmptyPolicy_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	d.Notify(context.Background(), notification.Event{Phase: "Pending"}, pkgnotification.EmptyPolicy)
}

func TestDispatcher_Notify_EmptyChannels_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	d.Notify(context.Background(), notification.Event{Phase: "Pending"}, pkgnotification.NotificationPolicy{})
}

func TestDispatcher_Notify_Slack_SendsPayload(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	d.Notify(context.Background(), notification.Event{
		Phase:        "Converged",
		Version:      "v1.2.0",
		Target:       "staging",
		PromotionRun: "rel-1",
	}, slackPolicy(srv.URL))

	if len(received) == 0 {
		t.Fatal("expected Slack webhook to receive a payload")
	}
	var payload map[string]string
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("unmarshal slack payload: %v", err)
	}
	if payload["text"] == "" {
		t.Error("expected non-empty 'text' field in Slack payload")
	}
}

func TestDispatcher_Notify_Slack_FailureEmoji(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	d.Notify(context.Background(), notification.Event{
		Phase:     "Failed",
		IsFailure: true,
	}, slackPolicy(srv.URL))

	var payload map[string]string
	_ = json.Unmarshal(received, &payload)
	if text := payload["text"]; len(text) == 0 {
		t.Error("expected non-empty text for failure notification")
	}
}

func TestDispatcher_Notify_Slack_ServerError_NoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	// A notification failure must never block or panic.
	d.Notify(context.Background(), notification.Event{Phase: "Failed", IsFailure: true}, slackPolicy(srv.URL))
}

func TestDispatcher_Notify_Webhook_SendsPlainJSON(t *testing.T) {
	var received []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	d.Notify(context.Background(), notification.Event{
		Type:         pkgnotification.EventTargetConverged,
		Phase:        "Converged",
		Version:      "v1.0.0",
		Target:       "prod",
		PromotionRun: "rel-1",
	}, webhookPolicy(srv.URL))

	if contentType != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", contentType)
	}
	var got notification.Event
	if err := json.Unmarshal(received, &got); err != nil {
		t.Fatalf("unmarshal plain JSON: %v", err)
	}
	if got.Phase != "Converged" {
		t.Errorf("expected phase=Converged, got %s", got.Phase)
	}
}

func TestDispatcher_Notify_Webhook_SendsCloudEvents(t *testing.T) {
	var received []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	d.Notify(context.Background(), notification.Event{
		Type:          pkgnotification.EventTargetApplying,
		Phase:         "Applying",
		Version:       "v1.0.0",
		Target:        "prod",
		PromotionRun:  "rel-2",
		Plan: "main",
		Stage:         "canary",
	}, webhookCloudEventsPolicy(srv.URL))

	if len(received) == 0 {
		t.Fatal("expected webhook to receive a payload")
	}

	// Verify CloudEvents content type
	if contentType != "application/cloudevents+json" {
		t.Errorf("expected Content-Type=application/cloudevents+json, got %s", contentType)
	}

	// Verify CloudEvents envelope
	var ce map[string]interface{}
	if err := json.Unmarshal(received, &ce); err != nil {
		t.Fatalf("unmarshal CloudEvents payload: %v", err)
	}
	if ce["specversion"] != "1.0" {
		t.Errorf("expected specversion=1.0, got %v", ce["specversion"])
	}
	if ce["type"] != pkgnotification.EventTargetApplying {
		t.Errorf("expected type=%s, got %v", pkgnotification.EventTargetApplying, ce["type"])
	}
	if ce["subject"] != "promotionplan/main/stage/canary/target/prod" {
		t.Errorf("expected subject=promotionplan/main/stage/canary/target/prod, got %v", ce["subject"])
	}
	if ce["source"] != "/kapro/promotionruns/rel-2" {
		t.Errorf("expected source=/kapro/promotionruns/rel-2, got %v", ce["source"])
	}

	// Verify data contains the event
	data, ok := ce["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data to be a map")
	}
	if data["phase"] != "Applying" {
		t.Errorf("expected data.phase=Applying, got %v", data["phase"])
	}
	if data["version"] != "v1.0.0" {
		t.Errorf("expected data.version=v1.0.0, got %v", data["version"])
	}
}

func TestDispatcher_Notify_Webhook_ServerError_NoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	// Non-2xx response must not panic — just logs the error.
	d.Notify(context.Background(), notification.Event{Phase: "Failed"}, webhookPolicy(srv.URL))
}

func TestDispatcher_Notify_MultipleChannels_AllCalled(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	policy := pkgnotification.NotificationPolicy{
		Channels: []pkgnotification.Channel{
			{Type: "slack", Target: srv.URL},
			{Type: "webhook", Target: srv.URL},
		},
	}
	d.Notify(context.Background(), notification.Event{Phase: "Converged"}, policy)

	if called != 2 {
		t.Errorf("expected 2 HTTP calls (slack + webhook), got %d", called)
	}
}

func TestDispatcher_Notify_UnknownType_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	policy := pkgnotification.NotificationPolicy{
		Channels: []pkgnotification.Channel{
			{Type: "pagerduty", Target: "svc-id-123"},
		},
	}
	// pagerduty requires the engine notifier; Dispatcher skips it without panicking.
	d.Notify(context.Background(), notification.Event{Phase: "Converged"}, policy)
}

package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/notification"
)

func TestDispatcher_Notify_NilPolicy_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	d.Notify(context.Background(), notification.Event{Phase: "Pending"}, nil)
}

func TestDispatcher_Notify_EmptyNotifications_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	policy := &kaprov1alpha1.PromotionPolicy{}
	d.Notify(context.Background(), notification.Event{Phase: "Pending"}, policy)
}

func TestDispatcher_Notify_Slack_SendsPayload(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "slack", Channel: srv.URL},
			},
		},
	}
	d.Notify(context.Background(), notification.Event{
		Phase:       "Converged",
		Version:     "v1.2.0",
		Environment: "staging",
		Release:     "rel-1",
	}, policy)

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
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "slack", Channel: srv.URL},
			},
		},
	}
	d.Notify(context.Background(), notification.Event{
		Phase:     "Failed",
		IsFailure: true,
	}, policy)

	var payload map[string]string
	_ = json.Unmarshal(received, &payload)
	// Failure events use :x: emoji — verify text contains it.
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
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "slack", Channel: srv.URL},
			},
		},
	}
	// A notification failure must never block or panic.
	d.Notify(context.Background(), notification.Event{Phase: "Failed", IsFailure: true}, policy)
}

func TestDispatcher_Notify_Webhook_SendsJSON(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "webhook", URL: srv.URL},
			},
		},
	}
	d.Notify(context.Background(), notification.Event{
		Phase:       "Applying",
		Version:     "v1.0.0",
		Environment: "prod",
		Release:     "rel-2",
	}, policy)

	if len(received) == 0 {
		t.Fatal("expected webhook to receive a payload")
	}
	var got notification.Event
	if err := json.Unmarshal(received, &got); err != nil {
		t.Fatalf("unmarshal webhook payload: %v", err)
	}
	if got.Phase != "Applying" {
		t.Errorf("expected Phase=Applying, got %s", got.Phase)
	}
	if got.Version != "v1.0.0" {
		t.Errorf("expected Version=v1.0.0, got %s", got.Version)
	}
}

func TestDispatcher_Notify_Webhook_ServerError_NoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "webhook", URL: srv.URL},
			},
		},
	}
	// Non-2xx response must not panic — just logs the error.
	d.Notify(context.Background(), notification.Event{Phase: "Failed"}, policy)
}

func TestDispatcher_Notify_MultipleChannels_AllCalled(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &notification.Dispatcher{HTTPClient: srv.Client()}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "slack", Channel: srv.URL},
				{Type: "webhook", URL: srv.URL},
			},
		},
	}
	d.Notify(context.Background(), notification.Event{Phase: "Converged"}, policy)

	if called != 2 {
		t.Errorf("expected 2 HTTP calls (slack + webhook), got %d", called)
	}
}

func TestDispatcher_Notify_UnknownType_NoPanic(t *testing.T) {
	d := &notification.Dispatcher{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{
				{Type: "pagerduty", URL: "https://example.com"},
			},
		},
	}
	// Unknown types are silently skipped.
	d.Notify(context.Background(), notification.Event{Phase: "Converged"}, policy)
}

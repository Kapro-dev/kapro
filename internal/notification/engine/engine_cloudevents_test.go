package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kapro.io/kapro/pkg/notification"
)

func TestSendCloudEvents_EnvelopeAndContentType(t *testing.T) {
	var received []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := notification.Event{
		Type:          notification.EventTargetConverged,
		Phase:         "Converged",
		Version:       "v2.0.0",
		Target:        "fi-prod",
		PromotionRun:  "app-v2",
		Plan: "eu-rollout",
		Stage:         "prod",
	}

	err := sendCloudEvents(context.Background(), srv.URL, event)
	if err != nil {
		t.Fatalf("sendCloudEvents: %v", err)
	}

	if contentType != "application/cloudevents+json" {
		t.Errorf("Content-Type = %q, want application/cloudevents+json", contentType)
	}

	var ce map[string]interface{}
	if err := json.Unmarshal(received, &ce); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ce["specversion"] != "1.0" {
		t.Errorf("specversion = %v, want 1.0", ce["specversion"])
	}
	if ce["type"] != notification.EventTargetConverged {
		t.Errorf("type = %v, want %s", ce["type"], notification.EventTargetConverged)
	}
	if ce["subject"] != "promotionplan/eu-rollout/stage/prod/target/fi-prod" {
		t.Errorf("subject = %v, want promotionplan/eu-rollout/stage/prod/target/fi-prod", ce["subject"])
	}
	if ce["source"] != "/kapro/promotionruns/app-v2" {
		t.Errorf("source = %v, want /kapro/promotionruns/app-v2", ce["source"])
	}
}

func TestSendCloudEvents_EmptyType_FallsBackToUnknown(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := sendCloudEvents(context.Background(), srv.URL, notification.Event{
		Phase:        "Converged",
		Target:       "de-prod",
		PromotionRun: "rel-1",
	})
	if err != nil {
		t.Fatalf("sendCloudEvents: %v", err)
	}

	var ce map[string]interface{}
	_ = json.Unmarshal(received, &ce)
	if ce["type"] != "kapro.promotionrun.target.unknown" {
		t.Errorf("type = %v, want kapro.promotionrun.target.unknown", ce["type"])
	}
}

func TestSendCloudEvents_ServerError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	err := sendCloudEvents(context.Background(), srv.URL, notification.Event{
		Type:         notification.EventTargetFailed,
		Phase:        "Failed",
		PromotionRun: "rel-1",
	})
	if err == nil {
		t.Fatal("expected error for 502 response")
	}
}

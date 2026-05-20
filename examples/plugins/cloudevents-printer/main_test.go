package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kapro.io/kapro/pkg/events"
)

func TestHandleAcceptsValidCloudEvent(t *testing.T) {
	body, _, err := events.Render(events.Event{
		Type:          events.EventPromotionSucceeded,
		PromotionName: "checkout",
		Phase:         "Succeeded",
		PreviousPhase: "Progressing",
		Version:       "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	s := newServer("", log.New(&out, "", 0))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	s.handle(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(out.String(), "promotion.succeeded") {
		t.Fatalf("printed output missing type token: %q", out.String())
	}
	if !strings.Contains(out.String(), "promo=checkout") {
		t.Fatalf("printed output missing promotion: %q", out.String())
	}
	if !strings.Contains(out.String(), "phase=Progressing -> Succeeded") {
		t.Fatalf("printed output missing transition: %q", out.String())
	}
}

func TestHandleRejectsBadCloudEvent(t *testing.T) {
	s := newServer("", log.New(&strings.Builder{}, "", 0))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"not":"a cloudevent"}`))
	s.handle(rr, req)
	// Missing specversion → unmarshal succeeds but our check fails.
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleEnforcesAuthHeader(t *testing.T) {
	body, _, _ := events.Render(events.Event{
		Type: events.EventPromotionSucceeded, PromotionName: "p", Phase: "Succeeded",
	})
	s := newServer("s3cret", log.New(&strings.Builder{}, "", 0))

	noHeader := httptest.NewRecorder()
	s.handle(noHeader, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if noHeader.Code != http.StatusUnauthorized {
		t.Fatalf("no-header status = %d, want 401", noHeader.Code)
	}

	withHeader := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Kapro-Auth", "s3cret")
	s.handle(withHeader, req)
	if withHeader.Code != http.StatusNoContent {
		t.Fatalf("with-header status = %d, want 204", withHeader.Code)
	}
}

func TestHandleRejectsNonPost(t *testing.T) {
	s := newServer("", log.New(&strings.Builder{}, "", 0))
	rr := httptest.NewRecorder()
	s.handle(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestFormatEventEveryEventType makes sure formatEvent doesn't blow
// up or produce empty output for any constant in the Kapro vocabulary.
// New event types are exercised automatically by this sweep.
func TestFormatEventEveryEventType(t *testing.T) {
	for _, et := range events.AllEventTypes() {
		_, env, err := events.Render(events.Event{
			Type:          et,
			PromotionName: "canary",
			Phase:         "Progressing",
			Version:       "v0.0.1",
		})
		if err != nil {
			t.Errorf("Render %q: %v", et, err)
			continue
		}
		out := formatEvent(env)
		if out == "" {
			t.Errorf("formatEvent(%q) returned empty string", et)
		}
		// Round-trip the rendered envelope through the same JSON parser
		// the server uses, exercising the full pipeline.
		var rt events.Envelope
		if err := json.Unmarshal(mustRender(t, et), &rt); err != nil {
			t.Errorf("unmarshal %q: %v", et, err)
		}
		if rt.Type != et {
			t.Errorf("round-trip type = %q, want %q", rt.Type, et)
		}
	}
}

func mustRender(t *testing.T, et events.EventType) []byte {
	t.Helper()
	body, _, err := events.Render(events.Event{
		Type: et, PromotionName: "x", Phase: "Progressing",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

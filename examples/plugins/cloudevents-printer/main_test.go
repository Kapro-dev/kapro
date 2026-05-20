package main

import (
	"bytes"
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

// TestHandleAcceptsEveryEventType sweeps every constant in
// events.AllEventTypes(), renders an envelope for it, and POSTs it
// through the real server.handle. This catches two failure modes in
// one test: a new EventType whose rendered envelope confuses Unmarshal,
// or a new EventType whose printed line is empty. The handler invocation
// makes the README's "re-parses through the same handler" claim true.
func TestHandleAcceptsEveryEventType(t *testing.T) {
	for _, et := range events.AllEventTypes() {
		body, env, err := events.Render(events.Event{
			Type:          et,
			PromotionName: "canary",
			Phase:         "Progressing",
			Version:       "v0.0.1",
		})
		if err != nil {
			t.Errorf("Render %q: %v", et, err)
			continue
		}
		if env.Type != et {
			t.Errorf("Render(%q).Type = %q", et, env.Type)
		}
		if out := formatEvent(env); out == "" {
			t.Errorf("formatEvent(%q) returned empty string", et)
		}

		// Invoke the real handler — round-trips through the same JSON
		// parser, specversion check, and 204 response path that
		// production traffic hits.
		var captured strings.Builder
		s := newServer("", log.New(&captured, "", 0))
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		s.handle(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Errorf("handle(%q): status = %d, want 204; body=%s", et, rr.Code, rr.Body.String())
		}
		if !strings.Contains(captured.String(), strings.TrimPrefix(string(et), "kapro.io/")) {
			t.Errorf("handle(%q): printed output missing type token; got %q", et, captured.String())
		}
	}
}

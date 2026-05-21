package kapro

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kapro.io/kapro/pkg/events"
)

func TestSubscriberDispatchesCloudEvent(t *testing.T) {
	body, _, err := events.Render(events.Event{
		Type:          events.EventPromotionSucceeded,
		PromotionName: "checkout",
		FleetRef:      "checkout-fleet",
		Phase:         "Succeeded",
		Version:       "v1.2.3",
		Time:          time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("render event: %v", err)
	}

	var got events.Event
	sub := NewSubscriber(":0")
	sub.On(events.EventPromotionSucceeded, func(event events.Event) {
		got = event
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	sub.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got.Type != events.EventPromotionSucceeded {
		t.Fatalf("event type = %q", got.Type)
	}
	if got.PromotionName != "checkout" {
		t.Fatalf("promotion = %q", got.PromotionName)
	}
	if got.FleetRef != "checkout-fleet" {
		t.Fatalf("fleetRef = %q", got.FleetRef)
	}
	if got.Version != "v1.2.3" {
		t.Fatalf("version = %q", got.Version)
	}
}

func TestSubscriberRunReturnsNilOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewSubscriber("127.0.0.1:0").Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on clean context cancellation", err)
	}
}

func TestSubscriberRejectsInvalidRequests(t *testing.T) {
	validBody, _, err := events.Render(events.Event{
		Type:          events.EventPromotionCreated,
		PromotionName: "checkout",
		Phase:         "Pending",
	})
	if err != nil {
		t.Fatalf("render event: %v", err)
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{
			name:   "non post",
			method: http.MethodGet,
			path:   "/",
			status: http.StatusMethodNotAllowed,
		},
		{
			name:   "unknown path",
			method: http.MethodPost,
			path:   "/events",
			body:   string(validBody),
			status: http.StatusNotFound,
		},
		{
			name:   "invalid json",
			method: http.MethodPost,
			path:   "/",
			body:   "{",
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid specversion",
			method: http.MethodPost,
			path:   "/",
			body:   `{"specversion":"0.3","type":"kapro.io/promotion.created"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "missing id",
			method: http.MethodPost,
			path:   "/",
			body:   `{"specversion":"1.0","source":"/apis/kapro.io/v1alpha2/promotions/checkout","type":"kapro.io/promotion.created","time":"2026-05-22T00:00:00Z","datacontenttype":"application/json","data":{"promotion":"checkout"}}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid time",
			method: http.MethodPost,
			path:   "/",
			body:   `{"specversion":"1.0","id":"1","source":"/apis/kapro.io/v1alpha2/promotions/checkout","type":"kapro.io/promotion.created","time":"nope","datacontenttype":"application/json","data":{"promotion":"checkout"}}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "unsupported data content type",
			method: http.MethodPost,
			path:   "/",
			body:   `{"specversion":"1.0","id":"1","source":"/apis/kapro.io/v1alpha2/promotions/checkout","type":"kapro.io/promotion.created","time":"2026-05-22T00:00:00Z","datacontenttype":"text/plain","data":{"promotion":"checkout"}}`,
			status: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := NewSubscriber(":0")
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader([]byte(tt.body)))
			sub.handler().ServeHTTP(rr, req)
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d", rr.Code, tt.status)
			}
		})
	}
}

func TestSubscriberAcceptsJSONContentTypeParameters(t *testing.T) {
	body, _, err := events.Render(events.Event{
		Type:          events.EventPromotionCreated,
		PromotionName: "checkout",
		Phase:         "Pending",
	})
	if err != nil {
		t.Fatalf("render event: %v", err)
	}
	body = bytes.Replace(body, []byte(`"datacontenttype":"application/json"`), []byte(`"datacontenttype":"application/json; charset=utf-8"`), 1)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	NewSubscriber(":0").handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
}

func TestSubscriberHealthzAndBodyLimit(t *testing.T) {
	sub := NewSubscriber(":0")
	sub.maxBody = 8

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	sub.handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("healthz status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/healthz", nil)
	sub.handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("healthz post status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("healthz Allow = %q", allow)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"specversion":"1.0"}`)))
	sub.handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestSubscriberPanicRecovery(t *testing.T) {
	body, _, err := events.Render(events.Event{
		Type:          events.EventPromotionFailed,
		PromotionName: "checkout",
		Phase:         "Failed",
	})
	if err != nil {
		t.Fatalf("render event: %v", err)
	}

	sub := NewSubscriber(":0")
	sub.On(events.EventPromotionFailed, func(events.Event) {
		panic("boom")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	sub.handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status after handler panic = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

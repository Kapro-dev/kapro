package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kapro.io/kapro/pkg/events"
)

type memorySink struct {
	mu      sync.Mutex
	records []ArchiveRecord
	err     error
}

func (s *memorySink) Write(ctx context.Context, record ArchiveRecord) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *memorySink) Close() error {
	return nil
}

func TestHandleEventArchivesOriginalBodyAndMetadata(t *testing.T) {
	sink := &memorySink{}
	handler := NewServer(":0", sink, defaultMaxBodyBytes, nil).handler()
	body := validEnvelopeBody(t)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/cloudevents+json")
	req.Header.Set("User-Agent", "kapro-test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if len(sink.records) != 1 {
		t.Fatalf("archived records = %d, want 1", len(sink.records))
	}
	got := sink.records[0]
	if string(got.Body) != string(body) {
		t.Fatalf("archived body changed\n got: %s\nwant: %s", got.Body, body)
	}
	if got.Metadata.ID != got.Envelope.ID {
		t.Fatalf("metadata id = %q, want envelope id %q", got.Metadata.ID, got.Envelope.ID)
	}
	if got.Metadata.BodySHA256 == "" {
		t.Fatal("metadata body hash is empty")
	}
	if got.Metadata.DedupeKey == "" {
		t.Fatal("metadata dedupe key is empty")
	}
	if got.Metadata.RequestContentType != "application/cloudevents+json" {
		t.Fatalf("request content type = %q", got.Metadata.RequestContentType)
	}
	if got.Metadata.UserAgent != "kapro-test" {
		t.Fatalf("user agent = %q", got.Metadata.UserAgent)
	}
}

func TestHandleEventRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		maxBody     int64
		contentType string
		want        int
	}{
		{
			name:        "wrong method",
			method:      http.MethodGet,
			path:        "/",
			contentType: "application/cloudevents+json",
			want:        http.StatusMethodNotAllowed,
		},
		{
			name:        "wrong path",
			method:      http.MethodPost,
			path:        "/events",
			contentType: "application/cloudevents+json",
			want:        http.StatusNotFound,
		},
		{
			name:        "wrong content type",
			method:      http.MethodPost,
			path:        "/",
			body:        "{}",
			contentType: "text/plain",
			want:        http.StatusUnsupportedMediaType,
		},
		{
			name:        "invalid json",
			method:      http.MethodPost,
			path:        "/",
			body:        "{",
			contentType: "application/cloudevents+json",
			want:        http.StatusBadRequest,
		},
		{
			name:        "missing id",
			method:      http.MethodPost,
			path:        "/",
			body:        `{"specversion":"1.0","source":"/s","type":"kapro.io/promotion.succeeded","time":"2026-05-22T10:00:00Z","datacontenttype":"application/json","data":{}}`,
			contentType: "application/cloudevents+json",
			want:        http.StatusBadRequest,
		},
		{
			name:        "too large",
			method:      http.MethodPost,
			path:        "/",
			body:        string(validEnvelopeBody(t)),
			maxBody:     8,
			contentType: "application/cloudevents+json",
			want:        http.StatusRequestEntityTooLarge,
		},
		{
			name:        "sink failure",
			method:      http.MethodPost,
			path:        "/",
			body:        string(validEnvelopeBody(t)),
			contentType: "application/cloudevents+json",
			want:        http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &memorySink{}
			if tt.name == "sink failure" {
				sink.err = errors.New("boom")
			}
			maxBody := tt.maxBody
			if maxBody == 0 {
				maxBody = defaultMaxBodyBytes
			}
			handler := NewServer(":0", sink, maxBody, nil).handler()
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func validEnvelopeBody(t *testing.T) []byte {
	t.Helper()
	envelope := events.Envelope{
		SpecVersion:     "1.0",
		ID:              "event-1",
		Source:          "/apis/kapro.io/v1alpha1/promotions/demo",
		Type:            events.EventType("kapro.io/promotion.succeeded"),
		Subject:         "demo",
		Time:            time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		DataContentType: "application/json",
		Data: events.EventData{
			Promotion: "demo",
			Phase:     "Succeeded",
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

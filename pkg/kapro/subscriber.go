package kapro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"kapro.io/kapro/pkg/events"
)

const (
	defaultMaxBodyBytes = int64(1 << 20)
)

// Subscriber consumes Kapro CloudEvents from a sink endpoint.
type Subscriber struct {
	endpoint string
	handlers map[events.EventType][]func(events.Event)

	maxBody int64
	mu      sync.RWMutex
}

// NewSubscriber creates a subscriber bound to endpoint, for example ":8080".
func NewSubscriber(endpoint string) *Subscriber {
	return &Subscriber{
		endpoint: endpoint,
		handlers: make(map[events.EventType][]func(events.Event)),
		maxBody:  defaultMaxBodyBytes,
	}
}

// On registers a handler for one Kapro CloudEvents type.
func (s *Subscriber) On(eventType events.EventType, handler func(events.Event)) *Subscriber {
	if handler == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[eventType] = append(s.handlers[eventType], handler)
	return s
}

// Run starts the HTTP subscriber and blocks until ctx is cancelled or the
// listener exits. Successful handlers return HTTP 204; handler panics or
// decoding errors return a non-2xx status so upstream delivery can retry.
func (s *Subscriber) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server := &http.Server{
		Addr:              s.endpoint,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", s.endpoint)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.endpoint, err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown subscriber: %w", err)
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Subscriber) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", s.handleEvent)
	return mux
}

func (s *Subscriber) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	maxBody := s.maxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "cloudevents body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read cloudevents body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var envelope events.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "decode cloudevents envelope", http.StatusBadRequest)
		return
	}
	if envelope.SpecVersion != "1.0" {
		http.Error(w, "unsupported cloudevents specversion", http.StatusBadRequest)
		return
	}
	if err := validateEnvelope(envelope); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	event := envelopeToEvent(envelope)
	if err := s.dispatch(event); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Subscriber) dispatch(event events.Event) (err error) {
	s.mu.RLock()
	handlers := append([]func(events.Event){}, s.handlers[event.Type]...)
	s.mu.RUnlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("kapro subscriber handler panic: %v", recovered)
		}
	}()
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func validateEnvelope(envelope events.Envelope) error {
	if strings.TrimSpace(envelope.ID) == "" {
		return errors.New("missing cloudevents id")
	}
	if strings.TrimSpace(envelope.Source) == "" {
		return errors.New("missing cloudevents source")
	}
	if envelope.Type == "" {
		return errors.New("missing cloudevents type")
	}
	if strings.TrimSpace(envelope.Time) == "" {
		return errors.New("missing cloudevents time")
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.Time); err != nil {
		return fmt.Errorf("invalid cloudevents time: %w", err)
	}
	if envelope.DataContentType != "" && !isJSONContentType(envelope.DataContentType) {
		return fmt.Errorf("unsupported cloudevents datacontenttype %q", envelope.DataContentType)
	}
	if strings.TrimSpace(envelope.Data.Promotion) == "" {
		return errors.New("missing kapro event data.promotion")
	}
	return nil
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

func envelopeToEvent(envelope events.Envelope) events.Event {
	eventTime, _ := time.Parse(time.RFC3339Nano, envelope.Time)
	return events.Event{
		Type:          envelope.Type,
		PromotionName: envelope.Data.Promotion,
		PromotionUID:  envelope.Data.PromotionUID,
		FleetRef:      envelope.Data.FleetRef,
		Phase:         envelope.Data.Phase,
		PreviousPhase: envelope.Data.PreviousPhase,
		Version:       envelope.Data.Version,
		AttemptName:   envelope.Data.AttemptName,
		Wave:          envelope.Data.Wave,
		Stage:         envelope.Data.Stage,
		Gate:          envelope.Data.Gate,
		Target:        envelope.Data.Target,
		Reason:        envelope.Data.Reason,
		Message:       envelope.Data.Message,
		Time:          eventTime,
	}
}

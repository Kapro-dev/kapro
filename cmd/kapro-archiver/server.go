package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"kapro.io/kapro/pkg/events"
)

type Server struct {
	addr    string
	sink    ArchiveSink
	maxBody int64
	logger  *slog.Logger
}

func NewServer(addr string, sink ArchiveSink, maxBody int64, logger *slog.Logger) *Server {
	if addr == "" {
		addr = defaultListenAddr
	}
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		addr:    addr,
		sink:    sink,
		maxBody: maxBody,
		logger:  logger,
	}
}

func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.sink == nil {
		return errors.New("archive sink is required")
	}

	server := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
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
			return fmt.Errorf("shutdown archiver: %w", err)
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

func (s *Server) handler() http.Handler {
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

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !isStructuredCloudEventsContentType(contentType) {
		http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
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
	if err := validateEnvelope(envelope); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	record := newArchiveRecord(envelope, body, r, receivedAt)
	if err := s.sink.Write(r.Context(), record); err != nil {
		s.logger.Error("archive event", "id", envelope.ID, "source", envelope.Source, "type", envelope.Type, "error", err)
		http.Error(w, "archive cloudevents envelope", http.StatusInternalServerError)
		return
	}
	s.logger.Info("archived event", "id", envelope.ID, "source", envelope.Source, "type", envelope.Type, "dedupeKey", record.Metadata.DedupeKey)
	w.WriteHeader(http.StatusNoContent)
}

// Package mcp implements a Model Context Protocol server for Kapro.
// It exposes Kapro state and actions to AI assistants (Claude, GitHub Copilot, Cursor)
// via JSON-RPC 2.0 over HTTP.
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Server is the Kapro MCP server. It exposes Kapro state and actions to AI
// assistants via the Model Context Protocol (JSON-RPC 2.0 over HTTP).
type Server struct {
	client client.Client
	mux    *http.ServeMux
}

// New creates a new MCP server backed by the given controller-runtime client.
func New(c client.Client) *Server {
	s := &Server{
		client: c,
		mux:    http.NewServeMux(),
	}
	h := &handler{server: s}
	s.mux.HandleFunc("/mcp", h.ServeHTTP)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return s
}

// Start runs the MCP HTTP server on addr and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return fmt.Errorf("mcp server: %w", err)
	}
}

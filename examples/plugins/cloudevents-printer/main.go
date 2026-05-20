// Command cloudevents-printer is the reference subscriber for Kapro's
// CloudEvents v1.0 sink. It is the smallest useful artefact a third
// party can run against a Kapro cluster to validate the public
// `pkg/events` contract end-to-end.
//
// What it does
//
//   - Listens for HTTP POSTs from the Kapro operator's CloudEvents
//     sink (the URL configured via KAPRO_EVENTS_SINK_URL on the
//     kapro-operator Deployment). The bundled in-cluster manifest
//     terminates TLS at the Ingress; the binary itself speaks plain
//     HTTP so that fronting it with any TLS proxy / service mesh /
//     OAuth gateway stays straightforward.
//   - Decodes each request body as a CloudEvents v1.0 structured-mode
//     envelope using `kapro.io/kapro/pkg/events.Envelope`.
//   - Pretty-prints one line per event to stdout in a
//     subscriber-shaped format that makes the fleet narrative
//     legible at a glance.
//   - Responds 204 No Content (or 400 on malformed input).
//
// What it deliberately does NOT do
//
//   - Translate to Slack / PagerDuty / Teams etc. — that's Argo CD
//     Notifications' / Flux Notification Controller's job.
//   - Persist or aggregate. It is a pretty-printer.
//   - Authenticate inbound requests beyond a static shared-secret
//     header check (see KAPRO_PRINTER_AUTH_HEADER below). Production
//     subscribers should front this with a real auth layer.
//
// This binary is intentionally tiny so that it doubles as a
// copy-paste starter for "how do I consume Kapro CloudEvents from
// Go?". The only Kapro dependency is `pkg/events`, which is the
// public API.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"kapro.io/kapro/pkg/events"
)

const (
	// envAuthHeader optionally requires inbound requests to carry a
	// matching `X-Kapro-Auth` header. When unset, the printer accepts
	// any request — fine for in-cluster usage behind an Ingress that
	// already authenticates the operator.
	envAuthHeader = "KAPRO_PRINTER_AUTH_HEADER"
	// envListenAddr controls the bind address. Defaults to :8080.
	envListenAddr = "KAPRO_PRINTER_LISTEN_ADDR"
)

func main() {
	listen := flag.String("listen", envOr(envListenAddr, ":8080"), "HTTP listen address")
	flag.Parse()

	expectedAuth := os.Getenv(envAuthHeader)
	out := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	srv := newServer(expectedAuth, out)
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handle)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Use the same logger instance for startup as for event lines so
	// stdout shows one consistent stream and tests can redirect it.
	out.Printf("cloudevents-printer listening on %s (auth header required: %t)",
		*listen, expectedAuth != "")
	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		out.Fatalf("server: %v", err)
	}
}

// server holds the shared printer state. The struct exists so tests
// can inject a logger and a fixed auth header without touching env.
type server struct {
	expectedAuth string
	out          *log.Logger
}

func newServer(expectedAuth string, out *log.Logger) *server {
	return &server{expectedAuth: expectedAuth, out: out}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.expectedAuth != "" {
		// Constant-time comparison defends against timing-side-channel
		// attacks even when this binary is briefly exposed beyond a
		// trusted network boundary. crypto/subtle.ConstantTimeCompare
		// returns 0 when the two byte slices differ in any byte (or
		// in length), and 1 on exact match.
		given := []byte(r.Header.Get("X-Kapro-Auth"))
		want := []byte(s.expectedAuth)
		if subtle.ConstantTimeCompare(given, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB ceiling
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var env events.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "unmarshal cloudevent: "+err.Error(), http.StatusBadRequest)
		return
	}
	if env.SpecVersion != "1.0" {
		http.Error(w, fmt.Sprintf("unsupported CloudEvents specversion %q", env.SpecVersion),
			http.StatusBadRequest)
		return
	}
	s.out.Println(formatEvent(env))
	w.WriteHeader(http.StatusNoContent)
}

// formatEvent renders one CloudEvents envelope into a single
// fleet-narrative-shaped line. Layout:
//
//	<UTC time> <type-short> promo=<name>[ wave=<w>][ stage=<s>][ gate=<g>][ target=<t>] phase=<X>[ -> <Y>] version=<v>
//
// Examples:
//
//	2026-05-19T22:00:01Z promotion.succeeded     promo=checkout phase=Progressing -> Succeeded version=v1.2.3
//	2026-05-19T22:00:05Z stage.gate.passed       promo=checkout wave=default stage=canary gate=metrics target=fi-prod phase=Progressing version=v1.2.3
func formatEvent(env events.Envelope) string {
	short := strings.TrimPrefix(string(env.Type), "kapro.io/")
	d := env.Data
	parts := []string{
		env.Time,
		padRight(short, 26),
		"promo=" + d.Promotion,
	}
	if d.Wave != "" {
		parts = append(parts, "wave="+d.Wave)
	}
	if d.Stage != "" {
		parts = append(parts, "stage="+d.Stage)
	}
	if d.Gate != "" {
		parts = append(parts, "gate="+d.Gate)
	}
	if d.Target != "" {
		parts = append(parts, "target="+d.Target)
	}
	phase := "phase=" + d.Phase
	if d.PreviousPhase != "" && d.PreviousPhase != d.Phase {
		phase = "phase=" + d.PreviousPhase + " -> " + d.Phase
	}
	parts = append(parts, phase)
	if d.Version != "" {
		parts = append(parts, "version="+d.Version)
	}
	if d.Reason != "" {
		parts = append(parts, "reason="+d.Reason)
	}
	return strings.Join(parts, " ")
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

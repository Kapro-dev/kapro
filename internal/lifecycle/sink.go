package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/events"
)

const (
	// SinkEnvURL is the operator-level CloudEvents subscriber URL. When
	// set the Dispatcher publishes every fleet-promotion event to this
	// endpoint as a CloudEvents v1.0 structured-mode JSON envelope.
	//
	// HTTPS is required unless KAPRO_LIFECYCLE_INSECURE_WEBHOOKS=1 is
	// also set (matches per-Promotion webhook semantics).
	SinkEnvURL = "KAPRO_EVENTS_SINK_URL"

	// SinkEnvAuthHeaderName customizes the header used to inject the
	// auth value below. Defaults to "Authorization".
	SinkEnvAuthHeaderName = "KAPRO_EVENTS_SINK_AUTH_HEADER_NAME"

	// SinkEnvAuthHeaderValue holds the literal header value (e.g.
	// "Bearer xoxb-..."). Operators should source this from a Secret via
	// the Deployment spec's `valueFrom.secretKeyRef` rather than baking
	// the literal into a plaintext field.
	SinkEnvAuthHeaderValue = "KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE"

	// SinkEnvTimeout caps each sink delivery attempt (parsed by
	// time.ParseDuration). Defaults to 10s.
	SinkEnvTimeout = "KAPRO_EVENTS_SINK_TIMEOUT"

	// SinkEnvMaxRetries bounds transient-failure retries per event.
	// Defaults to 3.
	SinkEnvMaxRetries = "KAPRO_EVENTS_SINK_MAX_RETRIES"

	defaultSinkTimeout    = 10 * time.Second
	defaultSinkMaxRetries = 3
)

// Sink is the operator-level CloudEvents subscriber. It is the canonical
// integration point for downstream CNCF projects (Argo Events, Flux
// Notification Controller, kube-event-exporter, Knative). One Sink per
// operator; receives every Kapro fleet-promotion event.
type Sink struct {
	URL             string
	AuthHeaderName  string
	AuthHeaderValue string
	Timeout         time.Duration
	MaxRetries      int32
	HTTPClient      *http.Client
}

// SinkFromEnv constructs a Sink from environment variables. Returns nil
// when SinkEnvURL is unset (sink is disabled). Returns an error when the
// URL is malformed or the timeout cannot be parsed — let the operator
// fail fast at startup rather than silently dropping events.
func SinkFromEnv() (*Sink, error) {
	url := strings.TrimSpace(os.Getenv(SinkEnvURL))
	if url == "" {
		return nil, nil
	}
	allowInsecure := os.Getenv(allowInsecureEnv) == "1"
	if _, err := validateWebhookURL(url, allowInsecure); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", SinkEnvURL, err)
	}

	timeout := defaultSinkTimeout
	if raw := os.Getenv(SinkEnvTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", SinkEnvTimeout, raw, err)
		}
		timeout = d
	}

	retries := int32(defaultSinkMaxRetries)
	if raw := os.Getenv(SinkEnvMaxRetries); raw != "" {
		var n int32
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 0 {
			return nil, fmt.Errorf("invalid %s %q: must be non-negative integer", SinkEnvMaxRetries, raw)
		}
		retries = n
	}

	headerName := strings.TrimSpace(os.Getenv(SinkEnvAuthHeaderName))
	if headerName == "" {
		headerName = "Authorization"
	}

	return &Sink{
		URL:             url,
		AuthHeaderName:  headerName,
		AuthHeaderValue: os.Getenv(SinkEnvAuthHeaderValue),
		Timeout:         timeout,
		MaxRetries:      retries,
		HTTPClient:      defaultHTTPClient(),
	}, nil
}

// Publish sends a single CloudEvents envelope to the sink with
// linear-backoff retries on transient failures. Sink failures are
// observability-grade: they emit Kubernetes Events and Prometheus
// metrics but never block the Promotion FSM or per-Promotion handlers.
func (s *Sink) Publish(ctx context.Context, rec record.EventRecorder, target *corev1.ObjectReference, ev events.Event) {
	if s == nil || s.URL == "" {
		return
	}
	body, _, err := events.Render(ev)
	if err != nil {
		s.observeFailure(ctx, rec, target, ev, 0, 0, err)
		return
	}

	start := time.Now()
	retries := max(s.MaxRetries, 0)

	var lastStatus int
	var lastErr error
	var madeAttempts int32

retryLoop:
	for attempt := int32(1); attempt <= retries+1; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, s.attemptTimeout(retries))
		status, err := s.doRequest(attemptCtx, body)
		cancel()
		lastStatus = status
		lastErr = err
		madeAttempts = attempt
		if err == nil && status >= 200 && status < 300 {
			s.observeSuccess(rec, target, ev, time.Since(start), madeAttempts, status)
			return
		}
		if isPermanentFailure(status, err) {
			break
		}
		if attempt > retries {
			break
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			break retryLoop
		case <-time.After(backoffBase * time.Duration(attempt)):
		}
	}
	if lastErr == nil && lastStatus > 0 {
		lastErr = fmt.Errorf("sink returned HTTP %d", lastStatus)
	}
	s.observeFailure(ctx, rec, target, ev, lastStatus, madeAttempts, lastErr)
}

func (s *Sink) attemptTimeout(retries int32) time.Duration {
	if retries < 0 {
		retries = 0
	}
	div := time.Duration(retries + 1)
	if div <= 0 {
		div = 1
	}
	return s.Timeout / div
}

func (s *Sink) doRequest(ctx context.Context, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build sink request: %w", err)
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	if s.AuthHeaderValue != "" {
		req.Header.Set(s.AuthHeaderName, s.AuthHeaderValue)
	}
	client := s.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func (s *Sink) observeSuccess(rec record.EventRecorder, target *corev1.ObjectReference,
	ev events.Event, dur time.Duration, attempts int32, status int) {
	metrics.LifecycleHookInvocations.WithLabelValues("Sink", string(ev.Type), "succeeded").Inc()
	metrics.LifecycleHookDuration.WithLabelValues("Sink", string(ev.Type)).Observe(dur.Seconds())
	if rec != nil && target != nil {
		rec.Eventf(target, corev1.EventTypeNormal, "EventSinkDelivered",
			"published %s to operator sink in %dms (HTTP %d, attempts=%d)",
			ev.Type, dur.Milliseconds(), status, attempts)
	}
}

func (s *Sink) observeFailure(ctx context.Context, rec record.EventRecorder, target *corev1.ObjectReference,
	ev events.Event, status int, attempts int32, err error) {
	metrics.LifecycleHookInvocations.WithLabelValues("Sink", string(ev.Type), "failed").Inc()
	logf.FromContext(ctx).Error(err, "publish to operator events sink failed",
		"type", string(ev.Type),
		"promotion", ev.PromotionName,
		"httpStatus", status,
		"attempts", attempts,
	)
	if rec != nil && target != nil {
		rec.Eventf(target, corev1.EventTypeWarning, "EventSinkFailed",
			"failed publishing %s to operator sink (HTTP %d, attempts=%d): %v",
			ev.Type, status, attempts, err)
	}
}

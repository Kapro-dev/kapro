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

	// SinkEnvTimeout is the total per-event delivery budget — initial
	// attempt plus all retries plus backoff sleeps must complete within
	// this window. Parsed by time.ParseDuration. Defaults to 10s. Set
	// generously when MaxRetries is high; per-attempt sub-budgets are
	// derived from the remaining overall budget.
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
// Publish enforces s.Timeout as the total-per-event budget (initial
// attempt + all retries + backoff sleeps). Each individual attempt
// derives its sub-deadline from the remaining budget; once the overall
// deadline expires, the loop exits even mid-backoff.
func (s *Sink) Publish(ctx context.Context, rec record.EventRecorder, target *corev1.ObjectReference, ev events.Event) {
	if s == nil || s.URL == "" {
		return
	}
	overallCtx, cancelOverall := context.WithTimeout(ctx, s.Timeout)
	defer cancelOverall()

	start := time.Now()

	body, _, err := events.Render(ev)
	if err != nil {
		s.observeFailure(overallCtx, rec, target, ev, time.Since(start), 0, 0, err)
		return
	}

	retries := max(s.MaxRetries, 0)

	var lastStatus int
	var lastErr error
	var madeAttempts int32

retryLoop:
	for attempt := int32(1); attempt <= retries+1; attempt++ {
		// Per-attempt sub-deadline = remaining overall budget divided by
		// the attempts still available. Guarantees the first attempt has
		// room for retries; the final attempt gets the rest.
		remaining := time.Until(deadlineOrFar(overallCtx))
		attemptsLeft := time.Duration((retries + 1) - attempt + 1)
		if attemptsLeft <= 0 {
			attemptsLeft = 1
		}
		attemptBudget := remaining / attemptsLeft
		attemptCtx, cancel := context.WithTimeout(overallCtx, attemptBudget)
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
		case <-overallCtx.Done():
			lastErr = overallCtx.Err()
			break retryLoop
		case <-time.After(backoffBase * time.Duration(attempt)):
		}
	}
	if lastErr == nil && lastStatus > 0 {
		lastErr = fmt.Errorf("sink returned HTTP %d", lastStatus)
	}
	s.observeFailure(overallCtx, rec, target, ev, time.Since(start), lastStatus, madeAttempts, lastErr)
}

// deadlineOrFar returns ctx's deadline, or a far-future time when the
// context has no deadline. Used to compute remaining budget without
// special-casing the unset-deadline path.
func deadlineOrFar(ctx context.Context) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(time.Hour)
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

// observeSuccess and observeFailure both record metrics with the
// {kind, phase, result} schema shared by the per-Promotion handler
// path. The `phase` label is the Promotion phase (e.g. "Succeeded"),
// NOT the CloudEvents type — keeping the cardinality predictable and
// dashboards/alerts consistent between sink and per-Promotion deliveries.
// The event type is carried in the Kubernetes Event message instead.

func (s *Sink) observeSuccess(rec record.EventRecorder, target *corev1.ObjectReference,
	ev events.Event, dur time.Duration, attempts int32, status int) {
	metrics.LifecycleHookInvocations.WithLabelValues("Sink", ev.Phase, "succeeded").Inc()
	metrics.LifecycleHookDuration.WithLabelValues("Sink", ev.Phase).Observe(dur.Seconds())
	if rec != nil && target != nil {
		rec.Eventf(target, corev1.EventTypeNormal, "EventSinkDelivered",
			"published %s to operator sink in %dms (HTTP %d, attempts=%d)",
			ev.Type, dur.Milliseconds(), status, attempts)
	}
}

func (s *Sink) observeFailure(ctx context.Context, rec record.EventRecorder, target *corev1.ObjectReference,
	ev events.Event, dur time.Duration, status int, attempts int32, err error) {
	metrics.LifecycleHookInvocations.WithLabelValues("Sink", ev.Phase, "failed").Inc()
	// Histogram observes end-to-end dispatch time including retries and
	// backoff, regardless of outcome. Subscribers debugging slow sinks
	// need failure latency just as much as success latency.
	metrics.LifecycleHookDuration.WithLabelValues("Sink", ev.Phase).Observe(dur.Seconds())
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

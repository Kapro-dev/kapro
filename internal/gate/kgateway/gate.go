// Package kgateway implements a Gate that evaluates promotion readiness by
// inspecting kgateway (https://kgateway.dev) traffic policies and backend health.
//
// kgateway is the CNCF Gateway API implementation that extends the Kubernetes
// Gateway API with AI-native routing, traffic mirroring, and advanced traffic
// management.
//
// The gate scrapes the kgateway Prometheus telemetry endpoint and evaluates:
//
//  1. HTTP mode (default): canary backend error rate + optional weight validation
//     via Envoy-compatible metrics exposed by kgateway.
//
//  2. AI mode: LLMProvider backend p99 latency + error rate via kgateway AI
//     Gateway metrics (kgateway_ai_request_duration_seconds, etc.).
//
// Example MetricGate configuration (JSON in MetricGate.Config):
//
//	{
//	  "namespace":           "gateway-system",
//	  "http_route":          "ocs-canary",
//	  "canary_backend":      "ocs-v2",
//	  "expected_weight":     20,
//	  "error_threshold":     0.01,
//	  "telemetry_endpoint":  "http://kgateway.gateway-system.svc:9091/metrics",
//	  "mode":                "http"
//	}
//
// AI mode example:
//
//	{
//	  "mode":               "ai",
//	  "ai_backend":         "gpt4-backend",
//	  "error_threshold":    0.05,
//	  "max_p99_latency_ms": 3000,
//	  "telemetry_endpoint": "http://kgateway.gateway-system.svc:9091/metrics"
//	}
package kgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/gate"
)

const (
	defaultTelemetryEndpoint = "http://kgateway.gateway-system.svc:9091/metrics"
	defaultErrorThreshold    = 0.01
	defaultMaxP99LatencyMs   = 2000.0

	modeHTTP = "http"
	modeAI   = "ai"
)

// Config is the JSON-encoded gate configuration stored in MetricGate.Config.
type Config struct {
	// Namespace is the Kubernetes namespace where kgateway resources live.
	// Default: "gateway-system".
	Namespace string `json:"namespace,omitempty"`

	// HTTPRoute is the name of the Gateway API HTTPRoute (informational — used
	// in log messages and weight validation context).
	HTTPRoute string `json:"http_route,omitempty"`

	// CanaryBackend is the Envoy cluster name (or Kubernetes service name) for
	// the canary version. Used to filter backend-specific metrics.
	CanaryBackend string `json:"canary_backend,omitempty"`

	// ExpectedWeight is the canary traffic percentage that should be active
	// (0–100). The gate fails if actual weight deviates by more than 5 points.
	// Set to 0 to skip weight validation.
	ExpectedWeight int `json:"expected_weight,omitempty"`

	// ErrorThreshold is the maximum acceptable HTTP 5xx error rate on the canary,
	// expressed as a fraction (0.0–1.0). Default: 0.01 (1%).
	ErrorThreshold float64 `json:"error_threshold,omitempty"`

	// TelemetryEndpoint is the Prometheus metrics URL exposed by kgateway.
	// Default: http://kgateway.gateway-system.svc:9091/metrics
	TelemetryEndpoint string `json:"telemetry_endpoint,omitempty"`

	// Mode selects the evaluation strategy.
	// "http" (default): evaluates HTTP backend health.
	// "ai":             evaluates LLMProvider backend latency and error rate.
	Mode string `json:"mode,omitempty"`

	// AIBackend is the LLMProvider resource name to evaluate in AI mode.
	AIBackend string `json:"ai_backend,omitempty"`

	// MaxP99LatencyMs is the maximum acceptable p99 latency in milliseconds
	// for the AI backend. Default: 2000ms.
	MaxP99LatencyMs float64 `json:"max_p99_latency_ms,omitempty"`
}

func (c *Config) applyDefaults() {
	if c.TelemetryEndpoint == "" {
		c.TelemetryEndpoint = defaultTelemetryEndpoint
	}
	if c.Namespace == "" {
		c.Namespace = "gateway-system"
	}
	if c.Mode == "" {
		c.Mode = modeHTTP
	}
	if c.ErrorThreshold == 0 {
		c.ErrorThreshold = defaultErrorThreshold
	}
	if c.MaxP99LatencyMs == 0 {
		c.MaxP99LatencyMs = defaultMaxP99LatencyMs
	}
}

func (c *Config) validate() error {
	if c.Mode == modeAI && c.AIBackend == "" {
		return fmt.Errorf("kgateway gate: ai_backend is required in AI mode")
	}
	return nil
}

// Gate implements KGI for kgateway traffic policy validation.
//
// It is safe for concurrent use. HTTPClient defaults to a 10-second timeout
// client when nil.
type Gate struct {
	HTTPClient *http.Client
}

func (g *Gate) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Evaluate checks kgateway backend health via the telemetry endpoint.
func (g *Gate) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	if req.Policy == nil || req.MetricIndex >= len(req.Policy.Spec.Gate.Metrics) {
		return gate.Result{Passed: true, Message: "no kgateway gate configured"}, nil
	}

	metric := req.Policy.Spec.Gate.Metrics[req.MetricIndex]

	var cfg Config
	if len(metric.Config) > 0 {
		if err := json.Unmarshal(metric.Config, &cfg); err != nil {
			return gate.Result{}, fmt.Errorf("kgateway gate: invalid config JSON: %w", err)
		}
	} else {
		cfg.TelemetryEndpoint = metric.Endpoint
		cfg.HTTPRoute = metric.Query
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return gate.Result{}, err
	}

	logger := log.FromContext(ctx)
	logger.Info("kgateway gate evaluating", "mode", cfg.Mode, "route", cfg.HTTPRoute)

	metrics, err := g.fetchMetrics(ctx, cfg.TelemetryEndpoint)
	if err != nil {
		return gate.Result{}, fmt.Errorf("kgateway gate: fetch metrics: %w", err)
	}

	switch cfg.Mode {
	case modeAI:
		return g.evaluateAI(cfg, metrics)
	default:
		return g.evaluateHTTP(cfg, metrics)
	}
}

// evaluateHTTP checks HTTP backend health via Envoy-compatible kgateway metrics.
//
// Metrics used:
//   - envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name=<backend>}
//   - envoy_cluster_upstream_rq_total{envoy_cluster_name=<backend>}
//   - kgateway_route_backend_weight{route=<route>,backend=<backend>}
func (g *Gate) evaluateHTTP(cfg Config, metrics *GatewayMetrics) (gate.Result, error) {
	if cfg.CanaryBackend == "" {
		// No specific backend — verify gateway is up and healthy overall.
		if !metrics.GatewayHealthy {
			return gate.Result{
				Passed:     false,
				Message:    "kgateway: gateway not in healthy state",
				RetryAfter: "15s",
			}, nil
		}
		return gate.Result{Passed: true, Message: "kgateway: gateway healthy"}, nil
	}

	errRate := metrics.BackendErrorRate(cfg.CanaryBackend)
	if errRate > cfg.ErrorThreshold {
		return gate.Result{
			Passed:  false,
			Message: fmt.Sprintf("kgateway: canary %q error rate %.2f%% > threshold %.2f%%", cfg.CanaryBackend, errRate*100, cfg.ErrorThreshold*100),
		}, nil
	}

	// Validate canary weight when configured.
	if cfg.ExpectedWeight > 0 {
		actualWeight, ok := metrics.BackendWeight(cfg.CanaryBackend)
		if ok {
			drift := actualWeight - cfg.ExpectedWeight
			if drift < 0 {
				drift = -drift
			}
			if drift > 5 {
				return gate.Result{
					Passed:  false,
					Message: fmt.Sprintf("kgateway: canary weight %d%% deviates >5 points from expected %d%%", actualWeight, cfg.ExpectedWeight),
				}, nil
			}
		}
	}

	return gate.Result{
		Passed:  true,
		Message: fmt.Sprintf("kgateway: canary %q error rate %.2f%%", cfg.CanaryBackend, errRate*100),
	}, nil
}

// evaluateAI checks AI backend health via kgateway AI Gateway metrics.
//
// Metrics used:
//   - kgateway_ai_request_duration_seconds{quantile="0.99",backend=<ai_backend>}
//   - kgateway_ai_request_errors_total{backend=<ai_backend>}
//   - kgateway_ai_requests_total{backend=<ai_backend>}
func (g *Gate) evaluateAI(cfg Config, metrics *GatewayMetrics) (gate.Result, error) {
	errRate := metrics.AIBackendErrorRate(cfg.AIBackend)
	if errRate > cfg.ErrorThreshold {
		return gate.Result{
			Passed:  false,
			Message: fmt.Sprintf("kgateway AI: backend %q error rate %.2f%% > threshold %.2f%%", cfg.AIBackend, errRate*100, cfg.ErrorThreshold*100),
		}, nil
	}

	p99Ms := metrics.AIBackendP99LatencyMs(cfg.AIBackend)
	if p99Ms > cfg.MaxP99LatencyMs {
		return gate.Result{
			Passed:  false,
			Message: fmt.Sprintf("kgateway AI: backend %q p99 latency %.0fms > max %.0fms", cfg.AIBackend, p99Ms, cfg.MaxP99LatencyMs),
		}, nil
	}

	return gate.Result{
		Passed:  true,
		Message: fmt.Sprintf("kgateway AI: backend %q p99=%.0fms error=%.2f%%", cfg.AIBackend, p99Ms, errRate*100),
	}, nil
}

// fetchMetrics scrapes the kgateway Prometheus endpoint and parses metrics.
func (g *Gate) fetchMetrics(ctx context.Context, endpoint string) (*GatewayMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from kgateway metrics", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read kgateway metrics: %w", err)
	}

	return parsePrometheusMetrics(body), nil
}

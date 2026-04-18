package kgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/gate"
)

// metricsResponse builds a minimal Prometheus text payload for tests.
func metricsResponse(lines ...string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func serveMetrics(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, body)
	}))
}

func makeHTTPRequest(t *testing.T, server *httptest.Server, backend string, expectedWeight int) gate.Request {
	t.Helper()
	cfg, _ := json.Marshal(Config{
		TelemetryEndpoint: server.URL,
		CanaryBackend:     backend,
		ExpectedWeight:    expectedWeight,
		ErrorThreshold:    0.01,
		Mode:              modeHTTP,
	})
	return gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{
						{Provider: "kgateway", Config: cfg},
					},
				},
			},
		},
	}
}

// ── HTTP mode ────────────────────────────────────────────────────────────────

func TestHTTPMode_Passes_WhenErrorRateBelowThreshold(t *testing.T) {
	metrics := metricsResponse(
		`envoy_cluster_upstream_rq_total{envoy_cluster_name="ocs-v2"} 1000`,
		`envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="ocs-v2"} 5`,
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeHTTPRequest(t, srv, "ocs-v2", 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true, got false: %s", result.Message)
	}
}

func TestHTTPMode_Blocks_WhenErrorRateTooHigh(t *testing.T) {
	metrics := metricsResponse(
		`envoy_cluster_upstream_rq_total{envoy_cluster_name="ocs-v2"} 100`,
		`envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="ocs-v2"} 10`,
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeHTTPRequest(t, srv, "ocs-v2", 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on 10%% error rate, got true: %s", result.Message)
	}
}

func TestHTTPMode_Blocks_WhenWeightDriftTooHigh(t *testing.T) {
	metrics := metricsResponse(
		`envoy_cluster_upstream_rq_total{envoy_cluster_name="ocs-v2"} 1000`,
		`envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="ocs-v2"} 2`,
		`kgateway_route_backend_weight{route="ocs-canary",backend="ocs-v2"} 5`, // expected 20, actual 5
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeHTTPRequest(t, srv, "ocs-v2", 20))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on weight drift >5, got true: %s", result.Message)
	}
}

func TestHTTPMode_Passes_WhenWeightWithinTolerance(t *testing.T) {
	metrics := metricsResponse(
		`envoy_cluster_upstream_rq_total{envoy_cluster_name="ocs-v2"} 1000`,
		`envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="ocs-v2"} 2`,
		`kgateway_route_backend_weight{route="ocs-canary",backend="ocs-v2"} 22`, // expected 20, drift=2
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeHTTPRequest(t, srv, "ocs-v2", 20))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true within 5-point tolerance, got: %s", result.Message)
	}
}

func TestHTTPMode_NoBackend_ChecksGatewayHealth(t *testing.T) {
	metrics := metricsResponse(`kgateway_gateway_ready{} 1`)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	cfg, _ := json.Marshal(Config{TelemetryEndpoint: srv.URL, Mode: modeHTTP})
	req := gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{{Provider: "kgateway", Config: cfg}},
				},
			},
		},
	}

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true (gateway healthy), got: %s", result.Message)
	}
}

func TestHTTPMode_Blocks_WhenGatewayUnhealthy(t *testing.T) {
	metrics := metricsResponse(`kgateway_gateway_ready{} 0`)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	cfg, _ := json.Marshal(Config{TelemetryEndpoint: srv.URL, Mode: modeHTTP})
	req := gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{{Provider: "kgateway", Config: cfg}},
				},
			},
		},
	}

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false when gateway_ready=0, got true: %s", result.Message)
	}
}

// ── AI mode ──────────────────────────────────────────────────────────────────

func makeAIRequest(t *testing.T, server *httptest.Server, aiBackend string) gate.Request {
	t.Helper()
	cfg, _ := json.Marshal(Config{
		TelemetryEndpoint: server.URL,
		Mode:              modeAI,
		AIBackend:         aiBackend,
		ErrorThreshold:    0.05,
		MaxP99LatencyMs:   2000,
	})
	return gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{{Provider: "kgateway", Config: cfg}},
				},
			},
		},
	}
}

func TestAIMode_Passes_WhenLatencyAndErrorRateAcceptable(t *testing.T) {
	metrics := metricsResponse(
		`kgateway_ai_requests_total{backend="gpt4"} 500`,
		`kgateway_ai_request_errors_total{backend="gpt4"} 10`,
		`kgateway_ai_request_duration_seconds{quantile="0.99",backend="gpt4"} 1.5`,
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeAIRequest(t, srv, "gpt4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true, got false: %s", result.Message)
	}
}

func TestAIMode_Blocks_WhenP99TooHigh(t *testing.T) {
	metrics := metricsResponse(
		`kgateway_ai_requests_total{backend="gpt4"} 500`,
		`kgateway_ai_request_errors_total{backend="gpt4"} 5`,
		`kgateway_ai_request_duration_seconds{quantile="0.99",backend="gpt4"} 3.5`, // 3500ms > 2000ms
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeAIRequest(t, srv, "gpt4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on p99 latency 3500ms, got true: %s", result.Message)
	}
}

func TestAIMode_Blocks_WhenErrorRateTooHigh(t *testing.T) {
	metrics := metricsResponse(
		`kgateway_ai_requests_total{backend="gpt4"} 100`,
		`kgateway_ai_request_errors_total{backend="gpt4"} 10`, // 10% > 5% threshold
		`kgateway_ai_request_duration_seconds{quantile="0.99",backend="gpt4"} 0.5`,
	)
	srv := serveMetrics(t, metrics)
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeAIRequest(t, srv, "gpt4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on AI error rate 10%%, got true: %s", result.Message)
	}
}

func TestAIMode_MissingAIBackend_Errors(t *testing.T) {
	srv := serveMetrics(t, "")
	defer srv.Close()

	cfg, _ := json.Marshal(Config{TelemetryEndpoint: srv.URL, Mode: modeAI})
	req := gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{{Provider: "kgateway", Config: cfg}},
				},
			},
		},
	}

	g := &Gate{}
	_, err := g.Evaluate(context.Background(), req)
	if err == nil {
		t.Error("expected error when ai_backend is missing in AI mode")
	}
}

// ── metrics parser ────────────────────────────────────────────────────────────

func TestParsePrometheusMetrics_ExtractsAllFamilies(t *testing.T) {
	payload := metricsResponse(
		`# HELP envoy_cluster_upstream_rq_total total requests`,
		`# TYPE envoy_cluster_upstream_rq_total counter`,
		`envoy_cluster_upstream_rq_total{envoy_cluster_name="svc-a"} 200`,
		`envoy_cluster_upstream_rq_xx{envoy_response_code_class="5",envoy_cluster_name="svc-a"} 4`,
		`kgateway_route_backend_weight{route="r1",backend="svc-a"} 30`,
		`kgateway_ai_requests_total{backend="llm-1"} 1000`,
		`kgateway_ai_request_errors_total{backend="llm-1"} 20`,
		`kgateway_ai_request_duration_seconds{quantile="0.99",backend="llm-1"} 1.2`,
		`kgateway_gateway_ready{} 1`,
	)

	m := parsePrometheusMetrics([]byte(payload))

	if m.BackendErrorRate("svc-a") != 0.02 {
		t.Errorf("expected error rate 0.02, got %v", m.BackendErrorRate("svc-a"))
	}
	w, ok := m.BackendWeight("svc-a")
	if !ok || w != 30 {
		t.Errorf("expected weight 30 for svc-a, got %d ok=%v", w, ok)
	}
	if m.AIBackendErrorRate("llm-1") != 0.02 {
		t.Errorf("expected AI error rate 0.02, got %v", m.AIBackendErrorRate("llm-1"))
	}
	if m.AIBackendP99LatencyMs("llm-1") != 1200 {
		t.Errorf("expected p99 1200ms, got %v", m.AIBackendP99LatencyMs("llm-1"))
	}
	if !m.GatewayHealthy {
		t.Error("expected GatewayHealthy=true")
	}
}

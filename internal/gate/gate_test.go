package gate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/gate"
)

// ---- SoakGate ---------------------------------------------------------------

func TestSoakGate_NoPolicy(t *testing.T) {
	g := &gate.SoakGate{}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{},
		Policy:  nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Error("expected Passed=true when policy is nil")
	}
}

func TestSoakGate_NoSoakTime(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{SoakTime: ""},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{},
		Policy:  policy,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Error("expected Passed=true when soakTime is empty")
	}
}

func TestSoakGate_ClockNotStarted(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{SoakTime: "5m"},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{},
		Policy:  policy,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false when StartedAt is empty")
	}
}

func TestSoakGate_Elapsed(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{SoakTime: "1ms"},
	}
	time.Sleep(5 * time.Millisecond) // ensure soak elapsed
	promo := &gate.Context{StartedAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo, Policy: policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true after soak elapsed, got message: %s", result.Message)
	}
}

func TestSoakGate_NotElapsed(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{SoakTime: "1h"},
	}
	promo := &gate.Context{StartedAt: time.Now().UTC().Format(time.RFC3339)}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo, Policy: policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false when soak has not elapsed")
	}
	if result.RetryAfter == "" {
		t.Error("expected non-empty RetryAfter when soak has not elapsed")
	}
}

func TestSoakGate_InvalidDuration(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{SoakTime: "not-a-duration"},
	}
	_, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{},
		Policy:  policy,
	})
	if err == nil {
		t.Error("expected error for invalid soakTime duration")
	}
}

// ---- MetricsGate ------------------------------------------------------------

// prometheusVectorResponse builds a minimal Prometheus instant-vector JSON response.
func prometheusVectorResponse(value string) []byte {
	resp := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"resultType": "vector",
			"result": []map[string]interface{}{
				{
					"metric": map[string]string{},
					"value":  []interface{}{float64(1609459200), value},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func prometheusEmptyVectorResponse() []byte {
	resp := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"resultType": "vector",
			"result":     []interface{}{},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func prometheusMatrixResponse(values ...string) []byte {
	points := make([]interface{}, 0, len(values))
	for i, value := range values {
		points = append(points, []interface{}{float64(1609459200 + i*30), value})
	}
	resp := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"resultType": "matrix",
			"result": []map[string]interface{}{
				{
					"metric": map[string]string{},
					"values": points,
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func TestMetricsGate_NoMetrics(t *testing.T) {
	g := &gate.MetricsGate{}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context:     &gate.Context{},
		Policy:      &kaprov1alpha1.GatePolicySpec{},
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Error("expected Passed=true when no metrics configured")
	}
}

func TestMetricsGate_Passed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusVectorResponse("1"))
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{
				{Provider: "prometheus", Query: "up", Window: "5m", Endpoint: srv.URL},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context:     &gate.Context{},
		Policy:      policy,
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true, got: %s", result.Message)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("expected one evidence entry, got %d", len(result.Evidence))
	}
	if result.Evidence[0].AnalysisMode != "threshold" {
		t.Fatalf("expected threshold evidence, got %q", result.Evidence[0].AnalysisMode)
	}
}

func TestMetricsGate_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{
				{Provider: "prometheus", Query: "up == 0", Window: "5m", Endpoint: srv.URL},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context:     &gate.Context{},
		Policy:      policy,
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false for empty vector response")
	}
}

func TestMetricsGate_PrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{Provider: "prometheus", Query: "up", Endpoint: srv.URL}},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Error is non-fatal — gate should block with retry, not return an error.
	if result.IsPassed() {
		t.Error("expected Passed=false on prometheus error")
	}
	if result.RetryAfter == "" {
		t.Error("expected RetryAfter on prometheus error")
	}
}

func TestMetricsGate_NonFiniteValueBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusVectorResponse("NaN"))
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{Provider: "prometheus", Query: "up", Endpoint: srv.URL}},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Fatal("expected NaN result to block the gate")
	}
}

func TestMetricsGate_SLOBurnRateUsesLTEComparator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusVectorResponse("1.5"))
	}))
	defer srv.Close()

	threshold := 2.0
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "burn_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis:  &kaprov1alpha1.MetricAnalysisSpec{Mode: "sloBurnRate"},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Fatalf("expected SLO burn-rate gate to pass, got %s", result.Message)
	}
	if got := result.Evidence[0].Comparator; got != "lte" {
		t.Fatalf("expected lte comparator, got %q", got)
	}
}

func TestMetricsGate_SLOBurnRateEmptyResultIsInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	threshold := 2.0
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "burn_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis:  &kaprov1alpha1.MetricAnalysisSpec{Mode: "sloBurnRate"},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseInconclusive {
		t.Fatalf("expected empty burn-rate result to be inconclusive, got %s", result.Phase)
	}
	if result.RetryAfter == "" {
		t.Fatal("expected RetryAfter for empty burn-rate result")
	}
	if got := result.Evidence[0].SampleCount; got != 0 {
		t.Fatalf("expected sample count 0, got %d", got)
	}
}

func TestMetricsGate_BaselineRatioBlocksWhenRegressionTooLarge(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		if requests == 1 {
			_, _ = w.Write(prometheusVectorResponse("12"))
			return
		}
		_, _ = w.Write(prometheusVectorResponse("10"))
	}))
	defer srv.Close()

	threshold := 1.1
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "canary_error_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:          "baseline",
					BaselineQuery: "baseline_error_rate",
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Fatalf("expected baseline regression to block")
	}
	if got := result.Evidence[0].ObservedValue; got != "1.2" {
		t.Fatalf("expected observed ratio 1.2, got %q", got)
	}
	if got := result.Evidence[0].BaselineValue; got != "10" {
		t.Fatalf("expected baseline 10, got %q", got)
	}
}

func TestMetricsGate_BaselineEmptyResultIsInconclusive(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		if requests == 1 {
			_, _ = w.Write(prometheusVectorResponse("1"))
			return
		}
		_, _ = w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	threshold := 1.1
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "canary_error_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:          "baseline",
					BaselineQuery: "baseline_error_rate",
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseInconclusive {
		t.Fatalf("expected empty baseline result to be inconclusive, got %s", result.Phase)
	}
	if got := result.Evidence[0].SampleCount; got != 0 {
		t.Fatalf("expected sample count 0, got %d", got)
	}
}

func TestMetricsGate_SequentialRequiresMinimumSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusMatrixResponse("1", "1"))
	}))
	defer srv.Close()

	threshold := 0.5
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "success_rate",
				Window:    "5m",
				Interval:  "30s",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:       "sequential",
					MinSamples: 3,
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseInconclusive {
		t.Fatalf("expected inconclusive, got %s", result.Phase)
	}
	if got := result.Evidence[0].SampleCount; got != 2 {
		t.Fatalf("expected sample count 2, got %d", got)
	}
}

func TestMetricsGate_SequentialPassesWithConfidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusMatrixResponse("1", "1", "1", "1", "1", "1"))
	}))
	defer srv.Close()

	threshold := 0.1
	confidence := 0.8
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "success_rate",
				Window:    "5m",
				Interval:  "30s",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:                "sequential",
					MinSamples:          5,
					ConfidenceThreshold: &confidence,
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Fatalf("expected sequential gate to pass, got %s", result.Message)
	}
	if result.Evidence[0].Confidence == nil {
		t.Fatal("expected confidence evidence")
	}
	if result.Evidence[0].PValue == nil {
		t.Fatal("expected p-value evidence")
	}
}

func TestMetricsGate_ChangePointFailsOnRegression(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusMatrixResponse("1", "1", "1", "1", "5", "5", "5", "5"))
	}))
	defer srv.Close()

	threshold := 0.0
	alpha := 0.05
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "latency",
				Window:    "5m",
				Interval:  "30s",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:       "changePoint",
					Comparator: "lte",
					MinSamples: 8,
					Alpha:      &alpha,
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseFailed {
		t.Fatalf("expected regression to fail, got %s", result.Phase)
	}
	if result.Evidence[0].PValue == nil {
		t.Fatal("expected p-value evidence")
	}
	if result.Evidence[0].DecisionRule != "split-window change-point test" {
		t.Fatalf("unexpected decision rule %q", result.Evidence[0].DecisionRule)
	}
}

func TestMetricsGate_ScoreBlocksBelowThreshold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusVectorResponse("0.10"))
	}))
	defer srv.Close()

	threshold := 0.05
	scoreThreshold := 90.0
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "error_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:           "score",
					ScoreThreshold: &scoreThreshold,
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Fatal("expected score gate to block")
	}
	if result.Evidence[0].Score == nil || *result.Evidence[0].Score != 50 {
		t.Fatalf("expected score 50, got %#v", result.Evidence[0].Score)
	}
}

func TestMetricsGate_ScoreEmptyResultIsInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	threshold := 0.05
	scoreThreshold := 90.0
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "error_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:           "score",
					ScoreThreshold: &scoreThreshold,
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseInconclusive {
		t.Fatalf("expected empty score result to be inconclusive, got %s", result.Phase)
	}
	if result.RetryAfter == "" {
		t.Fatal("expected RetryAfter for empty score result")
	}
}

func TestMetricsGate_BaselineHealthPrecondition(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		if requests == 1 {
			_, _ = w.Write(prometheusVectorResponse("0"))
			return
		}
		_, _ = w.Write(prometheusVectorResponse("1"))
	}))
	defer srv.Close()

	threshold := 1.1
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{
				Provider:  "prometheus",
				Query:     "canary_error_rate",
				Endpoint:  srv.URL,
				Threshold: &threshold,
				Analysis: &kaprov1alpha1.MetricAnalysisSpec{
					Mode:                "baseline",
					BaselineQuery:       "baseline_error_rate",
					BaselineHealthQuery: "baseline_healthy",
				},
			}},
		},
	}

	result, err := (&gate.MetricsGate{HTTPClient: srv.Client()}).Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Fatal("expected unhealthy baseline to block")
	}
	if result.Evidence[0].BaselineHealthy == nil || *result.Evidence[0].BaselineHealthy {
		t.Fatalf("expected baselineHealthy=false evidence, got %#v", result.Evidence[0].BaselineHealthy)
	}
}

func TestMetricsGate_TemplateErrorFails(t *testing.T) {
	g := &gate.MetricsGate{}
	policy := &kaprov1alpha1.GatePolicySpec{
		Gate: kaprov1alpha1.GateSpec{
			Metrics: []kaprov1alpha1.MetricGate{{Provider: "prometheus", Query: "{{"}},
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseFailed {
		t.Fatalf("expected template error to fail, got %q", result.Phase)
	}
}

// ---- ApprovalGate -----------------------------------------------------------

func approvalScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func TestApprovalGate_NilClient_ReturnsError(t *testing.T) {
	g := &gate.ApprovalGate{Client: nil}
	_, err := g.Evaluate(context.Background(), gate.Request{
		Context: &gate.Context{},
	})
	if err == nil {
		t.Error("expected error when Client is nil")
	}
}

func TestApprovalGate_NoApproval_Pending(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).Build()
	g := &gate.ApprovalGate{Client: fakeClient}

	promo := &gate.Context{PromotionRunRef: "rel-1", Target: "target-staging"}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false when no Approval exists")
	}
	if result.RetryAfter == "" {
		t.Error("expected RetryAfter to be set while waiting for approval")
	}
}

func TestApprovalGate_MatchingApproval_Passes(t *testing.T) {
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: gate.ApprovalName("rel-1", "target-staging"),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-staging",
			ApprovedBy:   "alice",
			Comment:      "LGTM",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &gate.Context{PromotionRunRef: "rel-1", Target: "target-staging", Namespace: "default"}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true, got message: %s", result.Message)
	}
}

func TestApprovalGate_BypassApproval_Passes(t *testing.T) {
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: gate.ApprovalName("rel-1", "target-staging"),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-staging",
			ApprovedBy:   "bob",
			Bypass:       true,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &gate.Context{PromotionRunRef: "rel-1", Target: "target-staging", Namespace: "default"}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true for bypass approval, got: %s", result.Message)
	}
}

func TestApprovalGate_WrongName_Pending(t *testing.T) {
	// Approval exists but its name doesn't match ApprovalName(promotionrun, target) —
	// the gate must not unblock on label matches.
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: "some-other-name",
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-staging",
			ApprovedBy:   "carol",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &gate.Context{PromotionRunRef: "rel-1", Target: "target-staging", Namespace: "default"}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false when Approval name does not match ApprovalName")
	}
}

func TestApprovalGate_ContextNameScopesApproval(t *testing.T) {
	ref := "rel-1-promotionplan-stage-target-staging"
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: gate.ApprovalName("rel-1", ref),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-staging",
			Ref:          ref,
			ApprovedBy:   "dana",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &gate.Context{
		Name:            ref,
		PromotionRunRef: "rel-1",
		Target:          "target-staging",
		Namespace:       "default",
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Context: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Fatalf("expected scoped approval to pass, got %q", result.Message)
	}
}

func TestApprovalGate_ApproverAllowlistEnforced(t *testing.T) {
	ref := "rel-1-promotionplan-stage-target-staging"
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: gate.ApprovalName("rel-1", ref),
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-staging",
			Ref:          ref,
			ApprovedBy:   "mallory",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &gate.Context{
		Name:            ref,
		PromotionRunRef: "rel-1",
		Target:          "target-staging",
		Namespace:       "default",
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Context: promo,
		Policy: &kaprov1alpha1.GatePolicySpec{
			Approval: &kaprov1alpha1.ApprovalConfig{Approvers: []string{"alice", "bob"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Phase != kaprov1alpha1.GatePhaseFailed {
		t.Fatalf("expected failed result for disallowed approver, got %q", result.Phase)
	}
}

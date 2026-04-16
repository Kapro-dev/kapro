package gate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/gate"
)

// ---- SoakGate ---------------------------------------------------------------

func TestSoakGate_NoPolicy(t *testing.T) {
	g := &gate.SoakGate{}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion: &kaprov1alpha1.Promotion{},
		Policy:    nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected Passed=true when policy is nil")
	}
}

func TestSoakGate_NoSoakTime(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: ""},
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion: &kaprov1alpha1.Promotion{},
		Policy:    policy,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected Passed=true when soakTime is empty")
	}
}

func TestSoakGate_ClockNotStarted(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "5m"},
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion: &kaprov1alpha1.Promotion{},
		Policy:    policy,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected Passed=false when StartedAt is empty")
	}
}

func TestSoakGate_Elapsed(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "1ms"},
		},
	}
	time.Sleep(5 * time.Millisecond) // ensure soak elapsed
	promo := &kaprov1alpha1.Promotion{
		Status: kaprov1alpha1.PromotionStatus{
			StartedAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true after soak elapsed, got message: %s", result.Message)
	}
}

func TestSoakGate_NotElapsed(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "1h"},
		},
	}
	promo := &kaprov1alpha1.Promotion{
		Status: kaprov1alpha1.PromotionStatus{
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected Passed=false when soak has not elapsed")
	}
	if result.RetryAfter == "" {
		t.Error("expected non-empty RetryAfter when soak has not elapsed")
	}
}

func TestSoakGate_InvalidDuration(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "not-a-duration"},
		},
	}
	_, err := g.Evaluate(context.Background(), gate.Request{
		Promotion: &kaprov1alpha1.Promotion{},
		Policy:    policy,
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

func TestMetricsGate_NoMetrics(t *testing.T) {
	g := &gate.MetricsGate{}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion:   &kaprov1alpha1.Promotion{},
		Policy:      &kaprov1alpha1.PromotionPolicy{},
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected Passed=true when no metrics configured")
	}
}

func TestMetricsGate_Passed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prometheusVectorResponse("1"))
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kapro.io/prometheus-url": srv.URL,
			},
		},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "prometheus", Query: "up", Window: "5m"},
				},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion:   &kaprov1alpha1.Promotion{},
		Policy:      policy,
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true, got: %s", result.Message)
	}
}

func TestMetricsGate_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kapro.io/prometheus-url": srv.URL,
			},
		},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "prometheus", Query: "up == 0", Window: "5m"},
				},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion:   &kaprov1alpha1.Promotion{},
		Policy:      policy,
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected Passed=false for empty vector response")
	}
}

func TestMetricsGate_PrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.PromotionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"kapro.io/prometheus-url": srv.URL},
		},
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{Provider: "prometheus", Query: "up"}},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Promotion: &kaprov1alpha1.Promotion{}, Policy: policy, MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Error is non-fatal — gate should block with retry, not return an error.
	if result.Passed {
		t.Error("expected Passed=false on prometheus error")
	}
	if result.RetryAfter == "" {
		t.Error("expected RetryAfter on prometheus error")
	}
}

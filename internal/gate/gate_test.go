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
		Sync: &kaprov1alpha1.Sync{},
		Policy:    nil,
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
	policy := &kaprov1alpha1.GatePolicy{
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: ""},
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Sync: &kaprov1alpha1.Sync{},
		Policy:    policy,
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
	policy := &kaprov1alpha1.GatePolicy{
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "5m"},
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Sync: &kaprov1alpha1.Sync{},
		Policy:    policy,
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
	policy := &kaprov1alpha1.GatePolicy{
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "1ms"},
		},
	}
	time.Sleep(5 * time.Millisecond) // ensure soak elapsed
	promo := &kaprov1alpha1.Sync{
		Status: kaprov1alpha1.SyncStatus{
			StartedAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo, Policy: policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true after soak elapsed, got message: %s", result.Message)
	}
}

func TestSoakGate_NotElapsed(t *testing.T) {
	g := &gate.SoakGate{}
	policy := &kaprov1alpha1.GatePolicy{
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "1h"},
		},
	}
	promo := &kaprov1alpha1.Sync{
		Status: kaprov1alpha1.SyncStatus{
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo, Policy: policy})
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
	policy := &kaprov1alpha1.GatePolicy{
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{SoakTime: "not-a-duration"},
		},
	}
	_, err := g.Evaluate(context.Background(), gate.Request{
		Sync: &kaprov1alpha1.Sync{},
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
		Sync:       &kaprov1alpha1.Sync{},
		Policy:      &kaprov1alpha1.GatePolicy{},
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

	policy := &kaprov1alpha1.GatePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kapro.io/prometheus-url": srv.URL,
			},
		},
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "prometheus", Query: "up", Window: "5m"},
				},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Sync:       &kaprov1alpha1.Sync{},
		Policy:      policy,
		MetricIndex: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true, got: %s", result.Message)
	}
}

func TestMetricsGate_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(prometheusEmptyVectorResponse())
	}))
	defer srv.Close()

	policy := &kaprov1alpha1.GatePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kapro.io/prometheus-url": srv.URL,
			},
		},
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "prometheus", Query: "up == 0", Window: "5m"},
				},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Sync:       &kaprov1alpha1.Sync{},
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

	policy := &kaprov1alpha1.GatePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"kapro.io/prometheus-url": srv.URL},
		},
		Spec: kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{Provider: "prometheus", Query: "up"}},
			},
		},
	}

	g := &gate.MetricsGate{HTTPClient: srv.Client()}
	result, err := g.Evaluate(context.Background(), gate.Request{
		Sync: &kaprov1alpha1.Sync{}, Policy: policy, MetricIndex: 0,
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
		Sync: &kaprov1alpha1.Sync{},
	})
	if err == nil {
		t.Error("expected error when Client is nil")
	}
}

func TestApprovalGate_NoApproval_Pending(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).Build()
	g := &gate.ApprovalGate{Client: fakeClient}

	promo := &kaprov1alpha1.Sync{
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-staging",
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo})
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
			Name:      "approve-rel1-staging",
			Namespace: "default",
			Labels: map[string]string{
				"kapro.io/release":     "rel-1",
				"kapro.io/environment": "env-staging",
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:           kaprov1alpha1.ApprovalKindSync,
			Release:        "rel-1",
			EnvironmentRef: "env-staging",
			ApprovedBy:     "alice",
			Comment:        "LGTM",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &kaprov1alpha1.Sync{
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-staging",
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo})
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
			Name:      "bypass-rel1-staging",
			Namespace: "default",
			Labels: map[string]string{
				"kapro.io/release":     "rel-1",
				"kapro.io/environment": "env-staging",
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:           kaprov1alpha1.ApprovalKindSync,
			Release:        "rel-1",
			EnvironmentRef: "env-staging",
			ApprovedBy:     "bob",
			Bypass:         true,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &kaprov1alpha1.Sync{
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-staging",
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsPassed() {
		t.Errorf("expected Passed=true for bypass approval, got: %s", result.Message)
	}
}

func TestApprovalGate_WrongKind_Pending(t *testing.T) {
	// Approval exists but is for a Stage, not a Promotion — should not unblock.
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "batch-approval",
			Namespace: "default",
			Labels: map[string]string{
				"kapro.io/release":     "rel-1",
				"kapro.io/environment": "env-staging",
			},
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:           kaprov1alpha1.ApprovalKindStage,
			Release:        "rel-1",
			EnvironmentRef: "env-staging",
			ApprovedBy:     "carol",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(approvalScheme(t)).WithObjects(approval).Build()
	g := &gate.ApprovalGate{Client: fakeClient}
	promo := &kaprov1alpha1.Sync{
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-staging",
		},
	}
	result, err := g.Evaluate(context.Background(), gate.Request{Sync: promo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsPassed() {
		t.Error("expected Passed=false for approval with wrong Kind")
	}
}

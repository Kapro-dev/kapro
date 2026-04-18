package shadow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/gate"
)

func makePolicy(cfgJSON string) *kaprov1alpha1.PromotionPolicy {
	return &kaprov1alpha1.PromotionPolicy{
		Spec: kaprov1alpha1.PromotionPolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{
					{Provider: "shadow", Config: []byte(cfgJSON)},
				},
			},
		},
	}
}

func makeRequest(server *httptest.Server) gate.Request {
	cfg, _ := json.Marshal(Config{
		Endpoint:            server.URL,
		MinSampleSize:       100,
		DivergenceThreshold: 0.02,
		ErrorRateThreshold:  0.01,
	})
	return gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{
						{Provider: "shadow", Config: cfg},
					},
				},
			},
		},
	}
}

func serveStats(t *testing.T, stats ShadowStats) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stats" && r.URL.Path != "/summary.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	}))
}

func TestGate_Passes_WhenDivergenceAndErrorRateBelowThreshold(t *testing.T) {
	srv := serveStats(t, ShadowStats{
		TotalRequests:     1000,
		DivergentRequests: 10, // 1% divergence
		ErrorRequests:     5,  // 0.5% error
	})
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeRequest(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true, got false: %s", result.Message)
	}
}

func TestGate_Blocks_WhenSampleTooSmall(t *testing.T) {
	srv := serveStats(t, ShadowStats{
		TotalRequests:     10, // below MinSampleSize=100
		DivergentRequests: 0,
		ErrorRequests:     0,
	})
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeRequest(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected Passed=false when sample too small")
	}
	if result.RetryAfter == "" {
		t.Error("expected RetryAfter hint when waiting for sample")
	}
}

func TestGate_Blocks_WhenDivergenceTooHigh(t *testing.T) {
	srv := serveStats(t, ShadowStats{
		TotalRequests:     1000,
		DivergentRequests: 50, // 5% — above 2% threshold
		ErrorRequests:     0,
	})
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeRequest(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on high divergence, got true: %s", result.Message)
	}
}

func TestGate_Blocks_WhenErrorRateTooHigh(t *testing.T) {
	srv := serveStats(t, ShadowStats{
		TotalRequests:     1000,
		DivergentRequests: 5,  // 0.5% — fine
		ErrorRequests:     50, // 5% — above 1% threshold
	})
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeRequest(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Errorf("expected Passed=false on high error rate, got true: %s", result.Message)
	}
}

func TestGate_UsesPrecomputedRates(t *testing.T) {
	// Service returns pre-computed rates, no raw counts.
	srv := serveStats(t, ShadowStats{
		TotalRequests:  1000,
		DivergenceRate: 0.01, // 1%
		ErrorRate:      0.005, // 0.5%
	})
	defer srv.Close()

	g := &Gate{}
	result, err := g.Evaluate(context.Background(), makeRequest(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true with pre-computed rates, got: %s", result.Message)
	}
}

func TestGate_FallsBackToSummaryJSON(t *testing.T) {
	// /v1/stats returns 404; /summary.json should be tried.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/summary.json" {
			stats := ShadowStats{TotalRequests: 500, DivergentRequests: 5, ErrorRequests: 2}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stats)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg, _ := json.Marshal(Config{
		Endpoint:      srv.URL,
		MinSampleSize: 100,
	})
	req := gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{
						{Provider: "shadow", Config: cfg},
					},
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
		t.Errorf("expected Passed=true via /summary.json fallback, got: %s", result.Message)
	}
}

func TestGate_NoPolicy_Passes(t *testing.T) {
	g := &Gate{}
	result, err := g.Evaluate(context.Background(), gate.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected Passed=true with nil policy")
	}
}

func TestGate_MissingEndpoint_Errors(t *testing.T) {
	req := gate.Request{
		MetricIndex: 0,
		Policy: &kaprov1alpha1.PromotionPolicy{
			Spec: kaprov1alpha1.PromotionPolicySpec{
				Gate: kaprov1alpha1.GateSpec{
					Metrics: []kaprov1alpha1.MetricGate{
						{Provider: "shadow"}, // no endpoint, no config
					},
				},
			},
		},
	}
	g := &Gate{}
	_, err := g.Evaluate(context.Background(), req)
	if err == nil {
		t.Error("expected error for missing endpoint")
	}
}

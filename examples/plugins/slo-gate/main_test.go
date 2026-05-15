package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	gateconformance "kapro.io/kapro/conformance/gate"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestKGIConformance(t *testing.T) {
	client := newTestClient(t, &sloGateServer{})
	scenario := gateconformance.DefaultScenario()
	scenario.Evaluate.Parameters = map[string]string{
		"provider":  "static",
		"metric":    "error_rate",
		"value":     "0.01",
		"threshold": "0.05",
		"operator":  "lte",
	}
	gateconformance.Run(t, client, scenario)
}

func TestEvaluateStaticThreshold(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		threshold string
		operator  string
		want      kgiv1alpha1.GatePhase
	}{
		{name: "lte passes", value: "0.01", threshold: "0.05", operator: "lte", want: kgiv1alpha1.GatePhase_GATE_PHASE_PASSED},
		{name: "lte fails", value: "0.08", threshold: "0.05", operator: "lte", want: kgiv1alpha1.GatePhase_GATE_PHASE_FAILED},
		{name: "gte passes", value: "99.9", threshold: "99.5", operator: "gte", want: kgiv1alpha1.GatePhase_GATE_PHASE_PASSED},
		{name: "gt fails", value: "10", threshold: "10", operator: "gt", want: kgiv1alpha1.GatePhase_GATE_PHASE_FAILED},
		{name: "unsupported operator", value: "10", threshold: "10", operator: "near", want: kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := (&sloGateServer{}).Evaluate(context.Background(), &kgiv1alpha1.EvaluateRequest{
				Gate: "slo",
				Parameters: map[string]string{
					"provider":  "static",
					"metric":    "availability",
					"value":     tt.value,
					"threshold": tt.threshold,
					"operator":  tt.operator,
				},
			})
			if err != nil {
				t.Fatalf("Evaluate returned error: %v", err)
			}
			if resp.GetPhase() != tt.want {
				t.Fatalf("phase=%s, want %s, message=%q", resp.GetPhase(), tt.want, resp.GetMessage())
			}
		})
	}
}

func TestEvaluateInvalidConfigIsInconclusive(t *testing.T) {
	tests := []struct {
		name       string
		parameters map[string]string
	}{
		{name: "missing threshold", parameters: map[string]string{"provider": "static", "value": "1"}},
		{name: "missing value", parameters: map[string]string{"provider": "static", "threshold": "1"}},
		{name: "unknown provider", parameters: map[string]string{"provider": "other", "threshold": "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := (&sloGateServer{}).Evaluate(context.Background(), &kgiv1alpha1.EvaluateRequest{Parameters: tt.parameters})
			if err != nil {
				t.Fatalf("Evaluate returned error: %v", err)
			}
			if resp.GetPhase() != kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE {
				t.Fatalf("phase=%s, want INCONCLUSIVE, message=%q", resp.GetPhase(), resp.GetMessage())
			}
		})
	}
}

func TestEvaluatePrometheusThreshold(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != `sum(rate(http_requests_total{status=~"5.."}[5m]))` {
			t.Fatalf("query=%q", got)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1710000000,"0.02"]}]}}`)
	}))
	defer prom.Close()

	resp, err := (&sloGateServer{}).Evaluate(context.Background(), &kgiv1alpha1.EvaluateRequest{
		Gate: "error-rate",
		Parameters: map[string]string{
			"provider":      "prometheus",
			"prometheusURL": prom.URL,
			"query":         `sum(rate(http_requests_total{status=~"5.."}[5m]))`,
			"threshold":     "0.05",
			"operator":      "lte",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if resp.GetPhase() != kgiv1alpha1.GatePhase_GATE_PHASE_PASSED {
		t.Fatalf("phase=%s, want PASSED, message=%q", resp.GetPhase(), resp.GetMessage())
	}
}

func TestEvaluatePrometheusEmptyResultIsRunning(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer prom.Close()

	resp, err := (&sloGateServer{}).Evaluate(context.Background(), &kgiv1alpha1.EvaluateRequest{
		Parameters: map[string]string{
			"provider":      "prometheus",
			"prometheusURL": prom.URL,
			"query":         "up",
			"threshold":     "1",
			"operator":      "gte",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if resp.GetPhase() != kgiv1alpha1.GatePhase_GATE_PHASE_RUNNING {
		t.Fatalf("phase=%s, want RUNNING, message=%q", resp.GetPhase(), resp.GetMessage())
	}
}

func newTestClient(t *testing.T, server *sloGateServer) kgiv1alpha1.GateServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	kgiv1alpha1.RegisterGateServiceServer(grpcServer, server)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Errorf("grpc server stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return kgiv1alpha1.NewGateServiceClient(conn)
}

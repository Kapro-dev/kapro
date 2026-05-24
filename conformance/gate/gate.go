// Package gate provides conformance checks for external KGI plugins.
package gate

import (
	"context"
	"testing"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/conformance"
	"kapro.io/kapro/pkg/plugincompat"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/protobuf/proto"
)

// Scenario contains the request used by the gate conformance harness.
type Scenario struct {
	Evaluate *kgiv1alpha1.EvaluateRequest
	Timeout  time.Duration
}

// DefaultScenario returns a minimal deterministic gate test scenario.
func DefaultScenario() Scenario {
	return Scenario{
		Evaluate: &kgiv1alpha1.EvaluateRequest{
			PromotionRun:  "conformance-promotionrun",
			Target:        "conformance-target",
			PromotionPlan: "main",
			Stage:         "canary",
			Version:       "oci://example.com/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Gate:          "conformance",
			Parameters: map[string]string{
				"conformance": "true",
			},
		},
		Timeout: 10 * time.Second,
	}
}

// Run executes the base KGI conformance checks against a gRPC gate client.
func Run(t *testing.T, client kgiv1alpha1.GateServiceClient, scenario Scenario) {
	t.Helper()
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), scenario.Timeout)
	defer cancel()

	t.Run("GetCapabilitiesReturnsSupportedContractVersion", func(t *testing.T) {
		resp, err := client.GetCapabilities(ctx, &kgiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			t.Fatalf("GetCapabilities returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("GetCapabilities returned nil response")
		}
		if !plugincompat.IsContractVersionSupported(kaprov1alpha1.PluginTypeGate, resp.GetContractVersion()) {
			t.Fatalf("contract_version = %q, supported versions = %v", resp.GetContractVersion(), plugincompat.SupportedKGIContractVersions())
		}
	})

	t.Run("EvaluateReturnsValidPhaseAndDoesNotMutateRequest", func(t *testing.T) {
		req := cloneEvaluate(scenario.Evaluate)
		before := cloneEvaluate(req)
		resp, err := client.Evaluate(ctx, req)
		if err != nil {
			t.Fatalf("Evaluate returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("Evaluate returned nil response")
		}
		if !isValidPhase(resp.GetPhase()) {
			t.Fatalf("Evaluate returned invalid phase: %s", resp.GetPhase())
		}
		if !proto.Equal(req, before) {
			t.Fatalf("Evaluate mutated request: before=%v after=%v", before, req)
		}
	})
}

// Check executes the base KGI conformance checks against a gRPC gate client and
// returns structured results for CLIs and custom test runners.
func Check(ctx context.Context, client kgiv1alpha1.GateServiceClient, scenario Scenario) conformance.Report {
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), scenario.Timeout)
		defer cancel()
	}
	return conformance.Report{
		Suite: "KGI gate",
		Results: []conformance.Result{
			checkCapabilities(ctx, client),
			checkEvaluateReturnsValidPhaseAndDoesNotMutateRequest(ctx, client, scenario),
		},
	}
}

func checkCapabilities(ctx context.Context, client kgiv1alpha1.GateServiceClient) conformance.Result {
	const name = "GetCapabilitiesReturnsSupportedContractVersion"
	resp, err := client.GetCapabilities(ctx, &kgiv1alpha1.GetCapabilitiesRequest{})
	if err != nil {
		return conformance.Fail(name, "GetCapabilities returned error: %v", err)
	}
	if resp == nil {
		return conformance.Fail(name, "GetCapabilities returned nil response")
	}
	if !plugincompat.IsContractVersionSupported(kaprov1alpha1.PluginTypeGate, resp.GetContractVersion()) {
		return conformance.Fail(name, "contract_version = %q, supported versions = %v", resp.GetContractVersion(), plugincompat.SupportedKGIContractVersions())
	}
	return conformance.Pass(name)
}

func checkEvaluateReturnsValidPhaseAndDoesNotMutateRequest(ctx context.Context, client kgiv1alpha1.GateServiceClient, scenario Scenario) conformance.Result {
	const name = "EvaluateReturnsValidPhaseAndDoesNotMutateRequest"
	req := cloneEvaluate(scenario.Evaluate)
	before := cloneEvaluate(req)
	resp, err := client.Evaluate(ctx, req)
	if err != nil {
		return conformance.Fail(name, "Evaluate returned error: %v", err)
	}
	if resp == nil {
		return conformance.Fail(name, "Evaluate returned nil response")
	}
	if !isValidPhase(resp.GetPhase()) {
		return conformance.Fail(name, "Evaluate returned invalid phase: %s", resp.GetPhase())
	}
	if !proto.Equal(req, before) {
		return conformance.Fail(name, "Evaluate mutated request: before=%v after=%v", before, req)
	}
	return conformance.Pass(name)
}

func isValidPhase(phase kgiv1alpha1.GatePhase) bool {
	switch phase {
	case kgiv1alpha1.GatePhase_GATE_PHASE_PASSED,
		kgiv1alpha1.GatePhase_GATE_PHASE_FAILED,
		kgiv1alpha1.GatePhase_GATE_PHASE_RUNNING,
		kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE:
		return true
	default:
		return false
	}
}

func cloneEvaluate(msg *kgiv1alpha1.EvaluateRequest) *kgiv1alpha1.EvaluateRequest {
	if msg == nil {
		return nil
	}
	return proto.Clone(msg).(*kgiv1alpha1.EvaluateRequest)
}

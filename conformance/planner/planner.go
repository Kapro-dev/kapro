// Package planner provides conformance checks for external KPI plugins.
package planner

import (
	"context"
	"testing"
	"time"

	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/protobuf/proto"
)

const contractVersion = "v1alpha1"

// Scenario contains the request used by the planner conformance harness.
type Scenario struct {
	Plan    *kpiv1alpha1.PlanRequest
	Timeout time.Duration
}

// DefaultScenario returns a minimal deterministic planner test scenario.
func DefaultScenario() Scenario {
	return Scenario{
		Plan: &kpiv1alpha1.PlanRequest{
			Release:  "conformance-release",
			Pipeline: "main",
			Stage:    "canary",
			Version:  "oci://example.com/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Strategy: &kpiv1alpha1.StageStrategy{
				MaxParallel:    2,
				MaxUnavailable: 1,
			},
			Targets: []*kpiv1alpha1.Target{
				{
					Name:  "alpha",
					Ready: true,
					Labels: map[string]string{
						"zone": "a",
					},
				},
				{
					Name:  "beta",
					Ready: true,
					Labels: map[string]string{
						"zone": "b",
					},
				},
				{
					Name:  "gamma",
					Ready: false,
					Labels: map[string]string{
						"zone": "c",
					},
				},
			},
			Parameters: map[string]string{
				"conformance": "true",
			},
		},
		Timeout: 10 * time.Second,
	}
}

// Run executes the base KPI conformance checks against a gRPC planner client.
func Run(t *testing.T, client kpiv1alpha1.PlannerServiceClient, scenario Scenario) {
	t.Helper()
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), scenario.Timeout)
	defer cancel()

	t.Run("GetCapabilitiesReturnsContractVersion", func(t *testing.T) {
		resp, err := client.GetCapabilities(ctx, &kpiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			t.Fatalf("GetCapabilities returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("GetCapabilities returned nil response")
		}
		if resp.GetContractVersion() != contractVersion {
			t.Fatalf("contract_version = %q, want %q", resp.GetContractVersion(), contractVersion)
		}
		if !hasPlannerCapability(resp.GetCapabilities()) {
			t.Fatalf("capabilities = %v, want at least one of filter, score, order, defer", resp.GetCapabilities())
		}
	})

	t.Run("PlanHandlesEmptyTargetList", func(t *testing.T) {
		req := clonePlan(scenario.Plan)
		if req == nil {
			t.Fatal("scenario Plan request is nil")
		}
		req.Targets = nil

		resp, err := client.Plan(ctx, req)
		if err != nil {
			t.Fatalf("Plan with empty target list returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("Plan with empty target list returned nil response")
		}
		if len(resp.GetTargets()) != 0 {
			t.Fatalf("Plan with empty target list returned targets: %v", resp.GetTargets())
		}
	})

	t.Run("PlanIsDeterministicForSameRequest", func(t *testing.T) {
		first, err := client.Plan(ctx, clonePlan(scenario.Plan))
		if err != nil {
			t.Fatalf("first Plan returned error: %v", err)
		}
		second, err := client.Plan(ctx, clonePlan(scenario.Plan))
		if err != nil {
			t.Fatalf("second Plan returned error: %v", err)
		}
		if !proto.Equal(first, second) {
			t.Fatalf("Plan is not deterministic: first=%v second=%v", first, second)
		}
	})

	t.Run("PlanDoesNotMutateRequest", func(t *testing.T) {
		req := clonePlan(scenario.Plan)
		before := clonePlan(req)
		if _, err := client.Plan(ctx, req); err != nil {
			t.Fatalf("Plan returned error: %v", err)
		}
		if !proto.Equal(req, before) {
			t.Fatalf("Plan mutated request: before=%v after=%v", before, req)
		}
	})

	t.Run("PlanReturnsValidTargetsAndDecisions", func(t *testing.T) {
		req := clonePlan(scenario.Plan)
		resp, err := client.Plan(ctx, req)
		if err != nil {
			t.Fatalf("Plan returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("Plan returned nil response")
		}
		validatePlannedTargets(t, req, resp)
	})

	t.Run("PlanRespectsContextCancellation", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.Plan(cancelledCtx, clonePlan(scenario.Plan)); err == nil {
			t.Fatal("Plan with cancelled context returned nil error")
		}
	})
}

func validatePlannedTargets(t *testing.T, req *kpiv1alpha1.PlanRequest, resp *kpiv1alpha1.PlanResponse) {
	t.Helper()

	requestTargets := make(map[string]struct{}, len(req.GetTargets()))
	for _, target := range req.GetTargets() {
		requestTargets[target.GetName()] = struct{}{}
	}

	seen := make(map[string]struct{}, len(resp.GetTargets()))
	for _, target := range resp.GetTargets() {
		name := target.GetName()
		if _, ok := requestTargets[name]; !ok {
			t.Fatalf("Plan returned target %q not present in request", name)
		}
		if _, ok := seen[name]; ok {
			t.Fatalf("Plan returned duplicate target %q", name)
		}
		seen[name] = struct{}{}
		if !isValidDecision(target.GetDecision()) {
			t.Fatalf("Plan returned invalid decision for target %q: %s", name, target.GetDecision())
		}
	}
}

func isValidDecision(decision kpiv1alpha1.PlanningDecision) bool {
	switch decision {
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE,
		kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP,
		kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER:
		return true
	default:
		return false
	}
}

func hasPlannerCapability(capabilities []string) bool {
	for _, capability := range capabilities {
		switch capability {
		case "filter", "score", "order", "defer":
			return true
		}
	}
	return false
}

func clonePlan(msg *kpiv1alpha1.PlanRequest) *kpiv1alpha1.PlanRequest {
	if msg == nil {
		return nil
	}
	return proto.Clone(msg).(*kpiv1alpha1.PlanRequest)
}

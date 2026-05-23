// Package actuator provides conformance checks for external KAI plugins.
package actuator

import (
	"context"
	"testing"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/conformance"
	"kapro.io/kapro/pkg/plugincompat"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/protobuf/proto"
)

// Scenario contains the requests used by the actuator conformance harness.
type Scenario struct {
	Apply                *kaiv1alpha1.ApplyRequest
	IsConverged          *kaiv1alpha1.IsConvergedRequest
	Rollback             *kaiv1alpha1.RollbackRequest
	RequiredCapabilities []string
	Timeout              time.Duration
}

// DefaultScenario returns a minimal deterministic actuator test scenario.
func DefaultScenario() Scenario {
	return Scenario{
		Apply: &kaiv1alpha1.ApplyRequest{
			PromotionRun:    "conformance-promotionrun",
			Target:          "conformance-target",
			PromotionPlan:   "main",
			Stage:           "canary",
			Version:         "oci://example.com/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			PreviousVersion: "oci://example.com/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Parameters: map[string]string{
				"conformance": "true",
			},
		},
		IsConverged: &kaiv1alpha1.IsConvergedRequest{
			PromotionRun: "conformance-promotionrun",
			Target:       "conformance-target",
			Version:      "oci://example.com/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Parameters: map[string]string{
				"conformance": "true",
			},
		},
		Rollback: &kaiv1alpha1.RollbackRequest{
			PromotionRun:    "conformance-promotionrun",
			Target:          "conformance-target",
			Version:         "oci://example.com/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			PreviousVersion: "oci://example.com/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Parameters: map[string]string{
				"conformance": "true",
			},
		},
		RequiredCapabilities: []string{
			kaiv1alpha1.CapabilityApply,
			kaiv1alpha1.CapabilityConvergence,
			kaiv1alpha1.CapabilityRollback,
		},
		Timeout: 10 * time.Second,
	}
}

// Run executes the base KAI conformance checks against a gRPC actuator client.
func Run(t *testing.T, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) {
	t.Helper()
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), scenario.Timeout)
	defer cancel()

	t.Run("GetCapabilitiesReturnsSupportedContractVersion", func(t *testing.T) {
		resp, err := client.GetCapabilities(ctx, &kaiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			t.Fatalf("GetCapabilities returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("GetCapabilities returned nil response")
		}
		if !plugincompat.IsContractVersionSupported(kaprov1alpha2.PluginTypeActuator, resp.GetContractVersion()) {
			t.Fatalf("contract_version = %q, supported versions = %v", resp.GetContractVersion(), plugincompat.SupportedKAIContractVersions())
		}
	})

	t.Run("GetCapabilitiesReportsRequiredCapabilities", func(t *testing.T) {
		resp, err := client.GetCapabilities(ctx, &kaiv1alpha1.GetCapabilitiesRequest{})
		if err != nil {
			t.Fatalf("GetCapabilities returned error: %v", err)
		}
		if resp == nil {
			t.Fatal("GetCapabilities returned nil response")
		}
		if missing := missingCapabilities(resp.GetCapabilities(), requiredCapabilities(scenario)); len(missing) > 0 {
			t.Fatalf("missing required capabilities %v from %v", missing, resp.GetCapabilities())
		}
	})

	t.Run("ApplyIsIdempotent", func(t *testing.T) {
		first, err := client.Apply(ctx, cloneApply(scenario.Apply))
		if err != nil {
			t.Fatalf("first Apply returned error: %v", err)
		}
		if first == nil || !first.GetAccepted() {
			t.Fatalf("first Apply accepted = false, response=%v", first)
		}
		second, err := client.Apply(ctx, cloneApply(scenario.Apply))
		if err != nil {
			t.Fatalf("second Apply returned error: %v", err)
		}
		if second == nil || !second.GetAccepted() {
			t.Fatalf("second Apply accepted = false, response=%v", second)
		}
	})

	t.Run("IsConvergedIsDeterministic", func(t *testing.T) {
		first, err := client.IsConverged(ctx, cloneIsConverged(scenario.IsConverged))
		if err != nil {
			t.Fatalf("first IsConverged returned error: %v", err)
		}
		second, err := client.IsConverged(ctx, cloneIsConverged(scenario.IsConverged))
		if err != nil {
			t.Fatalf("second IsConverged returned error: %v", err)
		}
		if !proto.Equal(first, second) {
			t.Fatalf("IsConverged is not deterministic: first=%v second=%v", first, second)
		}
	})

	t.Run("RollbackIsIdempotent", func(t *testing.T) {
		first, err := client.Rollback(ctx, cloneRollback(scenario.Rollback))
		if err != nil {
			t.Fatalf("first Rollback returned error: %v", err)
		}
		if first == nil || !first.GetAccepted() {
			t.Fatalf("first Rollback accepted = false, response=%v", first)
		}
		second, err := client.Rollback(ctx, cloneRollback(scenario.Rollback))
		if err != nil {
			t.Fatalf("second Rollback returned error: %v", err)
		}
		if second == nil || !second.GetAccepted() {
			t.Fatalf("second Rollback accepted = false, response=%v", second)
		}
	})

	t.Run("ApplyRespectsContextCancellation", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.Apply(cancelledCtx, cloneApply(scenario.Apply)); err == nil {
			t.Fatal("Apply with cancelled context returned nil error")
		}
	})
}

// Check executes the base KAI conformance checks against a gRPC actuator
// client and returns structured results for CLIs and custom test runners.
func Check(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Report {
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), scenario.Timeout)
		defer cancel()
	}
	return conformance.Report{
		Suite: "KAI actuator",
		Results: []conformance.Result{
			checkCapabilitiesContractVersion(ctx, client),
			checkRequiredCapabilities(ctx, client, scenario),
			checkApplyIsIdempotent(ctx, client, scenario),
			checkIsConvergedIsDeterministic(ctx, client, scenario),
			checkRollbackIsIdempotent(ctx, client, scenario),
			checkApplyRespectsContextCancellation(client, scenario),
		},
	}
}

func checkCapabilitiesContractVersion(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient) conformance.Result {
	const name = "GetCapabilitiesReturnsSupportedContractVersion"
	resp, err := client.GetCapabilities(ctx, &kaiv1alpha1.GetCapabilitiesRequest{})
	if err != nil {
		return conformance.Fail(name, "GetCapabilities returned error: %v", err)
	}
	if resp == nil {
		return conformance.Fail(name, "GetCapabilities returned nil response")
	}
	if !plugincompat.IsContractVersionSupported(kaprov1alpha2.PluginTypeActuator, resp.GetContractVersion()) {
		return conformance.Fail(name, "contract_version = %q, supported versions = %v", resp.GetContractVersion(), plugincompat.SupportedKAIContractVersions())
	}
	return conformance.Pass(name)
}

func checkRequiredCapabilities(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Result {
	const name = "GetCapabilitiesReportsRequiredCapabilities"
	resp, err := client.GetCapabilities(ctx, &kaiv1alpha1.GetCapabilitiesRequest{})
	if err != nil {
		return conformance.Fail(name, "GetCapabilities returned error: %v", err)
	}
	if resp == nil {
		return conformance.Fail(name, "GetCapabilities returned nil response")
	}
	if missing := missingCapabilities(resp.GetCapabilities(), requiredCapabilities(scenario)); len(missing) > 0 {
		return conformance.Fail(name, "missing required capabilities %v from %v", missing, resp.GetCapabilities())
	}
	return conformance.Pass(name)
}

func requiredCapabilities(scenario Scenario) []string {
	if len(scenario.RequiredCapabilities) > 0 {
		return append([]string(nil), scenario.RequiredCapabilities...)
	}
	return []string{
		kaiv1alpha1.CapabilityApply,
		kaiv1alpha1.CapabilityConvergence,
		kaiv1alpha1.CapabilityRollback,
	}
}

func missingCapabilities(actual, required []string) []string {
	missing := make([]string, 0)
	for _, capability := range required {
		if capability == kaiv1alpha1.CapabilityConvergence {
			if kaiv1alpha1.HasAnyCapability(actual, kaiv1alpha1.CapabilityConvergence, kaiv1alpha1.CapabilityObserve) {
				continue
			}
			missing = append(missing, capability)
			continue
		}
		if !kaiv1alpha1.HasCapability(actual, capability) {
			missing = append(missing, capability)
		}
	}
	return missing
}

func checkApplyIsIdempotent(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Result {
	const name = "ApplyIsIdempotent"
	first, err := client.Apply(ctx, cloneApply(scenario.Apply))
	if err != nil {
		return conformance.Fail(name, "first Apply returned error: %v", err)
	}
	if first == nil || !first.GetAccepted() {
		return conformance.Fail(name, "first Apply accepted = false, response=%v", first)
	}
	second, err := client.Apply(ctx, cloneApply(scenario.Apply))
	if err != nil {
		return conformance.Fail(name, "second Apply returned error: %v", err)
	}
	if second == nil || !second.GetAccepted() {
		return conformance.Fail(name, "second Apply accepted = false, response=%v", second)
	}
	return conformance.Pass(name)
}

func checkIsConvergedIsDeterministic(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Result {
	const name = "IsConvergedIsDeterministic"
	first, err := client.IsConverged(ctx, cloneIsConverged(scenario.IsConverged))
	if err != nil {
		return conformance.Fail(name, "first IsConverged returned error: %v", err)
	}
	second, err := client.IsConverged(ctx, cloneIsConverged(scenario.IsConverged))
	if err != nil {
		return conformance.Fail(name, "second IsConverged returned error: %v", err)
	}
	if !proto.Equal(first, second) {
		return conformance.Fail(name, "IsConverged is not deterministic: first=%v second=%v", first, second)
	}
	return conformance.Pass(name)
}

func checkRollbackIsIdempotent(ctx context.Context, client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Result {
	const name = "RollbackIsIdempotent"
	first, err := client.Rollback(ctx, cloneRollback(scenario.Rollback))
	if err != nil {
		return conformance.Fail(name, "first Rollback returned error: %v", err)
	}
	if first == nil || !first.GetAccepted() {
		return conformance.Fail(name, "first Rollback accepted = false, response=%v", first)
	}
	second, err := client.Rollback(ctx, cloneRollback(scenario.Rollback))
	if err != nil {
		return conformance.Fail(name, "second Rollback returned error: %v", err)
	}
	if second == nil || !second.GetAccepted() {
		return conformance.Fail(name, "second Rollback accepted = false, response=%v", second)
	}
	return conformance.Pass(name)
}

func checkApplyRespectsContextCancellation(client kaiv1alpha1.ActuatorServiceClient, scenario Scenario) conformance.Result {
	const name = "ApplyRespectsContextCancellation"
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Apply(cancelledCtx, cloneApply(scenario.Apply)); err == nil {
		return conformance.Fail(name, "Apply with cancelled context returned nil error")
	}
	return conformance.Pass(name)
}

func cloneApply(msg *kaiv1alpha1.ApplyRequest) *kaiv1alpha1.ApplyRequest {
	if msg == nil {
		return nil
	}
	return proto.Clone(msg).(*kaiv1alpha1.ApplyRequest)
}

func cloneIsConverged(msg *kaiv1alpha1.IsConvergedRequest) *kaiv1alpha1.IsConvergedRequest {
	if msg == nil {
		return nil
	}
	return proto.Clone(msg).(*kaiv1alpha1.IsConvergedRequest)
}

func cloneRollback(msg *kaiv1alpha1.RollbackRequest) *kaiv1alpha1.RollbackRequest {
	if msg == nil {
		return nil
	}
	return proto.Clone(msg).(*kaiv1alpha1.RollbackRequest)
}

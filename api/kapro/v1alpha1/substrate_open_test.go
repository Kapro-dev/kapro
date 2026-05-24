package v1alpha1

import "testing"

func TestSubstrateSpecCanonicalOpenSubstrate(t *testing.T) {
	spec := SubstrateSpec{
		Substrate: &SubstrateImplementationSpec{Kind: "hello-world", Actuator: "hello-world"},
		Execution: &SubstrateExecutionSpec{Mode: ExecutionModeHubPush},
	}
	if got := spec.SubstrateKind(); got != "hello-world" {
		t.Fatalf("SubstrateKind() = %q", got)
	}
	if got := spec.ActuatorName(); got != "hello-world" {
		t.Fatalf("ActuatorName() = %q", got)
	}
	if got := spec.ExecutionMode(); got != ExecutionModeHubPush {
		t.Fatalf("ExecutionMode() = %q", got)
	}
}

func TestSubstrateSpecLegacyFallback(t *testing.T) {
	spec := SubstrateSpec{
		Substrate: &SubstrateImplementationSpec{Kind: "flux", Actuator: "flux"},
		Execution: &SubstrateExecutionSpec{Mode: ExecutionModeSpokePull},
	}
	if got := spec.SubstrateKind(); got != "flux" {
		t.Fatalf("SubstrateKind() = %q", got)
	}
	if got := spec.ActuatorName(); got != "flux" {
		t.Fatalf("ActuatorName() = %q", got)
	}
	if got := spec.ExecutionMode(); got != ExecutionModeSpokePull {
		t.Fatalf("ExecutionMode() = %q", got)
	}
}

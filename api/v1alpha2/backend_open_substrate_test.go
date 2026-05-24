package v1alpha2

import "testing"

func TestBackendSpecCanonicalOpenSubstrate(t *testing.T) {
	spec := BackendSpec{
		Substrate: &BackendSubstrateSpec{Kind: "hello-world", Actuator: "hello-world"},
		Execution: &BackendExecutionSpec{Mode: ExecutionModeHubPush},
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

func TestBackendSpecLegacyFallback(t *testing.T) {
	spec := BackendSpec{Driver: BackendDriverFlux, Runtime: BackendRuntimeSpoke}
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

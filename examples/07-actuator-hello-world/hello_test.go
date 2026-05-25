package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/actuator"
)

// TestHelloWorldRegistersAsCustomSubstrate is the CI guarantee for the
// open-substrate claim: a custom substrate authored via the public SDK
// (only public imports — no internal/*) must register, resolve, and apply
// through the public registry path.
//
// If a change to pkg/kapro/actuator breaks the authoring shape used by
// hello-world, this test fails before the PR merges.
func TestHelloWorldRegistersAsCustomSubstrate(t *testing.T) {
	reg := actuator.NewRegistry()

	sub := actuator.NewBoolFunc("hello-world", func(_ context.Context, req actuator.ApplyRequest) (bool, string, error) {
		return true, fmt.Sprintf("delivered %s", req.Version), nil
	})
	if err := reg.RegisterRegistration(actuator.Registration{
		Name:     "hub-push/hello-world",
		Mode:     kaprov1alpha1.SubstrateModePush,
		Actuator: sub,
	}); err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}

	resolved, err := reg.Resolve("hub-push/hello-world")
	if err != nil {
		t.Fatalf("Resolve hub-push/hello-world: %v", err)
	}
	if resolved == nil {
		t.Fatalf("Resolve returned nil actuator")
	}

	cluster := &kaprov1alpha1.Cluster{}
	if err := resolved.Apply(context.Background(), actuator.ApplyRequest{Cluster: cluster, Version: "v1.2.3"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// TestHelloWorldExposesCapabilities asserts that BoolFunc-backed substrates
// declare an honest capability profile. Production actuators should declare
// their bits explicitly; this test pins the BoolFunc default profile so a
// change to it surfaces as a deliberate diff.
func TestHelloWorldExposesCapabilities(t *testing.T) {
	sub := actuator.NewBoolFunc("hello-world", func(_ context.Context, _ actuator.ApplyRequest) (bool, string, error) {
		return true, "ok", nil
	})

	caps := sub.Capabilities()

	if !caps.SupportsApply {
		t.Errorf("BoolFunc must support Apply; got %#v", caps)
	}
	if !caps.SupportsObserve {
		t.Errorf("BoolFunc must support Observe (trivial); got %#v", caps)
	}
	if caps.SupportsRollback {
		t.Errorf("BoolFunc must NOT advertise Rollback (cannot unwind side effects); got %#v", caps)
	}
	if !caps.SupportsHubExecution {
		t.Errorf("BoolFunc must support hub execution (in-process); got %#v", caps)
	}
	if caps.SubstrateKind != "hello-world" {
		t.Errorf("Capabilities.SubstrateKind = %q, want hello-world", caps.SubstrateKind)
	}
	if caps.ContractVersion == "" {
		t.Errorf("Capabilities.ContractVersion must be set after Normalize")
	}
}

// TestHelloWorldRejectsConflictingRegistration proves the registry validates
// substrate names before they reach the controller. A custom-substrate author
// who picks a name colliding with a built-in or an existing custom must see
// the error at registration, not at resolve time.
func TestHelloWorldRejectsConflictingRegistration(t *testing.T) {
	reg := actuator.NewRegistry()

	sub := actuator.NewBoolFunc("hello-world", func(_ context.Context, _ actuator.ApplyRequest) (bool, string, error) {
		return true, "ok", nil
	})

	if err := reg.RegisterRegistration(actuator.Registration{
		Name:     "hub-push/hello-world",
		Mode:     kaprov1alpha1.SubstrateModePush,
		Actuator: sub,
	}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := reg.RegisterRegistration(actuator.Registration{
		Name:     "hub-push/hello-world",
		Mode:     kaprov1alpha1.SubstrateModePush,
		Actuator: sub,
	})
	if err == nil {
		t.Fatalf("expected duplicate-registration error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error %q does not mention duplicate registration", err)
	}
}

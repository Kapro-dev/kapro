package actuator

import (
	"context"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

type stubActuator struct{}

func (stubActuator) Apply(context.Context, ApplyRequest) error { return nil }
func (stubActuator) IsConverged(context.Context, *kaprov1alpha2.Cluster, string, string) (bool, error) {
	return true, nil
}
func (stubActuator) Rollback(context.Context, *kaprov1alpha2.Cluster, string, string) error {
	return nil
}
func (stubActuator) ApplyDelta(context.Context, DeltaApplyRequest) (int, error) { return 0, nil }
func (stubActuator) IsAllConverged(context.Context, *kaprov1alpha2.Cluster, map[string]string) (bool, error) {
	return true, nil
}

func TestRegistryRegisterRegistrationStoresCapabilities(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterRegistration(Registration{
		Mode: kaprov1alpha2.DeliveryModePush,
		Capabilities: Capabilities{
			Driver:              kaprov1alpha2.BackendDriverArgo,
			Runtime:             kaprov1alpha2.BackendRuntimeHub,
			SupportsApply:       true,
			SupportsConvergence: true,
		},
		Actuator: stubActuator{},
	})
	if err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}

	if _, err := registry.Resolve("push/argo"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	reg, ok := registry.Registration("push/argo")
	if !ok {
		t.Fatalf("Registration not found")
	}
	if reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		t.Fatalf("contract version = %q", reg.Capabilities.ContractVersion)
	}
	if !reg.Capabilities.SupportsMode(kaprov1alpha2.DeliveryModePush) {
		t.Fatalf("capabilities do not include push mode: %#v", reg.Capabilities)
	}
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterRegistration(Registration{Actuator: stubActuator{}}); err == nil {
		t.Fatalf("RegisterRegistration with empty key succeeded")
	}
	if err := registry.RegisterRegistration(Registration{Name: "push/flux"}); err == nil {
		t.Fatalf("RegisterRegistration with nil actuator succeeded")
	}
}

func TestRegistryRegisterKeepsLegacyPermissiveBehavior(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("", nil); err != nil {
		t.Fatalf("legacy Register rejected empty nil entry: %v", err)
	}
	got, err := registry.Resolve("")
	if err != nil {
		t.Fatalf("Resolve legacy empty key: %v", err)
	}
	if got != nil {
		t.Fatalf("Resolve legacy empty key = %#v, want nil", got)
	}
}

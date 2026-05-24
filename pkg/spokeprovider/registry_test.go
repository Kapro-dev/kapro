package spokeprovider

import (
	"context"
	"strings"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type stubProvider struct {
	driver kaprov1alpha1.SubstrateDriver
}

func (s *stubProvider) Driver() kaprov1alpha1.SubstrateDriver { return s.driver }
func (s *stubProvider) Capabilities() Capabilities {
	return Capabilities{
		Driver:            s.driver,
		SupportsReconcile: true,
		SupportsObserve:   true,
	}
}
func (s *stubProvider) Reconcile(ctx context.Context, req ReconcileRequest) ReconcileResult {
	return ReconcileResult{Phase: kaprov1alpha1.DeliveryPhaseConverged}
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	if err := r.Register(kaprov1alpha1.SubstrateDriverOCI, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Resolve(kaprov1alpha1.SubstrateDriverOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != p {
		t.Fatalf("Resolve returned a different provider instance")
	}
	reg, ok := r.Registration(kaprov1alpha1.SubstrateDriverOCI)
	if !ok || reg.Capabilities.Driver != kaprov1alpha1.SubstrateDriverOCI || !reg.Capabilities.SupportsReconcile {
		t.Fatalf("registration = %#v/%v, want OCI reconcile metadata", reg, ok)
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	if err := r.Register(kaprov1alpha1.SubstrateDriverOCI, p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(kaprov1alpha1.SubstrateDriverOCI, p)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate-registration error, got %v", err)
	}
}

func TestRegistry_RegisterRejectsEmptyAndNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", &stubProvider{}); err == nil {
		t.Fatalf("expected error for empty driver")
	}
	if err := r.Register(kaprov1alpha1.SubstrateDriverOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_UpsertReturnsPrevious(t *testing.T) {
	r := NewRegistry()
	first := &stubProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	second := &stubProvider{driver: kaprov1alpha1.SubstrateDriverOCI}

	prev, err := r.Upsert(kaprov1alpha1.SubstrateDriverOCI, first)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if prev != nil {
		t.Fatalf("first Upsert returned non-nil prev: %v", prev)
	}
	prev, err = r.Upsert(kaprov1alpha1.SubstrateDriverOCI, second)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if prev != first {
		t.Fatalf("second Upsert did not return the first provider")
	}
	got, err := r.Resolve(kaprov1alpha1.SubstrateDriverOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != second {
		t.Fatalf("Resolve did not return the replaced provider")
	}
}

func TestRegistry_RegisterRegistrationStoresMetadata(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverFlux}
	if err := r.RegisterRegistration(Registration{
		Capabilities: Capabilities{
			Driver:            kaprov1alpha1.SubstrateDriverFlux,
			SupportsReconcile: true,
			SupportsObserve:   true,
		},
		Provider: p,
	}); err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}
	reg, ok := r.Registration(kaprov1alpha1.SubstrateDriverFlux)
	if !ok || reg.Provider != p || reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		t.Fatalf("registration = %#v/%v", reg, ok)
	}
}

func TestRegistry_RegisterRegistrationRejectsMetadataMismatch(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverFlux}
	err := r.RegisterRegistration(Registration{
		Driver: kaprov1alpha1.SubstrateDriverFlux,
		Capabilities: Capabilities{
			ContractVersion:   ContractVersionV1Alpha1,
			Driver:            kaprov1alpha1.SubstrateDriverOCI,
			SupportsReconcile: true,
		},
		Provider: p,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected capabilities-driver mismatch error, got %v", err)
	}
}

func TestRegistry_RegisterRegistrationRejectsProviderDriverMismatch(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverFlux}
	err := r.RegisterRegistration(Registration{
		Driver:   kaprov1alpha1.SubstrateDriverOCI,
		Provider: p,
	})
	if err == nil || !strings.Contains(err.Error(), "provider driver") {
		t.Fatalf("expected provider-driver mismatch error, got %v", err)
	}
}

func TestRegistry_RegisterRegistrationRejectsUnknownContract(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverFlux}
	err := r.RegisterRegistration(Registration{
		Driver: kaprov1alpha1.SubstrateDriverFlux,
		Capabilities: Capabilities{
			ContractVersion:   "v9",
			Driver:            kaprov1alpha1.SubstrateDriverFlux,
			SupportsReconcile: true,
		},
		Provider: p,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported provider contract version") {
		t.Fatalf("expected unsupported-contract error, got %v", err)
	}
}

func TestRegistry_UpsertRejectsEmptyAndNil(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Upsert("", &stubProvider{}); err == nil {
		t.Fatalf("expected error for empty driver")
	}
	if _, err := r.Upsert(kaprov1alpha1.SubstrateDriverOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateDriverFlux}
	if err := r.Register(kaprov1alpha1.SubstrateDriverFlux, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	prev, ok := r.Unregister(kaprov1alpha1.SubstrateDriverFlux)
	if !ok || prev != p {
		t.Fatalf("Unregister returned ok=%v prev=%v", ok, prev)
	}
	if _, err := r.Resolve(kaprov1alpha1.SubstrateDriverFlux); err == nil {
		t.Fatalf("expected Resolve to fail after Unregister")
	}
	if _, ok := r.Unregister(kaprov1alpha1.SubstrateDriverFlux); ok {
		t.Fatalf("expected ok=false on double unregister")
	}
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Resolve(kaprov1alpha1.SubstrateDriverExternal)
	if err == nil || !strings.Contains(err.Error(), "unknown substrate driver") {
		t.Fatalf("expected unknown-driver error, got %v", err)
	}
}

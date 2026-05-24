package spokeprovider

import (
	"context"
	"strings"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type stubProvider struct {
	driver kaprov1alpha1.SubstrateKind
}

func (s *stubProvider) SubstrateKind() kaprov1alpha1.SubstrateKind { return s.driver }
func (s *stubProvider) Capabilities() Capabilities {
	return Capabilities{
		SubstrateKind:     s.driver,
		SupportsReconcile: true,
		SupportsObserve:   true,
	}
}
func (s *stubProvider) Reconcile(ctx context.Context, req ReconcileRequest) ReconcileResult {
	return ReconcileResult{Phase: kaprov1alpha1.DeliveryPhaseConverged}
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindOCI}
	if err := r.Register(kaprov1alpha1.SubstrateKindOCI, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Resolve(kaprov1alpha1.SubstrateKindOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != p {
		t.Fatalf("Resolve returned a different provider instance")
	}
	reg, ok := r.Registration(kaprov1alpha1.SubstrateKindOCI)
	if !ok || reg.Capabilities.SubstrateKind != kaprov1alpha1.SubstrateKindOCI || !reg.Capabilities.SupportsReconcile {
		t.Fatalf("registration = %#v/%v, want OCI reconcile metadata", reg, ok)
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindOCI}
	if err := r.Register(kaprov1alpha1.SubstrateKindOCI, p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(kaprov1alpha1.SubstrateKindOCI, p)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate-registration error, got %v", err)
	}
}

func TestRegistry_RegisterRejectsEmptyAndNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", &stubProvider{}); err == nil {
		t.Fatalf("expected error for empty driver")
	}
	if err := r.Register(kaprov1alpha1.SubstrateKindOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_UpsertReturnsPrevious(t *testing.T) {
	r := NewRegistry()
	first := &stubProvider{driver: kaprov1alpha1.SubstrateKindOCI}
	second := &stubProvider{driver: kaprov1alpha1.SubstrateKindOCI}

	prev, err := r.Upsert(kaprov1alpha1.SubstrateKindOCI, first)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if prev != nil {
		t.Fatalf("first Upsert returned non-nil prev: %v", prev)
	}
	prev, err = r.Upsert(kaprov1alpha1.SubstrateKindOCI, second)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if prev != first {
		t.Fatalf("second Upsert did not return the first provider")
	}
	got, err := r.Resolve(kaprov1alpha1.SubstrateKindOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != second {
		t.Fatalf("Resolve did not return the replaced provider")
	}
}

func TestRegistry_RegisterRegistrationStoresMetadata(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindFlux}
	if err := r.RegisterRegistration(Registration{
		Capabilities: Capabilities{
			SubstrateKind:     kaprov1alpha1.SubstrateKindFlux,
			SupportsReconcile: true,
			SupportsObserve:   true,
		},
		Provider: p,
	}); err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}
	reg, ok := r.Registration(kaprov1alpha1.SubstrateKindFlux)
	if !ok || reg.Provider != p || reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		t.Fatalf("registration = %#v/%v", reg, ok)
	}
}

func TestRegistry_RegisterRegistrationRejectsMetadataMismatch(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindFlux}
	err := r.RegisterRegistration(Registration{
		SubstrateKind: kaprov1alpha1.SubstrateKindFlux,
		Capabilities: Capabilities{
			ContractVersion:   ContractVersionV1Alpha1,
			SubstrateKind:     kaprov1alpha1.SubstrateKindOCI,
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
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindFlux}
	err := r.RegisterRegistration(Registration{
		SubstrateKind: kaprov1alpha1.SubstrateKindOCI,
		Provider:      p,
	})
	if err == nil || !strings.Contains(err.Error(), "provider substrate kind") {
		t.Fatalf("expected provider-driver mismatch error, got %v", err)
	}
}

func TestRegistry_RegisterRegistrationRejectsUnknownContract(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindFlux}
	err := r.RegisterRegistration(Registration{
		SubstrateKind: kaprov1alpha1.SubstrateKindFlux,
		Capabilities: Capabilities{
			ContractVersion:   "v9",
			SubstrateKind:     kaprov1alpha1.SubstrateKindFlux,
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
	if _, err := r.Upsert(kaprov1alpha1.SubstrateKindOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha1.SubstrateKindFlux}
	if err := r.Register(kaprov1alpha1.SubstrateKindFlux, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	prev, ok := r.Unregister(kaprov1alpha1.SubstrateKindFlux)
	if !ok || prev != p {
		t.Fatalf("Unregister returned ok=%v prev=%v", ok, prev)
	}
	if _, err := r.Resolve(kaprov1alpha1.SubstrateKindFlux); err == nil {
		t.Fatalf("expected Resolve to fail after Unregister")
	}
	if _, ok := r.Unregister(kaprov1alpha1.SubstrateKindFlux); ok {
		t.Fatalf("expected ok=false on double unregister")
	}
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Resolve(kaprov1alpha1.SubstrateKindExternal)
	if err == nil || !strings.Contains(err.Error(), "unknown substrate kind") {
		t.Fatalf("expected unknown-driver error, got %v", err)
	}
}

package spokeprovider

import (
	"context"
	"strings"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

type stubProvider struct {
	driver kaprov1alpha2.BackendDriver
}

func (s *stubProvider) Driver() kaprov1alpha2.BackendDriver { return s.driver }
func (s *stubProvider) Reconcile(ctx context.Context, req ReconcileRequest) ReconcileResult {
	return ReconcileResult{Phase: kaprov1alpha2.DeliveryPhaseConverged}
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha2.BackendDriverOCI}
	if err := r.Register(kaprov1alpha2.BackendDriverOCI, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Resolve(kaprov1alpha2.BackendDriverOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != p {
		t.Fatalf("Resolve returned a different provider instance")
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha2.BackendDriverOCI}
	if err := r.Register(kaprov1alpha2.BackendDriverOCI, p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(kaprov1alpha2.BackendDriverOCI, p)
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate-registration error, got %v", err)
	}
}

func TestRegistry_RegisterRejectsEmptyAndNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", &stubProvider{}); err == nil {
		t.Fatalf("expected error for empty driver")
	}
	if err := r.Register(kaprov1alpha2.BackendDriverOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_UpsertReturnsPrevious(t *testing.T) {
	r := NewRegistry()
	first := &stubProvider{driver: kaprov1alpha2.BackendDriverOCI}
	second := &stubProvider{driver: kaprov1alpha2.BackendDriverOCI}

	prev, err := r.Upsert(kaprov1alpha2.BackendDriverOCI, first)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if prev != nil {
		t.Fatalf("first Upsert returned non-nil prev: %v", prev)
	}
	prev, err = r.Upsert(kaprov1alpha2.BackendDriverOCI, second)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if prev != first {
		t.Fatalf("second Upsert did not return the first provider")
	}
	got, err := r.Resolve(kaprov1alpha2.BackendDriverOCI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != second {
		t.Fatalf("Resolve did not return the replaced provider")
	}
}

func TestRegistry_UpsertRejectsEmptyAndNil(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Upsert("", &stubProvider{}); err == nil {
		t.Fatalf("expected error for empty driver")
	}
	if _, err := r.Upsert(kaprov1alpha2.BackendDriverOCI, nil); err == nil {
		t.Fatalf("expected error for nil provider")
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{driver: kaprov1alpha2.BackendDriverFlux}
	if err := r.Register(kaprov1alpha2.BackendDriverFlux, p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	prev, ok := r.Unregister(kaprov1alpha2.BackendDriverFlux)
	if !ok || prev != p {
		t.Fatalf("Unregister returned ok=%v prev=%v", ok, prev)
	}
	if _, err := r.Resolve(kaprov1alpha2.BackendDriverFlux); err == nil {
		t.Fatalf("expected Resolve to fail after Unregister")
	}
	if _, ok := r.Unregister(kaprov1alpha2.BackendDriverFlux); ok {
		t.Fatalf("expected ok=false on double unregister")
	}
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Resolve(kaprov1alpha2.BackendDriverExternal)
	if err == nil || !strings.Contains(err.Error(), "unknown backend driver") {
		t.Fatalf("expected unknown-driver error, got %v", err)
	}
}

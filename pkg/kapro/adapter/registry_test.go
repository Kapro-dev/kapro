package adapter

import (
	"context"
	"sync"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type stubAdapter struct {
	driver  kaprov1alpha1.SubstrateDriver
	runtime kaprov1alpha1.SubstrateRuntime
}

func (s stubAdapter) Driver() kaprov1alpha1.SubstrateDriver { return s.driver }
func (s stubAdapter) Runtime() kaprov1alpha1.SubstrateRuntime {
	return s.runtime
}
func (s stubAdapter) Capabilities() Capabilities {
	return Capabilities{
		Driver:           s.driver,
		Runtime:          s.runtime,
		SupportsApply:    true,
		SupportsObserve:  true,
		SupportsRollback: true,
		SupportsDiscover: true,
	}.Normalize()
}
func (s stubAdapter) Apply(context.Context, Request) (Result, error) {
	return Result{Driver: s.driver, Runtime: s.runtime}, nil
}
func (s stubAdapter) Observe(context.Context, Request) (Result, error) {
	return Result{Driver: s.driver, Runtime: s.runtime}, nil
}
func (s stubAdapter) Rollback(context.Context, Request) (Result, error) {
	return Result{Driver: s.driver, Runtime: s.runtime}, nil
}
func (s stubAdapter) Discover(context.Context, DiscoveryRequest) (DiscoveryResult, error) {
	return DiscoveryResult{Driver: s.driver, Runtime: s.runtime}, nil
}

func TestRegistryRegisterResolveAndDrivers(t *testing.T) {
	r := NewRegistry()
	flux := stubAdapter{driver: kaprov1alpha1.SubstrateDriverFlux, runtime: kaprov1alpha1.SubstrateRuntimeBoth}
	argo := stubAdapter{driver: kaprov1alpha1.SubstrateDriverArgo, runtime: kaprov1alpha1.SubstrateRuntimeHub}

	if err := r.Register(flux); err != nil {
		t.Fatalf("register flux: %v", err)
	}
	if err := r.Register(argo); err != nil {
		t.Fatalf("register argo: %v", err)
	}

	got, err := r.Resolve(kaprov1alpha1.SubstrateDriverFlux)
	if err != nil {
		t.Fatalf("resolve flux: %v", err)
	}
	if got.Driver() != kaprov1alpha1.SubstrateDriverFlux {
		t.Fatalf("resolved driver = %q, want %q", got.Driver(), kaprov1alpha1.SubstrateDriverFlux)
	}

	drivers := r.Drivers()
	if len(drivers) != 2 || drivers[0] != kaprov1alpha1.SubstrateDriverArgo || drivers[1] != kaprov1alpha1.SubstrateDriverFlux {
		t.Fatalf("drivers = %#v, want sorted argo, flux", drivers)
	}
}

func TestRegistryRejectsNilEmptyAndDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatalf("Register(nil) succeeded, want error")
	}
	if err := r.Register(stubAdapter{}); err == nil {
		t.Fatalf("Register(empty driver) succeeded, want error")
	}
	if err := r.Register(stubAdapter{driver: kaprov1alpha1.SubstrateDriverOCI}); err != nil {
		t.Fatalf("register oci: %v", err)
	}
	if err := r.Register(stubAdapter{driver: kaprov1alpha1.SubstrateDriverOCI}); err == nil {
		t.Fatalf("duplicate Register succeeded, want error")
	}
}

func TestRegistryUpsertAndUnregister(t *testing.T) {
	r := NewRegistry()
	first := stubAdapter{driver: kaprov1alpha1.SubstrateDriverFlux, runtime: kaprov1alpha1.SubstrateRuntimeSpoke}
	second := stubAdapter{driver: kaprov1alpha1.SubstrateDriverFlux, runtime: kaprov1alpha1.SubstrateRuntimeBoth}

	prev, err := r.Upsert(first)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if prev != nil {
		t.Fatalf("first upsert previous = %T, want nil", prev)
	}
	prev, err = r.Upsert(second)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if prev == nil || prev.Runtime() != kaprov1alpha1.SubstrateRuntimeSpoke {
		t.Fatalf("second upsert previous runtime = %v, want Spoke", prev)
	}

	removed, ok := r.Unregister(kaprov1alpha1.SubstrateDriverFlux)
	if !ok || removed.Runtime() != kaprov1alpha1.SubstrateRuntimeBoth {
		t.Fatalf("unregister = (%v, %v), want second adapter", removed, ok)
	}
	if _, err := r.Resolve(kaprov1alpha1.SubstrateDriverFlux); err == nil {
		t.Fatalf("resolve after unregister succeeded, want error")
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(stubAdapter{driver: kaprov1alpha1.SubstrateDriverFlux}); err != nil {
		t.Fatalf("register flux: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Resolve(kaprov1alpha1.SubstrateDriverFlux); err != nil {
				t.Errorf("resolve flux: %v", err)
			}
			_ = r.Drivers()
		}()
	}
	wg.Wait()
}

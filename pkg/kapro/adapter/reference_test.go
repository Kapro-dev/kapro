package adapter_test

import (
	"context"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/argocd"
	"kapro.io/kapro/pkg/kapro/adapter/flux"
	"kapro.io/kapro/pkg/kapro/adapter/oci"
)

func TestReferenceAdaptersExposeDriversAndDiscoveryModels(t *testing.T) {
	tests := []struct {
		name         string
		driver       kaprov1alpha2.BackendDriver
		wantReady    bool
		wantSelected int
		discover     func(context.Context) (bool, int, string, error)
	}{
		{
			name:         "argocd",
			driver:       kaprov1alpha2.BackendDriverArgo,
			wantReady:    true,
			wantSelected: 2,
			discover: func(ctx context.Context) (bool, int, string, error) {
				a := argocd.New()
				result, err := a.Discover(ctx, discoveryRequest(a.Driver()))
				return result.Ready, len(result.SelectedObjects), result.Reason, err
			},
		},
		{
			name:         "flux",
			driver:       kaprov1alpha2.BackendDriverFlux,
			wantReady:    true,
			wantSelected: 5,
			discover: func(ctx context.Context) (bool, int, string, error) {
				a := flux.New()
				result, err := a.Discover(ctx, discoveryRequest(a.Driver()))
				return result.Ready, len(result.SelectedObjects), result.Reason, err
			},
		},
		{
			name:         "oci",
			driver:       kaprov1alpha2.BackendDriverOCI,
			wantReady:    false,
			wantSelected: 0,
			discover: func(ctx context.Context) (bool, int, string, error) {
				a := oci.New()
				result, err := a.Discover(ctx, discoveryRequest(a.Driver()))
				return result.Ready, len(result.SelectedObjects), result.Reason, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, selected, reason, err := tt.discover(context.Background())
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if tt.driver == "" {
				t.Fatalf("test driver is empty")
			}
			if ready != tt.wantReady || selected != tt.wantSelected {
				t.Fatalf("ready=%v selected=%d reason=%s, want ready=%v selected=%d", ready, selected, reason, tt.wantReady, tt.wantSelected)
			}
		})
	}
}

func TestReferenceAdaptersExposeCapabilities(t *testing.T) {
	for _, a := range []adapter.Adapter{argocd.New(), flux.New(), oci.New()} {
		caps := a.Capabilities()
		if caps.Driver != a.Driver() || caps.Runtime == "" {
			t.Fatalf("%s capabilities = %#v", a.Driver(), caps)
		}
		if caps.SupportsApply || caps.SupportsObserve || caps.SupportsRollback {
			t.Fatalf("%s reference adapter should not advertise side-effect capabilities: %#v", a.Driver(), caps)
		}
		if caps.SupportsDiscover != (a.Driver() != kaprov1alpha2.BackendDriverOCI) {
			t.Fatalf("%s SupportsDiscover = %v", a.Driver(), caps.SupportsDiscover)
		}
	}
}

func discoveryRequest(driver kaprov1alpha2.BackendDriver) adapter.DiscoveryRequest {
	return adapter.DiscoveryRequest{Driver: driver}
}

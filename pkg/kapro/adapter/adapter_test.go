package adapter

import (
	"context"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestReferenceAdapterDiscoveryAndUnsupportedOperations(t *testing.T) {
	a := NewReferenceAdapter(kaprov1alpha1.SubstrateKindArgo, kaprov1alpha1.ExecutionScopeHub, DiscoveryModel{
		Supported: true,
		SelectedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{{
			Kind: "Application",
			Name: "checkout",
		}},
	})

	discovery, err := a.Discover(context.Background(), DiscoveryRequest{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !discovery.Ready || len(discovery.SelectedObjects) != 1 {
		t.Fatalf("discovery = %#v, want ready with one selected object", discovery)
	}
	caps := a.Capabilities()
	if caps.SupportsApply || caps.SupportsObserve || caps.SupportsRollback || !caps.SupportsDiscover {
		t.Fatalf("capabilities = %#v, want discover-only reference adapter", caps)
	}

	result, err := a.Apply(context.Background(), Request{})
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if result.Phase != kaprov1alpha1.DeliveryPhaseFailed || result.Reason != "OperationUnsupported" {
		t.Fatalf("apply result = %#v, want failed OperationUnsupported", result)
	}
}

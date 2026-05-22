package adapter

import (
	"context"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestReferenceAdapterDiscoveryAndUnsupportedOperations(t *testing.T) {
	a := NewReferenceAdapter(kaprov1alpha2.BackendDriverArgo, kaprov1alpha2.BackendRuntimeHub, DiscoveryModel{
		Supported: true,
		SelectedObjects: []kaprov1alpha2.DiscoveredBackendObject{{
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

	result, err := a.Apply(context.Background(), Request{})
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if result.Phase != kaprov1alpha2.DeliveryPhaseFailed || result.Reason != "OperationUnsupported" {
		t.Fatalf("apply result = %#v, want failed OperationUnsupported", result)
	}
}

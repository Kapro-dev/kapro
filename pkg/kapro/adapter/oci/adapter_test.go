package oci_test

import (
	"context"
	"strings"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/oci"
)

func TestModelDescribesUnsupportedOCIDiscovery(t *testing.T) {
	model := oci.Model()

	if model.SubstrateKind != kaprov1alpha1.SubstrateKindOCI {
		t.Fatalf("driver = %q, want oci", model.SubstrateKind)
	}
	if model.ExecutionScope != kaprov1alpha1.ExecutionScopeSpoke {
		t.Fatalf("runtime = %q, want Spoke", model.ExecutionScope)
	}
	if model.DefaultNamespace != "" {
		t.Fatalf("default namespace = %q, want empty", model.DefaultNamespace)
	}
	if model.Supported {
		t.Fatal("OCI discovery should be unsupported")
	}
	if len(model.SelectedObjects) != 0 || len(model.SkippedObjects) != 0 || len(model.UnsupportedObjects) != 0 {
		t.Fatalf("object models should be empty: %#v", model)
	}
}

func TestNewReturnsDiscoverUnsupportedAdapter(t *testing.T) {
	adapter := oci.New()

	if adapter.SubstrateKind() != kaprov1alpha1.SubstrateKindOCI {
		t.Fatalf("driver = %q, want oci", adapter.SubstrateKind())
	}
	if adapter.ExecutionScope() != kaprov1alpha1.ExecutionScopeSpoke {
		t.Fatalf("runtime = %q, want Spoke", adapter.ExecutionScope())
	}
	caps := adapter.Capabilities()
	if caps.ContractVersion != "v1alpha1" ||
		caps.SupportsDiscover ||
		caps.SupportsApply ||
		caps.SupportsObserve ||
		caps.SupportsRollback {
		t.Fatalf("capabilities = %#v", caps)
	}

	result, err := adapter.Discover(context.Background(), kaproadapter.DiscoveryRequest{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Ready || result.Reason != "DiscoveryUnsupported" {
		t.Fatalf("discovery readiness = %t/%q, want unsupported", result.Ready, result.Reason)
	}
	if result.SubstrateKind != kaprov1alpha1.SubstrateKindOCI || result.ExecutionScope != kaprov1alpha1.ExecutionScopeSpoke {
		t.Fatalf("driver/runtime = %q/%q, want oci/Spoke", result.SubstrateKind, result.ExecutionScope)
	}
	if !strings.Contains(result.Message, string(kaprov1alpha1.SubstrateKindOCI)) {
		t.Fatalf("message %q does not mention OCI driver", result.Message)
	}
	if len(result.SelectedObjects) != 0 ||
		len(result.SkippedObjects) != 0 ||
		len(result.UnsupportedPatterns) != 0 ||
		len(result.SubstrateObjectStatusExamples) != 0 {
		t.Fatalf("discovery result should not include objects: %#v", result)
	}
}

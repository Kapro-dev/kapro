// Package oci provides public reference adapters for OCI delivery core.
package oci

import (
	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/adapter"
)

// New returns a discovery-first OCI reference adapter.
func New() adapter.Adapter {
	return adapter.NewReferenceAdapter(kaprov1alpha1.SubstrateKindOCI, kaprov1alpha1.ExecutionScopeSpoke, Model())
}

// Model returns the OCI delivery-core discovery shape. OCI delivery does not
// currently expose substrate-native Kubernetes object discovery.
func Model() adapter.DiscoveryModel {
	return adapter.DiscoveryModel{
		SubstrateKind:    kaprov1alpha1.SubstrateKindOCI,
		ExecutionScope:   kaprov1alpha1.ExecutionScopeSpoke,
		DefaultNamespace: "",
		Supported:        false,
	}
}

// Package oci provides public reference adapters for OCI delivery core.
package oci

import (
	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/kapro/adapter"
)

// New returns a discovery-first OCI reference adapter.
func New() adapter.Adapter {
	return adapter.NewReferenceAdapter(kaprov1alpha2.BackendDriverOCI, kaprov1alpha2.BackendRuntimeSpoke, Model())
}

// Model returns the OCI delivery-core discovery shape. OCI delivery does not
// currently expose backend-native Kubernetes object discovery.
func Model() adapter.DiscoveryModel {
	return adapter.DiscoveryModel{
		Driver:           kaprov1alpha2.BackendDriverOCI,
		Runtime:          kaprov1alpha2.BackendRuntimeSpoke,
		DefaultNamespace: "",
		Supported:        false,
	}
}

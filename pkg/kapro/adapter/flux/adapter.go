// Package flux provides public reference adapters for Flux backends.
package flux

import (
	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/kapro/adapter"
)

// New returns a discovery-first Flux reference adapter.
func New() adapter.Adapter {
	return adapter.NewReferenceAdapter(kaprov1alpha2.BackendDriverFlux, kaprov1alpha2.BackendRuntimeBoth, Model())
}

// Model returns the Flux discovery shape currently modeled by Backend
// discovery: source objects plus HelmRelease and Kustomization targets.
func Model() adapter.DiscoveryModel {
	return adapter.DiscoveryModel{
		Driver:           kaprov1alpha2.BackendDriverFlux,
		Runtime:          kaprov1alpha2.BackendRuntimeBoth,
		DefaultNamespace: "flux-system",
		Supported:        true,
		SelectedObjects: []kaprov1alpha2.DiscoveredBackendObject{
			{
				APIVersion:   "source.toolkit.fluxcd.io/v1",
				Kind:         "GitRepository",
				Pattern:      "gitrepository",
				Reason:       "selected Flux source revision target",
				VersionField: "spec.ref.branch",
			},
			{
				APIVersion:   "source.toolkit.fluxcd.io/v1",
				Kind:         "OCIRepository",
				Pattern:      "ocirepository",
				Reason:       "selected Flux source revision target",
				VersionField: "spec.ref.tag",
			},
			{
				APIVersion:   "source.toolkit.fluxcd.io/v1",
				Kind:         "Bucket",
				Pattern:      "bucket",
				Reason:       "selected Flux source revision target",
				VersionField: "spec.ref.branch",
			},
			{
				APIVersion:   "helm.toolkit.fluxcd.io/v2",
				Kind:         "HelmRelease",
				Pattern:      "helmrelease",
				Reason:       "selected Flux promotion target",
				VersionField: "spec.chart.spec.version",
			},
			{
				APIVersion:   "kustomize.toolkit.fluxcd.io/v1",
				Kind:         "Kustomization",
				Pattern:      "kustomization",
				Reason:       "selected Flux promotion target",
				VersionField: "spec.sourceRef.name + spec.path + source revision",
			},
		},
	}
}

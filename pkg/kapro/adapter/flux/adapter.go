// Package flux provides public reference adapters for Flux substrates.
package flux

import (
	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/adapter"
)

// New returns a discovery-first Flux reference adapter.
func New() adapter.Adapter {
	return adapter.NewReferenceAdapter(kaprov1alpha1.SubstrateKindFlux, kaprov1alpha1.ExecutionScopeBoth, Model())
}

// Model returns the Flux discovery shape currently modeled by Substrate
// discovery: source objects plus HelmRelease and Kustomization targets.
func Model() adapter.DiscoveryModel {
	return adapter.DiscoveryModel{
		SubstrateKind:    kaprov1alpha1.SubstrateKindFlux,
		ExecutionScope:   kaprov1alpha1.ExecutionScopeBoth,
		DefaultNamespace: "flux-system",
		Supported:        true,
		SelectedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{
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

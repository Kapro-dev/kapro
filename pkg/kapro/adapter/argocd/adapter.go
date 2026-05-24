// Package argocd provides a public reference adapter for Argo CD substrates.
package argocd

import (
	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/adapter"
)

// New returns a discovery-first Argo CD reference adapter.
func New() adapter.Adapter {
	return adapter.NewReferenceAdapter(kaprov1alpha1.SubstrateDriverArgo, kaprov1alpha1.SubstrateRuntimeHub, Model())
}

// Model returns the Argo CD discovery shape currently modeled by Substrate
// discovery: cluster Secrets, Applications, and ApplicationSets.
func Model() adapter.DiscoveryModel {
	return adapter.DiscoveryModel{
		Driver:           kaprov1alpha1.SubstrateDriverArgo,
		Runtime:          kaprov1alpha1.SubstrateRuntimeHub,
		DefaultNamespace: "argocd",
		Supported:        true,
		SelectedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{
			{
				APIVersion: "v1",
				Kind:       "Secret",
				Pattern:    "argocd-cluster-secret",
				Reason:     "selected Argo CD cluster Secret",
			},
			{
				APIVersion:   "argoproj.io/v1alpha1",
				Kind:         "Application",
				Pattern:      "application",
				Reason:       "selected Argo CD Application promotion target",
				VersionField: "spec.source.targetRevision",
			},
		},
		SkippedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{
			{
				APIVersion:   "argoproj.io/v1alpha1",
				Kind:         "Application",
				Pattern:      "applicationset-child",
				Reason:       "generated ApplicationSet children are reconciled from the ApplicationSet template",
				VersionField: "spec.source.targetRevision",
			},
			{
				APIVersion:   "argoproj.io/v1alpha1",
				Kind:         "ApplicationSet",
				Pattern:      "applicationset",
				Reason:       "use the ApplicationSet actuator plugin to write templates",
				VersionField: "spec.template.spec.source.targetRevision",
			},
		},
		UnsupportedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{
			{
				APIVersion:   "argoproj.io/v1alpha1",
				Kind:         "Application",
				Pattern:      "app-of-apps-root",
				Reason:       "root app-of-apps objects package child Applications",
				VersionField: "spec.source.targetRevision",
			},
		},
	}
}

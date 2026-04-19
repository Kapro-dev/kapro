package controller

import (
	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IndexKeyRelease is the field index key for Sync and Approval objects.
// The index value is the owning Release name.
//
// Registration: ReleaseReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyRelease: release.Name}
const IndexKeyRelease = "kapro.io/release"

// IndexKeyEnvironment is the field index key for Sync objects, indexed by
// Sync.Spec.EnvironmentRef.  Used by the ManagedCluster watch in
// SyncReconciler to wake up all Syncs targeting a cluster that just
// changed phase (e.g. became Converged).
//
// Registration: ReleaseReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyEnvironment: cluster.Spec.EnvironmentRef}
const IndexKeyEnvironment = "kapro.io/environment"

// labelExtractor returns an IndexerFunc that extracts a single label value.
// Returns nil (not indexed) when the label is absent.
func labelExtractor(key string) client.IndexerFunc {
	return func(obj client.Object) []string {
		labels := obj.GetLabels()
		if v, ok := labels[key]; ok {
			return []string{v}
		}
		return nil
	}
}

// environmentRefExtractor returns an IndexerFunc that extracts
// Sync.Spec.EnvironmentRef for the IndexKeyEnvironment index.
func environmentRefExtractor() client.IndexerFunc {
	return func(obj client.Object) []string {
		sync, ok := obj.(*kaprov1alpha1.Sync)
		if !ok {
			return nil
		}
		if sync.Spec.EnvironmentRef == "" {
			return nil
		}
		return []string{sync.Spec.EnvironmentRef}
	}
}

package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// IndexKeyRelease is the field index key for Approval objects.
// The index value is the owning Release name.
//
// Registration: ReleaseReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyRelease: release.Name}
const IndexKeyRelease = "kapro.io/release"

// IndexKeyActiveCluster is a field index on Release objects.
// The index values are the target names from Release.status.targets.
// This lets the MemberCluster→Release watch mapper avoid scanning all releases —
// it only wakes up releases that actually reference the changed cluster.
//
// Registration: ReleaseReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyActiveCluster: mc.Name}
const IndexKeyActiveCluster = "kapro.io/active-cluster"

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

// activeClusterExtractor returns an IndexerFunc that extracts all target names
// from Release.status.targets. This is the index backing IndexKeyActiveCluster.
func activeClusterExtractor(obj client.Object) []string {
	rel, ok := obj.(*kaprov1alpha1.Release)
	if !ok {
		return nil
	}
	clusters := make([]string, 0, len(rel.Status.Targets))
	seen := make(map[string]struct{}, len(rel.Status.Targets))
	for _, target := range rel.Status.Targets {
		if target.Target == "" {
			continue
		}
		if _, dup := seen[target.Target]; dup {
			continue
		}
		seen[target.Target] = struct{}{}
		clusters = append(clusters, target.Target)
	}
	return clusters
}

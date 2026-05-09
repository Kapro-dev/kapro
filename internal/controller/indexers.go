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

// IndexKeyActiveCluster is a field index on ReleaseTarget objects.
// The index values are the target cluster names from ReleaseTarget.spec.target.
// This lets the MemberCluster→Release mapper avoid scanning all releases.
//
// Registration: ReleaseReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyActiveCluster: mc.Name}
const IndexKeyActiveCluster = "kapro.io/active-cluster"

// IndexKeyReleaseTargetRelease indexes ReleaseTarget objects by owning Release name.
const IndexKeyReleaseTargetRelease = "kapro.io/release-target-release"

// IndexKeyReleaseProgressing indexes Release objects that are currently progressing.
const IndexKeyReleaseProgressing = "kapro.io/release-progressing"

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

// ActiveClusterExtractor returns an IndexerFunc that extracts the target cluster
// from ReleaseTarget.spec.target. This is the index backing IndexKeyActiveCluster.
func ActiveClusterExtractor(obj client.Object) []string {
	rt, ok := obj.(*kaprov1alpha1.ReleaseTarget)
	if !ok {
		return nil
	}
	if rt.Spec.Target == "" {
		return nil
	}
	return []string{rt.Spec.Target}
}

func ReleaseTargetReleaseExtractor(obj client.Object) []string {
	rt, ok := obj.(*kaprov1alpha1.ReleaseTarget)
	if !ok {
		return nil
	}
	if rt.Spec.ReleaseRef == "" {
		return nil
	}
	return []string{rt.Spec.ReleaseRef}
}

func ReleaseProgressingExtractor(obj client.Object) []string {
	release, ok := obj.(*kaprov1alpha1.Release)
	if !ok {
		return nil
	}
	if release.Status.Phase == kaprov1alpha1.ReleasePhaseProgressing {
		return []string{"true"}
	}
	return nil
}

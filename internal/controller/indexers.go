package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// IndexKeyPromotionRun is the field index key for Approval objects.
// The index value is the owning PromotionRun name.
//
// Registration: PromotionRunReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyPromotionRun: promotionrun.Name}
const IndexKeyPromotionRun = "kapro.io/promotionrun"

// IndexKeyActiveCluster is a field index on PromotionTarget objects.
// The index values are the target cluster names from PromotionTarget.spec.target.
// This lets the FleetCluster→PromotionRun mapper avoid scanning all promotionruns.
//
// Registration: PromotionRunReconciler.SetupWithManager registers the index once.
// Usage: client.MatchingFields{IndexKeyActiveCluster: mc.Name}
const IndexKeyActiveCluster = "kapro.io/active-cluster"

// IndexKeyPromotionTargetPromotionRun indexes PromotionTarget objects by owning PromotionRun name.
const IndexKeyPromotionTargetPromotionRun = "kapro.io/promotion-target-promotionrun"

// IndexKeyPromotionRunProgressing indexes PromotionRun objects that are currently progressing.
const IndexKeyPromotionRunProgressing = "kapro.io/promotionrun-progressing"

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
// from PromotionTarget.spec.target. This is the index backing IndexKeyActiveCluster.
func ActiveClusterExtractor(obj client.Object) []string {
	rt, ok := obj.(*kaproruntimev1alpha1.Target)
	if !ok {
		return nil
	}
	if rt.Spec.Target == "" {
		return nil
	}
	return []string{rt.Spec.Target}
}

func PromotionTargetPromotionRunExtractor(obj client.Object) []string {
	rt, ok := obj.(*kaproruntimev1alpha1.Target)
	if !ok {
		return nil
	}
	if rt.Spec.PromotionRunRef == "" {
		return nil
	}
	return []string{rt.Spec.PromotionRunRef}
}

func PromotionRunProgressingExtractor(obj client.Object) []string {
	promotionrun, ok := obj.(*kaproruntimev1alpha1.PromotionRun)
	if !ok {
		return nil
	}
	if promotionrun.Status.Phase == kaprov1alpha1.PromotionRunPhaseProgressing {
		return []string{"true"}
	}
	return nil
}

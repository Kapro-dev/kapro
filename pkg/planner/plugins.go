package planner

import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// NewDefaultFramework returns Kapro's built-in planner stack.
func NewDefaultFramework() *Framework {
	return NewFramework(
		ReadinessFilter{},
		ActivePromotionRunFilter{},
		DeterministicOrder{},
	)
}

// ReadinessFilter skips clusters that explicitly report Ready=False.
type ReadinessFilter struct{}

func (ReadinessFilter) Name() string { return "readiness" }

func (ReadinessFilter) Filter(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha2.Cluster) *Status {
	ready := apimeta.FindStatusCondition(target.Status.Conditions, "Ready")
	if ready != nil && ready.Status == metav1.ConditionFalse {
		return NewStatusReason(Skip, "ClusterNotReady", "cluster Ready condition is false")
	}
	return nil
}

// ActivePromotionRunFilter skips clusters already assigned to a different active PromotionRun.
type ActivePromotionRunFilter struct{}

func (ActivePromotionRunFilter) Name() string { return "active-promotionrun" }

func (ActivePromotionRunFilter) Filter(_ context.Context, _ *CycleState, req Request, target kaprov1alpha2.Cluster) *Status {
	if req.PromotionRun == nil || target.Status.ActivePromotionRun == "" || target.Status.ActivePromotionRun == req.PromotionRun.Name {
		return nil
	}
	return NewStatusReason(Skip, "DifferentActivePromotionRun", "cluster is already processing another promotionrun")
}

// DeterministicOrder is a no-op score plugin. The framework's stable name
// tiebreaker provides deterministic ordering after all score plugins run.
type DeterministicOrder struct{}

func (DeterministicOrder) Name() string { return "deterministic-order" }

func (DeterministicOrder) Score(context.Context, *CycleState, Request, kaprov1alpha2.Cluster) (int64, *Status) {
	return 0, nil
}

package planner

import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// NewDefaultFramework returns Kapro's built-in planner stack.
func NewDefaultFramework() *Framework {
	return NewFramework(
		ReadinessFilter{},
		ActiveReleaseFilter{},
		DeterministicOrder{},
	)
}

// ReadinessFilter skips clusters that explicitly report Ready=False.
type ReadinessFilter struct{}

func (ReadinessFilter) Name() string { return "readiness" }

func (ReadinessFilter) Filter(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha1.MemberCluster) *Status {
	ready := apimeta.FindStatusCondition(target.Status.Conditions, "Ready")
	if ready != nil && ready.Status == metav1.ConditionFalse {
		return NewStatusReason(Skip, "ClusterNotReady", "cluster Ready condition is false")
	}
	return nil
}

// ActiveReleaseFilter skips clusters already assigned to a different active Release.
type ActiveReleaseFilter struct{}

func (ActiveReleaseFilter) Name() string { return "active-release" }

func (ActiveReleaseFilter) Filter(_ context.Context, _ *CycleState, req Request, target kaprov1alpha1.MemberCluster) *Status {
	if req.Release == nil || target.Status.ActiveRelease == "" || target.Status.ActiveRelease == req.Release.Name {
		return nil
	}
	return NewStatusReason(Skip, "DifferentActiveRelease", "cluster is already processing another release")
}

// DeterministicOrder is a no-op score plugin. The framework's stable name
// tiebreaker provides deterministic ordering after all score plugins run.
type DeterministicOrder struct{}

func (DeterministicOrder) Name() string { return "deterministic-order" }

func (DeterministicOrder) Score(context.Context, *CycleState, Request, kaprov1alpha1.MemberCluster) (int64, *Status) {
	return 0, nil
}

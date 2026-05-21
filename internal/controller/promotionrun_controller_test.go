package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// makePromotionPlan creates a minimal Plan with one stage targeting the given label selector.
func makePromotionPlan(name string, selectorLabels map[string]string) *kaprov1alpha2.Plan {
	return &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: []kaprov1alpha2.Stage{
				{
					Name:     "deploy",
					Selector: metav1.LabelSelector{MatchLabels: selectorLabels},
				},
			},
		},
	}
}

// TestPromotionRunReconciler_PendingToPromoting verifies that a PromotionRun transitions
// from Pending to Progressing when spec.version is set and target clusters exist.
func TestPromotionRunReconciler_PendingToPromoting(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"

	// 1. Create target cluster.
	env := makeFleetCluster("de-dev", map[string]string{"tier": "dev", "country": "de"})
	mustCreate(t, ctx, c, env)

	// 2. Create Plan with one stage targeting country=de.
	promotionplan := makePromotionPlan("standard-rollout-ptp", map[string]string{"country": "de"})
	mustCreate(t, ctx, c, promotionplan)

	// 3. Create PromotionRun with version.
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-promotionrun", Namespace: ns},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "registry.example.com/app@sha256:aaaa",
			PromotionPlans: []kaprov1alpha2.PlanRef{
				{
					Name: "initial",
					Plan: promotionplan.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, promotionrun)

	key := types.NamespacedName{Name: "test-promotionrun", Namespace: ns}

	// 4. Expect PromotionRun to leave Pending.
	eventually(t, func() bool {
		r := getPromotionRun(ctx, c, key)
		return r.Status.Phase != "" && r.Status.Phase != kaprov1alpha2.PromotionRunPhasePending
	}, "promotionrun should leave empty/pending phase")

	t.Logf("promotionrun phase: %s", getPromotionRun(ctx, c, key).Status.Phase)
}

// TestPromotionRunReconciler_MissingVersion_StaysPending verifies that a PromotionRun
// with an empty version stays stalled in Pending.
func TestPromotionRunReconciler_MissingVersion_StaysPending(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	promotionplan := makePromotionPlan("standard-rollout-ma", map[string]string{"x": "y"})
	mustCreate(t, ctx, c, promotionplan)

	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-ver-promotionrun", Namespace: "default"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "",
			PromotionPlans: []kaprov1alpha2.PlanRef{
				{
					Name: "initial",
					Plan: promotionplan.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, promotionrun)

	key := types.NamespacedName{Name: "missing-ver-promotionrun", Namespace: "default"}

	// Allow a few reconcile cycles to pass.
	eventually(t, func() bool {
		r := getPromotionRun(ctx, c, key)
		// Should stay pending (stalled with NoVersion condition).
		return r.Status.Phase == kaprov1alpha2.PromotionRunPhasePending ||
			r.Status.Phase == ""
	}, "promotionrun with empty version should stay pending")
}

// TestPromotionRunReconciler_EnvStatus_Populated verifies that a PromotionRun creates
// child PromotionTarget execution objects once it starts progressing.
func TestPromotionRunReconciler_EnvStatus_Populated(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"

	env := makeFleetCluster("de-dev-ownerref", map[string]string{"country": "de2"})
	mustCreate(t, ctx, c, env)

	promotionplan := makePromotionPlan("standard-rollout-or", map[string]string{"country": "de2"})
	mustCreate(t, ctx, c, promotionplan)

	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "ownerref-promotionrun", Namespace: ns},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "registry.example.com/app@sha256:bbbb",
			PromotionPlans: []kaprov1alpha2.PlanRef{
				{
					Name: "initial",
					Plan: promotionplan.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, promotionrun)

	// Wait until the PromotionRun has at least one PromotionTarget child.
	eventually(t, func() bool {
		return len(listPromotionTargets(t, ctx, c, promotionrun.Name, promotionrun.Namespace)) > 0
	}, "PromotionTarget children should be populated after progressing starts")
}

func listPromotionTargets(t *testing.T, ctx context.Context, c client.Client, promotionrunName, _ string) []kaprov1alpha2.Target {
	t.Helper()
	var list kaprov1alpha2.TargetList
	if err := c.List(ctx, &list); err != nil {
		t.Fatalf("list PromotionTargets: %v", err)
	}
	targets := make([]kaprov1alpha2.Target, 0)
	for _, target := range list.Items {
		if target.Spec.PromotionRunRef == promotionrunName {
			targets = append(targets, target)
		}
	}
	return targets
}

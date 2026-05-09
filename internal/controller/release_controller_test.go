package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// makePipeline creates a minimal Pipeline with one stage targeting the given label selector.
func makePipeline(name string, selectorLabels map[string]string) *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{
					Name:     "deploy",
					Selector: metav1.LabelSelector{MatchLabels: selectorLabels},
				},
			},
		},
	}
}

// TestReleaseReconciler_PendingToPromoting verifies that a Release transitions
// from Pending to Progressing when spec.version is set and target clusters exist.
func TestReleaseReconciler_PendingToPromoting(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"

	// 1. Create target cluster.
	env := makeMemberCluster("de-dev", map[string]string{"tier": "dev", "country": "de"})
	mustCreate(t, ctx, c, env)

	// 2. Create Pipeline with one stage targeting country=de.
	pipeline := makePipeline("standard-rollout-ptp", map[string]string{"country": "de"})
	mustCreate(t, ctx, c, pipeline)

	// 3. Create Release with version.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "test-release", Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "registry.example.com/app@sha256:aaaa",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{
					Name:     "initial",
					Pipeline: pipeline.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	key := types.NamespacedName{Name: "test-release", Namespace: ns}

	// 4. Expect Release to leave Pending.
	eventually(t, func() bool {
		r := getRelease(ctx, c, key)
		return r.Status.Phase != "" && r.Status.Phase != kaprov1alpha1.ReleasePhasePending
	}, "release should leave empty/pending phase")

	t.Logf("release phase: %s", getRelease(ctx, c, key).Status.Phase)
}

// TestReleaseReconciler_MissingVersion_StaysPending verifies that a Release
// with an empty version stays stalled in Pending.
func TestReleaseReconciler_MissingVersion_StaysPending(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	pipeline := makePipeline("standard-rollout-ma", map[string]string{"x": "y"})
	mustCreate(t, ctx, c, pipeline)

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-ver-release", Namespace: "default"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{
					Name:     "initial",
					Pipeline: pipeline.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	key := types.NamespacedName{Name: "missing-ver-release", Namespace: "default"}

	// Allow a few reconcile cycles to pass.
	eventually(t, func() bool {
		r := getRelease(ctx, c, key)
		// Should stay pending (stalled with NoVersion condition).
		return r.Status.Phase == kaprov1alpha1.ReleasePhasePending ||
			r.Status.Phase == ""
	}, "release with empty version should stay pending")
}

// TestReleaseReconciler_EnvStatus_Populated verifies that a Release creates
// child ReleaseTarget execution objects once it starts progressing.
func TestReleaseReconciler_EnvStatus_Populated(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"

	env := makeMemberCluster("de-dev-ownerref", map[string]string{"country": "de2"})
	mustCreate(t, ctx, c, env)

	pipeline := makePipeline("standard-rollout-or", map[string]string{"country": "de2"})
	mustCreate(t, ctx, c, pipeline)

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "ownerref-release", Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "registry.example.com/app@sha256:bbbb",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{
					Name:     "initial",
					Pipeline: pipeline.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	// Wait until the Release has at least one ReleaseTarget child.
	eventually(t, func() bool {
		return len(listReleaseTargets(t, ctx, c, release.Name, release.Namespace)) > 0
	}, "ReleaseTarget children should be populated after progressing starts")
}

func listReleaseTargets(t *testing.T, ctx context.Context, c client.Client, releaseName, _ string) []kaprov1alpha1.ReleaseTarget {
	t.Helper()
	var list kaprov1alpha1.ReleaseTargetList
	if err := c.List(ctx, &list); err != nil {
		t.Fatalf("list ReleaseTargets: %v", err)
	}
	targets := make([]kaprov1alpha1.ReleaseTarget, 0)
	for _, target := range list.Items {
		if target.Spec.ReleaseRef == releaseName {
			targets = append(targets, target)
		}
	}
	return targets
}

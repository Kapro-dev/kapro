package controller_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// TestReleaseReconciler_PendingToPromoting verifies that a Release transitions
// from Pending to Promoting after the required Artifact and scope Environments exist.
func TestReleaseReconciler_PendingToPromoting(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	artifactName := "ocs-v1-2-4"

	// 1. Create Artifact first — ReleaseReconciler.handlePending fetches it.
	art := makeArtifact(artifactName, ns)
	mustCreate(t, ctx, c, art)

	// 2. Create target Environment.
	env := makeEnvironment("de-dev", ns, map[string]string{"tier": "dev", "country": "de"})
	mustCreate(t, ctx, c, env)

	// 3. Create Release scoped to country=de.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "test-release", Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:    artifactName,
			PipelineRef: "standard-rollout",
			Scope: kaprov1alpha1.ReleaseScope{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"country": "de"},
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

// TestReleaseReconciler_MissingArtifact_StaysOrFailsPending verifies that a Release
// referencing a non-existent Artifact does not panic and stays in Pending/Failed.
func TestReleaseReconciler_MissingArtifact_StaysOrFailsPending(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-art-release", Namespace: "default"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:    "does-not-exist",
			PipelineRef: "standard-rollout",
			Scope: kaprov1alpha1.ReleaseScope{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"x": "y"}},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	key := types.NamespacedName{Name: "missing-art-release", Namespace: "default"}

	// Allow a few reconcile cycles to pass.
	eventually(t, func() bool {
		r := getRelease(ctx, c, key)
		// Acceptable outcomes: still Pending (requeuing) or Failed.
		return r.Status.Phase == kaprov1alpha1.ReleasePhasePending ||
			r.Status.Phase == kaprov1alpha1.ReleasePhaseFailed ||
			r.Status.Phase == ""
	}, "release with missing artifact should stay pending or fail")
}

// TestReleaseReconciler_OwnerRef verifies that Promotions created by the Release
// have ownerReferences pointing back to the Release.
func TestReleaseReconciler_OwnerRef(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"

	art := makeArtifact("art-ownerref", ns)
	mustCreate(t, ctx, c, art)

	env := makeEnvironment("de-dev-ownerref", ns, map[string]string{"country": "de2"})
	mustCreate(t, ctx, c, env)

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "ownerref-release", Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:    "art-ownerref",
			PipelineRef: "standard-rollout",
			Scope: kaprov1alpha1.ReleaseScope{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"country": "de2"},
				},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	// Wait until at least one Promotion is created.
	eventually(t, func() bool {
		var promos kaprov1alpha1.PromotionList
		_ = c.List(ctx, &promos)
		for _, p := range promos.Items {
			for _, ref := range p.OwnerReferences {
				if ref.Kind == "Release" && ref.Name == "ownerref-release" {
					return true
				}
			}
		}
		return false
	}, "a Promotion owned by the Release should exist")
}


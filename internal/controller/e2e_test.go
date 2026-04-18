package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// TestE2E_Release_Pipeline_BatchRun_Promotion_Converged is the full integration
// test of the Kapro state machine chain:
//
//	Release → (Promoting) creates Promotions per dev-tier env
//	       → (Progressing) creates BatchRun for prod-tier batch
//	       → BatchRun creates per-cluster Promotions
//	       → fake actuator signals Converged
//	       → BatchRun reaches Complete
//	       → Release reaches Complete
//
// Requires KUBEBUILDER_ASSETS to be set — skipped otherwise.
func TestE2E_Release_Pipeline_BatchRun_Promotion_Converged(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	// ── 1. Create Artifact ────────────────────────────────────────────────────
	art := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-art-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{{Type: "oci"}},
		},
	}
	mustCreate(t, ctx, c, art)

	// ── 2. Create Environments ────────────────────────────────────────────────
	// One dev env (matched by the promotion step selector).
	devEnv := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-dev-" + suffix,
			Labels: map[string]string{"tier": "dev", "e2e": suffix},
		},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	mustCreate(t, ctx, c, devEnv)

	// One prod env (matched by the batch-1 selector).
	prodEnv := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-prod-" + suffix,
			Labels: map[string]string{"tier": "prod", "e2e": suffix},
		},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	mustCreate(t, ctx, c, prodEnv)

	// ── 3. Create ClusterRegistrations with a live heartbeat ─────────────────
	// Simulates what cluster-controller writes. PromotionReconciler.handlePending
	// checks LastHeartbeat freshness before advancing.
	devReg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-reg-dev-" + suffix,
			Labels: map[string]string{"kapro.io/environment": "e2e-dev-" + suffix},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "e2e-dev-" + suffix,
		},
	}
	mustCreate(t, ctx, c, devReg)
	patchRegistrationConverged(t, ctx, c, devReg)

	prodReg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-reg-prod-" + suffix,
			Labels: map[string]string{"kapro.io/environment": "e2e-prod-" + suffix},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "e2e-prod-" + suffix,
		},
	}
	mustCreate(t, ctx, c, prodReg)
	patchRegistrationConverged(t, ctx, c, prodReg)

	// ── 4. Create Pipeline ────────────────────────────────────────────────────
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-pipeline-" + suffix},
		Spec: kaprov1alpha1.PipelineSpec{
			Promotion: kaprov1alpha1.PipelinePromotion{
				Steps: []kaprov1alpha1.PromotionStep{
					{
						Name:     "dev",
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev", "e2e": suffix}},
					},
				},
			},
			Progression: kaprov1alpha1.PipelineProgression{
				Batches: []kaprov1alpha1.Batch{
					{
						Name:      "batch-1",
						DependsOn: []string{},
						Selectors: []metav1.LabelSelector{
							{MatchLabels: map[string]string{"tier": "prod", "e2e": suffix}},
						},
					},
				},
			},
		},
	}
	mustCreate(t, ctx, c, pipeline)

	// ── 5. Create Release ─────────────────────────────────────────────────────
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-release-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:    art.Name,
			PipelineRef: pipeline.Name,
			Scope: kaprov1alpha1.ReleaseScope{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"e2e": suffix},
				},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	releaseKey := types.NamespacedName{Name: release.Name, Namespace: ns}

	// ── 6. Wait: Release leaves Pending ──────────────────────────────────────
	eventually(t, func() bool {
		r := getRelease(ctx, c, releaseKey)
		return r.Status.Phase != "" && r.Status.Phase != kaprov1alpha1.ReleasePhasePending
	}, "release should leave Pending")
	t.Logf("release phase after Pending: %s", getRelease(ctx, c, releaseKey).Status.Phase)

	// ── 7. Wait: at least one Promotion created (dev tier) ───────────────────
	eventually(t, func() bool {
		var promos kaprov1alpha1.PromotionList
		_ = c.List(ctx, &promos, client.InNamespace(ns),
			client.MatchingLabels{"kapro.io/release": release.Name})
		return len(promos.Items) > 0
	}, "at least one Promotion should be created for dev tier")

	// ── 8. Simulate convergence: update ClusterRegistrations with desired version
	// When the fake actuator's IsConverged returns true AND the ClusterRegistration
	// records the desiredVersion, handleApplying marks the Promotion as Converged.
	// Re-patch with a fresh timestamp to keep the heartbeat alive.
	patchRegistrationConverged(t, ctx, c, devReg)
	patchRegistrationConverged(t, ctx, c, prodReg)

	// ── 9. Wait: Release reaches Complete ────────────────────────────────────
	eventually(t, func() bool {
		r := getRelease(ctx, c, releaseKey)
		return r.Status.Phase == kaprov1alpha1.ReleasePhaseComplete
	}, "release should reach Complete — full chain Release→Promotions→BatchRun→Complete")

	t.Logf("✅ E2E chain complete — Release %s reached Complete", release.Name)
}

// patchRegistrationConverged sets a fresh heartbeat + Converged phase on a
// ClusterRegistration, simulating what cluster-controller writes after deployment.
func patchRegistrationConverged(t *testing.T, ctx context.Context, c client.Client, reg *kaprov1alpha1.ClusterRegistration) {
	t.Helper()
	// Re-fetch to get latest resource version before patching.
	latest := &kaprov1alpha1.ClusterRegistration{}
	if err := c.Get(ctx, types.NamespacedName{Name: reg.Name, Namespace: reg.Namespace}, latest); err != nil {
		t.Fatalf("get ClusterRegistration %s: %v", reg.Name, err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	latest.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	latest.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
	latest.Status.CurrentVersions = map[string]string{"default": "v1.0.0"}
	latest.Status.Health = kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true}
	if err := c.Status().Patch(ctx, latest, patch); err != nil {
		t.Fatalf("patch ClusterRegistration status %s: %v", reg.Name, err)
	}
}

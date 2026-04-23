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

// TestE2E_Release_Sync_Converged is the full integration test of
// the Kapro state machine chain:
//
//	Release → (Progressing) walks Pipeline DAG, creates Syncs per stage per env
//	       → Sync (dev stage) — fake actuator signals Converged
//	       → dev stage Complete; prod stage (dependsOn dev) starts
//	       → Syncs for prod stage Converge
//	       → Pipeline node reaches Complete
//	       → Release reaches Complete
//
// Requires KUBEBUILDER_ASSETS to be set — skipped otherwise.
func TestE2E_Release_Sync_Converged(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	// ── 1. Create Artifact ────────────────────────────────────────────────────
	art := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-art-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIRef{
					Repository: "172.17.0.1:5000/fleet-bundle",
					Tag:        "v1.0.0",
					Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			}},
		},
	}
	mustCreate(t, ctx, c, art)

	// resolve the version and app key the Syncs will carry, so we can
	// reflect them back in the MemberCluster status for convergence checks.
	resolvedVersion := art.Spec.Sources[0].OCI.Repository + "@" + art.Spec.Sources[0].OCI.Digest
	appKey := art.Name
	versions := map[string]string{appKey: resolvedVersion}

	// ── 2 + 3. Create MemberClusters with tier labels and live heartbeat ─────
	// MemberCluster.Name must match Sync.Spec.Target (looked up by name).
	// Tier labels are used by pipeline stage selectors.
	// SyncReconciler.handlePending checks LastHeartbeat freshness.
	// handleApplying checks CurrentVersions[appKey] == sync.Spec.Version.
	devReg := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-dev-" + suffix,
			Labels: map[string]string{"tier": "dev", "e2e": suffix},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	mustCreate(t, ctx, c, devReg)
	patchRegistrationConverged(t, ctx, c, devReg, versions)

	prodReg := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-prod-" + suffix,
			Labels: map[string]string{"tier": "prod", "e2e": suffix},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	mustCreate(t, ctx, c, prodReg)
	patchRegistrationConverged(t, ctx, c, prodReg, versions)

	// ── 4. Create Pipeline ────────────────────────────────────────────────────
	// Two stages: "dev" runs first, "prod" depends on it.
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-pipeline-" + suffix},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{
					Name:     "dev",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev", "e2e": suffix}},
				},
				{
					Name:      "prod",
					Selector:  metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod", "e2e": suffix}},
					DependsOn: []string{"dev"},
				},
			},
		},
	}
	mustCreate(t, ctx, c, pipeline)

	// ── 5. Create Release ─────────────────────────────────────────────────────
	// Single pipeline node referencing the pipeline CRD.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-release-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: art.Name,
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{
					Name:     "initial",
					Pipeline: pipeline.Name,
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

	// ── 7. Wait: at least one target entry created in release.Status.Targets ─
	// After the Sync CRD fold, ReleaseReconciler tracks target progress
	// inline in release.Status.Targets — no standalone Sync objects.
	eventually(t, func() bool {
		r := getRelease(ctx, c, releaseKey)
		return len(r.Status.Targets) > 0
	}, "at least one TargetStatus should be created in release.Status.Targets")

	// ── 8. Keep MemberClusters fresh so Syncs can converge ──────────────────
	patchRegistrationConverged(t, ctx, c, devReg, versions)
	patchRegistrationConverged(t, ctx, c, prodReg, versions)

	// ── 9. Wait: Release reaches Complete ────────────────────────────────────
	// The full chain (dev stage + prod stage, each going through env FSM states)
	// can take >20s; use a longer deadline here.
	eventuallyLong(t, func() bool {
		// Log target phases to aid debugging.
		r := getRelease(ctx, c, releaseKey)
		for _, target := range r.Status.Targets {
			t.Logf("  Target %s/%s/%s phase=%s", target.PipelineRef, target.Stage, target.Target, target.Phase)
		}
		t.Logf("  Release phase=%s", r.Status.Phase)
		return r.Status.Phase == kaprov1alpha1.ReleasePhaseComplete
	}, "release should reach Complete — full chain Release→Env FSM→Converged→Complete")

	t.Logf("✅ E2E chain complete — Release %s reached Complete", release.Name)
}

// patchRegistrationConverged sets a fresh heartbeat + Converged phase on a
// MemberCluster, simulating what cluster-controller writes after deployment.
// currentVersions should map appKey→version for all syncs that should converge.
func patchRegistrationConverged(t *testing.T, ctx context.Context, c client.Client, reg *kaprov1alpha1.MemberCluster, currentVersions map[string]string) {
	t.Helper()
	// Retry Get — caching client may not have synced immediately after Create.
	latest := &kaprov1alpha1.MemberCluster{}
	eventually(t, func() bool {
		err := c.Get(ctx, types.NamespacedName{Name: reg.Name}, latest)
		return err == nil
	}, "MemberCluster "+reg.Name+" to appear in cache")
	patch := client.MergeFrom(latest.DeepCopy())
	latest.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	latest.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
	latest.Status.CurrentVersions = currentVersions
	latest.Status.Health = kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true}
	if err := c.Status().Patch(ctx, latest, patch); err != nil {
		t.Fatalf("patch MemberCluster status %s: %v", reg.Name, err)
	}
}

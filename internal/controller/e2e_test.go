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

	// ── 1. Define version ────────────────────────────────────────────────────
	resolvedVersion := "172.17.0.1:5000/fleet-bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	appKey := "default"
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
				Mode: "pull", Backend: "flux",
				Pull: &kaprov1alpha1.PullConfig{Namespace: "flux-system", OCIRepository: "test-repo", KustomizationPath: "."},
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
				Mode: "pull", Backend: "flux",
				Pull: &kaprov1alpha1.PullConfig{Namespace: "flux-system", OCIRepository: "test-repo", KustomizationPath: "."},
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
					DependsOn: []kaprov1alpha1.StageDependency{{Stage: "dev"}},
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
			Version: resolvedVersion,
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

	// ── 7. Wait: at least one ReleaseTarget child is created ─
	eventually(t, func() bool {
		return len(listReleaseTargets(t, ctx, c, release.Name, release.Namespace)) > 0
	}, "at least one ReleaseTarget should be created")

	// ── 8. Keep MemberClusters fresh so Syncs can converge ──────────────────
	patchRegistrationConverged(t, ctx, c, devReg, versions)
	patchRegistrationConverged(t, ctx, c, prodReg, versions)

	// ── 9. Wait: Release reaches Complete ────────────────────────────────────
	// The full chain (dev stage + prod stage, each going through env FSM states)
	// can take >20s; use a longer deadline here.
	eventuallyLong(t, func() bool {
		// Log target phases to aid debugging.
		for _, target := range listReleaseTargets(t, ctx, c, release.Name, release.Namespace) {
			t.Logf("  Target %s/%s/%s phase=%s", target.Spec.PipelineRef, target.Spec.Stage, target.Spec.Target, target.Status.Phase)
		}
		r := getRelease(ctx, c, releaseKey)
		t.Logf("  Release phase=%s", r.Status.Phase)
		return r.Status.Phase == kaprov1alpha1.ReleasePhaseComplete
	}, "release should reach Complete — full chain Release→Env FSM→Converged→Complete")

	t.Logf("✅ E2E chain complete — Release %s reached Complete", release.Name)
}

// TestE2E_HaltPolicy_CancelsSiblingTarget verifies the halt policy end-to-end:
//
//  1. A Release with one stage targeting two clusters starts progressing.
//  2. One target's status is patched to Failed (simulating actuator failure).
//  3. ReleaseReconciler detects the failure and sets spec.cancelled=true on the
//     sibling ReleaseTarget (parent owns spec).
//  4. ReleaseTargetReconciler observes spec.cancelled and transitions the sibling
//     to Failed (child owns status).
//  5. Release reaches Failed.
//
// This test requires envtest because cancelPendingStageTargets uses field-indexed
// List + Update on cluster-scoped ReleaseTarget objects.
func TestE2E_HaltPolicy_CancelsSiblingTarget(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	// ── 1. Define version ───────────────────────────────────────────────────
	haltVersion := "172.17.0.1:5000/halt-bundle@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// ── 2. Create two MemberClusters matching the same stage ─────────────────
	mc1 := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "halt-cluster1-" + suffix,
			Labels: map[string]string{"tier": "halt", "halt-test": suffix},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Mode: "pull", Backend: "flux",
				Pull: &kaprov1alpha1.PullConfig{Namespace: "flux-system", OCIRepository: "test-repo", KustomizationPath: "."},
			},
		},
	}
	mc2 := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "halt-cluster2-" + suffix,
			Labels: map[string]string{"tier": "halt", "halt-test": suffix},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Mode: "pull", Backend: "flux",
				Pull: &kaprov1alpha1.PullConfig{Namespace: "flux-system", OCIRepository: "test-repo", KustomizationPath: "."},
			},
		},
	}
	mustCreate(t, ctx, c, mc1)
	mustCreate(t, ctx, c, mc2)
	// Don't converge either cluster — targets will stay in early FSM states.
	// We'll patch one target to Failed manually after it's created.

	// ── 3. Create Pipeline: one stage, default onFailure (halt) ──────────────
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-pipeline-" + suffix},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     "deploy",
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"halt-test": suffix}},
				// OnFailure not set → defaults to halt
			}},
		},
	}
	mustCreate(t, ctx, c, pipeline)

	// ── 4. Create Release ────────────────────────────────────────────────────
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-release-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: haltVersion,
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: pipeline.Name},
			},
		},
	}
	mustCreate(t, ctx, c, release)

	releaseKey := types.NamespacedName{Name: release.Name, Namespace: ns}

	// ── 5. Wait for both ReleaseTargets to be created ────────────────────────
	eventually(t, func() bool {
		targets := listReleaseTargets(t, ctx, c, release.Name, ns)
		return len(targets) >= 2
	}, "two ReleaseTargets should be created")

	// ── 6. Patch one ReleaseTarget status to Failed (simulate failure)
	// Wait a moment for the controller to process, then patch.
	time.Sleep(500 * time.Millisecond)
	targets := listReleaseTargets(t, ctx, c, release.Name, ns)
	var victim *kaprov1alpha1.ReleaseTarget
	for i := range targets {
		if targets[i].Spec.Target == mc1.Name {
			victim = &targets[i]
			break
		}
	}
	if victim == nil {
		t.Fatal("could not find ReleaseTarget for " + mc1.Name)
	}
	patch := client.MergeFrom(victim.DeepCopy())
	victim.Status.Phase = kaprov1alpha1.TargetPhaseFailed
	victim.Status.Message = "deploy failed: simulated"
	victim.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := c.Status().Patch(ctx, victim, patch); err != nil {
		t.Fatalf("patch ReleaseTarget to Failed: %v", err)
	}
	t.Logf("patched %s to Failed", victim.Name)

	// Nudge the Release to trigger re-aggregation of child statuses.
	var rel kaprov1alpha1.Release
	if err := c.Get(ctx, releaseKey, &rel); err == nil {
		relPatch := client.MergeFrom(rel.DeepCopy())
		if rel.Annotations == nil {
			rel.Annotations = map[string]string{}
		}
		rel.Annotations["kapro.io/nudge"] = time.Now().UTC().Format(time.RFC3339Nano)
		_ = c.Patch(ctx, &rel, relPatch)
	}

	// ── 7. Wait: sibling ReleaseTarget gets spec.cancelled=true ──────────────
	eventually(t, func() bool {
		targets := listReleaseTargets(t, ctx, c, release.Name, ns)
		for _, rt := range targets {
			if rt.Spec.Target == mc2.Name && rt.Spec.Cancelled {
				return true
			}
		}
		return false
	}, "sibling ReleaseTarget should have spec.cancelled=true")

	// ── 8. Verify sibling's cancellation reason is set ───────────────────────
	cancelTargets := listReleaseTargets(t, ctx, c, release.Name, ns)
	for _, rt := range cancelTargets {
		if rt.Spec.Target == mc2.Name {
			if rt.Spec.CancelledReason == "" {
				t.Error("expected cancellation reason on cancelled sibling")
			}
			t.Logf("sibling %s cancelled: %s", rt.Name, rt.Spec.CancelledReason)
			break
		}
	}

	// ── 9. Wait: cancelled sibling transitions to Failed via ReleaseTargetReconciler
	eventually(t, func() bool {
		targets := listReleaseTargets(t, ctx, c, release.Name, ns)
		for _, rt := range targets {
			if rt.Spec.Target == mc2.Name {
				return rt.Status.Phase == kaprov1alpha1.TargetPhaseFailed
			}
		}
		return false
	}, "cancelled sibling should transition to Failed")

	// ── 10. Wait: Release reaches Failed ─────────────────────────────────────
	eventually(t, func() bool {
		r := getRelease(ctx, c, releaseKey)
		return r.Status.Phase == kaprov1alpha1.ReleasePhaseFailed
	}, "release should reach Failed after halt policy")

	// ── 11. Verify the trigger target is still Failed (not overwritten) ──────
	finalTargets := listReleaseTargets(t, ctx, c, release.Name, ns)
	for _, rt := range finalTargets {
		if rt.Spec.Target == mc1.Name {
			if rt.Status.Phase != kaprov1alpha1.TargetPhaseFailed {
				t.Errorf("trigger target phase changed to %s — expected Failed", rt.Status.Phase)
			}
			break
		}
	}

	t.Logf("halt policy verified: trigger=%s(Failed), sibling=%s(cancelled→Failed), Release=Failed",
		mc1.Name, mc2.Name)
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

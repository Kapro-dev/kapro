package controller_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// TestE2E_PromotionRun_Sync_Converged is the full integration test of
// the Kapro state machine chain:
//
//	PromotionRun → (Progressing) walks Plan DAG, creates Syncs per stage per env
//	       → Sync (dev stage) — fake actuator signals Converged
//	       → dev stage Complete; prod stage (dependsOn dev) starts
//	       → Syncs for prod stage Converge
//	       → Plan node reaches Complete
//	       → PromotionRun reaches Complete
//
// Requires KUBEBUILDER_ASSETS to be set — skipped otherwise.
func TestE2E_PromotionRun_Sync_Converged(t *testing.T) {
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	// ── 1. Define version ────────────────────────────────────────────────────
	resolvedVersion := "172.17.0.1:5000/fleet-bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	appKey := "default"
	versions := map[string]string{appKey: resolvedVersion}

	// ── 2 + 3. Create FleetClusters with tier labels and live heartbeat ─────
	// FleetCluster.Name must match Sync.Spec.Target (looked up by name).
	// Tier labels are used by promotionplan stage selectors.
	// SyncReconciler.handlePending checks LastHeartbeat freshness.
	// handleApplying checks CurrentVersions[appKey] == sync.Spec.Version.
	devReg := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-dev-" + suffix,
			Labels: map[string]string{"tier": "dev", "e2e": suffix},
		},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{
				Mode: "pull", Ref: "flux",
				Parameters: map[string]string{"namespace": "flux-system", "ociRepository": "test-repo"},
			},
		},
	}
	mustCreate(t, ctx, c, devReg)
	patchRegistrationConverged(t, ctx, c, devReg, versions)

	prodReg := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "e2e-prod-" + suffix,
			Labels: map[string]string{"tier": "prod", "e2e": suffix},
		},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{
				Mode: "pull", Ref: "flux",
				Parameters: map[string]string{"namespace": "flux-system", "ociRepository": "test-repo"},
			},
		},
	}
	mustCreate(t, ctx, c, prodReg)
	patchRegistrationConverged(t, ctx, c, prodReg, versions)

	// ── 4. Create Plan ────────────────────────────────────────────────────
	// Two stages: "dev" runs first, "prod" depends on it.
	promotionplan := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-promotionplan-" + suffix},
		Spec: kaprov1alpha1.PlanSpec{
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
	mustCreate(t, ctx, c, promotionplan)

	// ── 5. Create PromotionRun ─────────────────────────────────────────────────────
	// Single promotionplan node referencing the promotionplan CRD.
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-promotionrun-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.PromotionRunSpec{
			Version: resolvedVersion,
			Plans: []kaprov1alpha1.PlanRef{
				{
					Name: "initial",
					Plan: promotionplan.Name,
				},
			},
		},
	}
	mustCreate(t, ctx, c, promotionrun)

	promotionrunKey := types.NamespacedName{Name: promotionrun.Name, Namespace: ns}

	// ── 6. Wait: PromotionRun leaves Pending ──────────────────────────────────────
	eventually(t, func() bool {
		r := getPromotionRun(ctx, c, promotionrunKey)
		return r.Status.Phase != "" && r.Status.Phase != kaprov1alpha1.PromotionRunPhasePending
	}, "promotionrun should leave Pending")
	t.Logf("promotionrun phase after Pending: %s", getPromotionRun(ctx, c, promotionrunKey).Status.Phase)

	// ── 7. Wait: at least one PromotionTarget child is created ─
	eventually(t, func() bool {
		return len(listPromotionTargets(t, ctx, c, promotionrun.Name, promotionrun.Namespace)) > 0
	}, "at least one PromotionTarget should be created")

	// ── 8. Keep FleetClusters fresh so Syncs can converge ──────────────────
	patchRegistrationConverged(t, ctx, c, devReg, versions)
	patchRegistrationConverged(t, ctx, c, prodReg, versions)

	// ── 9. Wait: PromotionRun reaches Complete ────────────────────────────────────
	// The full chain (dev stage + prod stage, each going through env FSM states)
	// can take >20s; use a longer deadline here.
	eventuallyLong(t, func() bool {
		// Log target phases to aid debugging.
		for _, target := range listPromotionTargets(t, ctx, c, promotionrun.Name, promotionrun.Namespace) {
			t.Logf("  Target %s/%s/%s phase=%s", target.Spec.PlanRef, target.Spec.Stage, target.Spec.Target, target.Status.Phase)
		}
		r := getPromotionRun(ctx, c, promotionrunKey)
		t.Logf("  PromotionRun phase=%s", r.Status.Phase)
		return r.Status.Phase == kaprov1alpha1.PromotionRunPhaseComplete
	}, "promotionrun should reach Complete — full chain PromotionRun→Env FSM→Converged→Complete")

	t.Logf("✅ E2E chain complete — PromotionRun %s reached Complete", promotionrun.Name)
}

// TestE2E_HaltPolicy_CancelsSiblingTarget verifies the halt policy end-to-end:
//
//  1. A PromotionRun with one stage targeting two clusters starts progressing.
//  2. One target's status is patched to Failed (simulating actuator failure).
//  3. PromotionRunReconciler detects the failure and sets spec.cancelled=true on the
//     sibling PromotionTarget (parent owns spec).
//  4. TargetReconciler observes spec.cancelled and transitions the sibling
//     to Failed (child owns status).
//  5. PromotionRun reaches Failed.
//
// This test requires envtest because cancelPendingStageTargets uses field-indexed
// List + Update on cluster-scoped PromotionTarget objects.
func TestE2E_HaltPolicy_CancelsSiblingTarget(t *testing.T) {
	if os.Getenv("KAPRO_RUN_HALT_POLICY_E2E") != "1" {
		t.Skip("skipped by default: set KAPRO_RUN_HALT_POLICY_E2E=1 to run the flaky halt-policy envtest")
	}
	ctx, cancel, c := setupEnv(t)
	defer cancel()

	ns := "default"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	// ── 1. Define version ───────────────────────────────────────────────────
	haltVersion := "172.17.0.1:5000/halt-bundle@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// ── 2. Create two FleetClusters matching the same stage ─────────────────
	mc1 := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "halt-cluster1-" + suffix,
			Labels: map[string]string{"tier": "halt", "halt-test": suffix},
		},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{
				Mode: "pull", Ref: "flux",
				Parameters: map[string]string{"namespace": "flux-system", "ociRepository": "test-repo"},
			},
		},
	}
	mc2 := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "halt-cluster2-" + suffix,
			Labels: map[string]string{"tier": "halt", "halt-test": suffix},
		},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{
				Mode: "pull", Ref: "flux",
				Parameters: map[string]string{"namespace": "flux-system", "ociRepository": "test-repo"},
			},
		},
	}
	mustCreate(t, ctx, c, mc1)
	mustCreate(t, ctx, c, mc2)
	// Don't converge either cluster — targets will stay in early FSM states.
	// We'll patch one target to Failed manually after it's created.

	// ── 3. Create Plan: one stage, default onFailure (halt) ──────────────
	promotionplan := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-promotionplan-" + suffix},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     "deploy",
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"halt-test": suffix}},
				// OnFailure not set → defaults to halt
			}},
		},
	}
	mustCreate(t, ctx, c, promotionplan)

	// ── 4. Create PromotionRun ────────────────────────────────────────────────────
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-promotionrun-" + suffix, Namespace: ns},
		Spec: kaprov1alpha1.PromotionRunSpec{
			Version: haltVersion,
			Plans: []kaprov1alpha1.PlanRef{
				{Name: "initial", Plan: promotionplan.Name},
			},
		},
	}
	mustCreate(t, ctx, c, promotionrun)

	promotionrunKey := types.NamespacedName{Name: promotionrun.Name, Namespace: ns}

	// ── 5. Wait for both PromotionTargets to be created ────────────────────────
	eventually(t, func() bool {
		targets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
		return len(targets) >= 2
	}, "two PromotionTargets should be created")

	// ── 6. Patch one PromotionTarget status to Failed (simulate failure)
	// Wait a moment for the controller to process, then patch.
	time.Sleep(500 * time.Millisecond)
	targets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
	var victim *kaproruntimev1alpha1.Target
	for i := range targets {
		if targets[i].Spec.Target == mc1.Name {
			victim = &targets[i]
			break
		}
	}
	if victim == nil {
		t.Fatal("could not find PromotionTarget for " + mc1.Name)
	}
	patch := client.MergeFrom(victim.DeepCopy())
	victim.Status.Phase = kaprov1alpha1.TargetPhaseFailed
	victim.Status.Message = "deploy failed: simulated"
	victim.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := c.Status().Patch(ctx, victim, patch); err != nil {
		t.Fatalf("patch PromotionTarget to Failed: %v", err)
	}
	t.Logf("patched %s to Failed", victim.Name)

	// Nudge the PromotionRun to trigger re-aggregation of child statuses.
	var rel kaproruntimev1alpha1.PromotionRun
	if err := c.Get(ctx, promotionrunKey, &rel); err == nil {
		relPatch := client.MergeFrom(rel.DeepCopy())
		if rel.Annotations == nil {
			rel.Annotations = map[string]string{}
		}
		rel.Annotations["kapro.io/nudge"] = time.Now().UTC().Format(time.RFC3339Nano)
		_ = c.Patch(ctx, &rel, relPatch)
	}

	// ── 7. Wait: sibling PromotionTarget gets spec.cancelled=true ──────────────
	eventually(t, func() bool {
		targets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
		for _, rt := range targets {
			if rt.Spec.Target == mc2.Name && rt.Spec.Cancelled {
				return true
			}
		}
		return false
	}, "sibling PromotionTarget should have spec.cancelled=true")

	// ── 8. Verify sibling's cancellation reason is set ───────────────────────
	cancelTargets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
	for _, rt := range cancelTargets {
		if rt.Spec.Target == mc2.Name {
			if rt.Spec.CancelledReason == "" {
				t.Error("expected cancellation reason on cancelled sibling")
			}
			t.Logf("sibling %s cancelled: %s", rt.Name, rt.Spec.CancelledReason)
			break
		}
	}

	// ── 9. Wait: cancelled sibling transitions to Failed via TargetReconciler
	eventually(t, func() bool {
		targets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
		for _, rt := range targets {
			if rt.Spec.Target == mc2.Name {
				return rt.Status.Phase == kaprov1alpha1.TargetPhaseFailed
			}
		}
		return false
	}, "cancelled sibling should transition to Failed")

	// ── 10. Wait: PromotionRun reaches Failed ─────────────────────────────────────
	eventually(t, func() bool {
		r := getPromotionRun(ctx, c, promotionrunKey)
		return r.Status.Phase == kaprov1alpha1.PromotionRunPhaseFailed
	}, "promotionrun should reach Failed after halt policy")

	// ── 11. Verify the trigger target is still Failed (not overwritten) ──────
	finalTargets := listPromotionTargets(t, ctx, c, promotionrun.Name, ns)
	for _, rt := range finalTargets {
		if rt.Spec.Target == mc1.Name {
			if rt.Status.Phase != kaprov1alpha1.TargetPhaseFailed {
				t.Errorf("trigger target phase changed to %s — expected Failed", rt.Status.Phase)
			}
			break
		}
	}

	t.Logf("halt policy verified: trigger=%s(Failed), sibling=%s(cancelled→Failed), PromotionRun=Failed",
		mc1.Name, mc2.Name)
}

// patchRegistrationConverged sets a fresh heartbeat + Converged phase on a
// FleetCluster, simulating what cluster-controller writes after deployment.
// currentVersions should map appKey→version for all syncs that should converge.
//
// Sets conditions[Ready]=True too — that is what
// ClusterHeartbeatReconciler writes for a healthy pull-mode cluster and
// what TargetReconciler.requireFreshHeartbeat now reads to decide
// whether to proceed. Without this, the e2e would defer indefinitely
// because no Ready condition is observed.
func patchRegistrationConverged(t *testing.T, ctx context.Context, c client.Client, reg *kaprov1alpha1.Cluster, currentVersions map[string]string) {
	t.Helper()
	// Retry Get — caching client may not have synced immediately after Create.
	latest := &kaprov1alpha1.Cluster{}
	eventually(t, func() bool {
		err := c.Get(ctx, types.NamespacedName{Name: reg.Name}, latest)
		return err == nil
	}, "FleetCluster "+reg.Name+" to appear in cache")
	patch := client.MergeFrom(latest.DeepCopy())
	latest.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	latest.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
	latest.Status.CurrentVersions = currentVersions
	latest.Status.Health = kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true}
	apimeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type:    kaprov1alpha1.ConditionTypeReady,
		Status:  metav1.ConditionTrue,
		Reason:  kaprov1alpha1.ReasonHeartbeatFresh,
		Message: "simulated by e2e patchRegistrationConverged",
	})
	if err := c.Status().Patch(ctx, latest, patch); err != nil {
		t.Fatalf("patch FleetCluster status %s: %v", reg.Name, err)
	}
}

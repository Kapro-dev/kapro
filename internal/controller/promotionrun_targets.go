package controller

// promotionrun_targets.go — target-lifecycle helpers for the PromotionRun
// controller. Extracted from promotionrun_controller.go in D2-PR3 as part
// of the decomposition the audit flagged: the 2000+ LoC monolith mixed
// FSM dispatch (now in buildRunFSM), DAG resolution, and target lifecycle.
// This file holds the target-lifecycle slice — every method that creates,
// upserts, cancels, persists, or loads a PromotionTarget child object,
// plus the rollback-target trigger and the active-rollback predicate.
//
// File-move only, no signature changes. Methods remain on
// PromotionRunReconciler so existing call sites compile unchanged.

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func (r *PromotionRunReconciler) upsertTarget(
	promotionrun *kaprov1alpha2.PromotionRun,
	promotionplanRefName string,
	promotionplan *kaprov1alpha2.Plan,
	stage kaprov1alpha2.Stage,
	mc kaprov1alpha2.Cluster,
	resolvedGate *kaprov1alpha2.GatePolicySpec,
) (int, error) {
	desiredVersions := promotionrunDesiredVersions(promotionrun)
	version, appKey := primaryDesiredVersion(desiredVersions, promotionrun.Status.ResolvedVersion, promotionrunAppKey(promotionrun))
	key := syncKey(promotionplanRefName, stage.Name, mc.Name)
	for i, target := range promotionrun.Status.Targets {
		if syncKey(target.PlanRef, target.Stage, target.Target) == key {
			target := &promotionrun.Status.Targets[i]
			target.Version = version
			target.Gate = resolvedGate
			target.AppKey = appKey
			target.DesiredVersions = copyStringMap(desiredVersions)
			return i, nil
		}
	}
	newTarget := kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: promotionrun.Name,
		Target:          mc.Name,
		PlanRef:         promotionplanRefName,
		Plan:            promotionplan.Name,
		Stage:           stage.Name,
		Version:         version,
		Gate:            resolvedGate,
		AppKey:          appKey,
		DesiredVersions: copyStringMap(desiredVersions),
	}
	promotionrun.Status.Targets = append(promotionrun.Status.Targets, newTarget)
	return len(promotionrun.Status.Targets) - 1, nil
}

func (r *PromotionRunReconciler) triggerRollbackTargets(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, promotionplanRefName string, promotionplan *kaprov1alpha2.Plan, stageName string) {
	eligibleStages := make(map[string]struct{}, len(promotionplan.Spec.Stages))
	for _, stage := range promotionplan.Spec.Stages {
		eligibleStages[stage.Name] = struct{}{}
		if stage.Name == stageName {
			break
		}
	}
	n := len(promotionrun.Status.Targets) // capture length before appending
	for i := 0; i < n; i++ {
		target := &promotionrun.Status.Targets[i]
		if target.PlanRef != promotionplanRefName {
			continue
		}
		if _, ok := eligibleStages[target.Stage]; !ok {
			continue
		}
		if target.Phase != kaprov1alpha2.TargetPhaseConverged {
			continue
		}
		r.triggerTargetRollback(ctx, promotionrun, i)
	}
}

func (r *PromotionRunReconciler) hasActiveRollbackTargets(promotionrun *kaprov1alpha2.PromotionRun) bool {
	for _, target := range promotionrun.Status.Targets {
		if !target.Rollback {
			continue
		}
		switch target.Phase {
		case kaprov1alpha2.TargetPhaseConverged, kaprov1alpha2.TargetPhaseFailed, kaprov1alpha2.TargetPhaseSkipped:
			continue
		default:
			return true
		}
	}
	return false
}

func (r *PromotionRunReconciler) cancelPendingStageTargets(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, promotionplanRefName, stageName string) {
	log := log.FromContext(ctx)

	// List PromotionTarget objects for this promotionrun (indexed, not full scan).
	var list kaprov1alpha2.TargetList
	if err := r.List(ctx, &list, client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name}); err != nil {
		log.Error(err, "cancel: failed to list PromotionTargets")
		return
	}

	for i := range list.Items {
		rt := &list.Items[i]
		if rt.Spec.PlanRef != promotionplanRefName || rt.Spec.Stage != stageName {
			continue
		}
		// Skip terminal targets.
		switch rt.Status.Phase {
		case kaprov1alpha2.TargetPhaseConverged, kaprov1alpha2.TargetPhaseFailed, kaprov1alpha2.TargetPhaseSkipped:
			continue
		}
		if rt.Spec.Cancelled {
			continue
		}

		// Signal cancellation via spec — the child reconciler observes this
		// and transitions status to Failed on its next reconcile.
		// Use a raw JSON merge patch to set spec.cancelled directly, avoiding
		// resourceVersion conflicts with concurrent status writes.
		rawPatch := client.RawPatch(types.MergePatchType,
			[]byte(`{"spec":{"cancelled":true,"cancelledReason":"stage halted due to peer failure (failurePolicy: halt)"}}`))
		if err := r.Patch(ctx, rt, rawPatch); err != nil {
			log.Error(err, "cancel: failed to patch PromotionTarget spec", "name", rt.Name)
			continue
		}
		log.Info("cancel: signalled cancellation", "target", rt.Name)

		// Also update inline targets for immediate aggregation so the parent
		// can compute the correct PromotionRun phase without waiting for child reconcile.
		for j := range promotionrun.Status.Targets {
			t := &promotionrun.Status.Targets[j]
			if t.Target == rt.Spec.Target && t.PlanRef == promotionplanRefName && t.Stage == stageName {
				t.Phase = kaprov1alpha2.TargetPhaseFailed
				t.Message = "cancelled: " + rt.Spec.CancelledReason
				break
			}
		}
	}
}

func (r *PromotionRunReconciler) clearActivePromotionRun(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) {
	log := log.FromContext(ctx)
	if len(promotionrun.Status.Targets) == 0 {
		if err := r.loadPromotionTargets(ctx, promotionrun); err != nil {
			log.Error(err, "clearActivePromotionRun: failed to load promotion targets")
			return
		}
	}
	seen := make(map[string]bool)
	for _, target := range promotionrun.Status.Targets {
		mcName := target.Target
		if seen[mcName] {
			continue
		}
		seen[mcName] = true
		var mc kaprov1alpha2.Cluster
		if err := r.Get(ctx, client.ObjectKey{Name: mcName}, &mc); err != nil {
			continue
		}
		if mc.Status.ActivePromotionRun == promotionrun.Name {
			patch := client.MergeFrom(mc.DeepCopy())
			mc.Status.ActivePromotionRun = ""
			if err := r.Status().Patch(ctx, &mc, patch); err != nil {
				log.Error(err, "clearActivePromotionRun: failed to clear activePromotionRun", "cluster", mcName)
			}
		}
	}
}

func (r *PromotionRunReconciler) promotionTargetFromStatus(promotionrun *kaprov1alpha2.PromotionRun, target kaprov1alpha2.TargetExecutionState) *kaprov1alpha2.Target {
	rt := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name: promotionTargetObjectName(target),
			Labels: map[string]string{
				IndexKeyPromotionRun:     promotionrun.Name,
				"kapro.io/target":        target.Target,
				"kapro.io/promotionplan": target.PlanRef,
				"kapro.io/stage":         target.Stage,
			},
		},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: target.PromotionRunRef,
			Target:          target.Target,
			PlanRef:         target.PlanRef,
			Plan:            target.Plan,
			Stage:           target.Stage,
			Version:         target.Version,
			Gate:            target.Gate,
			AppKey:          target.AppKey,
			DesiredVersions: copyStringMap(target.DesiredVersions),
			Rollback:        target.Rollback,
		},
		Status: kaprov1alpha2.TargetStatus{TargetExecutionState: target},
	}
	if err := ctrl.SetControllerReference(promotionrun, rt, r.Scheme); err == nil {
		return rt
	}
	return rt
}

func (r *PromotionRunReconciler) loadPromotionTargets(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) error {
	var list kaprov1alpha2.TargetList
	if err := r.List(ctx, &list,
		client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name},
	); err != nil {
		return err
	}
	targets := make([]kaprov1alpha2.TargetExecutionState, 0, len(list.Items))
	for i := range list.Items {
		rt := &list.Items[i]
		targets = append(targets, targetStatusFromPromotionTarget(rt))
	}
	sort.Slice(targets, func(i, j int) bool {
		ai := promotionTargetObjectName(targets[i])
		aj := promotionTargetObjectName(targets[j])
		return ai < aj
	})
	promotionrun.Status.Targets = targets
	return nil
}

func (r *PromotionRunReconciler) persistPromotionTargets(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) error {
	var existingList kaprov1alpha2.TargetList
	if err := r.List(ctx, &existingList,
		client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name},
	); err != nil {
		return err
	}
	existing := make(map[string]*kaprov1alpha2.Target, len(existingList.Items))
	for i := range existingList.Items {
		rt := existingList.Items[i]
		existing[rt.Name] = rt.DeepCopy()
	}

	for _, target := range promotionrun.Status.Targets {
		name := promotionTargetObjectName(target)
		desired := r.promotionTargetFromStatus(promotionrun, target)
		if _, ok := existing[name]; !ok {
			// Create new child — status starts empty, TargetReconciler will drive it.
			toCreate := desired.DeepCopy()
			toCreate.Status = kaprov1alpha2.TargetStatus{}
			if err := r.Create(ctx, toCreate); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create PromotionTarget %s: %w", name, err)
			}
		} else {
			// Update spec/labels/ownerRefs only — never touch status.
			// Skip the patch if nothing actually changed (avoids O(N) API writes
			// per reconcile when targets are stable).
			current := existing[name]
			if promotionTargetSpecEqual(current, desired) {
				continue
			}
			specPatch := client.MergeFrom(current.DeepCopy())
			current.Labels = desired.Labels
			current.Spec = desired.Spec
			current.OwnerReferences = desired.OwnerReferences
			if err := r.Patch(ctx, current, specPatch); err != nil {
				return fmt.Errorf("patch PromotionTarget %s: %w", name, err)
			}
		}
	}
	return nil
}

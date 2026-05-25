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

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func (r *PromotionRunReconciler) upsertTarget(
	ctx context.Context,
	targets *[]kaprov1alpha1.TargetExecutionState,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	promotionplanRefName string,
	promotionplan *kaprov1alpha1.Plan,
	stage kaprov1alpha1.Stage,
	mc kaprov1alpha1.Cluster,
	resolvedGate *kaprov1alpha1.GatePolicySpec,
) (int, error) {
	desiredVersions := promotionrunDesiredVersions(promotionrun)
	version, appKey := primaryDesiredVersion(desiredVersions, promotionrun.Status.ResolvedVersion, promotionrunAppKey(promotionrun))
	key := syncKey(promotionplanRefName, stage.Name, mc.Name)
	for i, target := range *targets {
		if syncKey(target.PlanRef, target.Stage, target.Target) == key {
			target := &(*targets)[i]
			target.Version = version
			target.Gate = resolvedGate
			target.AppKey = appKey
			target.DesiredVersions = copyStringMap(desiredVersions)
			return i, nil
		}
	}
	newTarget := kaprov1alpha1.TargetExecutionState{
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
	*targets = append(*targets, newTarget)
	r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         promotionplanRefName,
		Stage:        stage.Name,
		Target:       mc.Name,
		EventType:    kaproruntimev1alpha1.DecisionTraceEventBatchProgress,
		Source:       "promotionrun-controller",
		Phase:        "Bind",
		Reason:       "TargetBound",
		Message:      fmt.Sprintf("target %s bound to stage %s", mc.Name, stage.Name),
		Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{{
			Type:   "target-bind",
			Source: "promotionrun-controller",
			Detail: targetBindDecisionEvidence(newTarget),
		}},
	})
	return len(*targets) - 1, nil
}

func (r *PromotionRunReconciler) triggerRollbackTargets(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun, targets *[]kaprov1alpha1.TargetExecutionState, promotionplanRefName string, promotionplan *kaprov1alpha1.Plan, stageName string) {
	eligibleStages := make(map[string]struct{}, len(promotionplan.Spec.Stages))
	for _, stage := range promotionplan.Spec.Stages {
		eligibleStages[stage.Name] = struct{}{}
		if stage.Name == stageName {
			break
		}
	}
	n := len(*targets) // capture length before appending
	for i := 0; i < n; i++ {
		target := &(*targets)[i]
		if target.PlanRef != promotionplanRefName {
			continue
		}
		if _, ok := eligibleStages[target.Stage]; !ok {
			continue
		}
		if target.Phase != kaprov1alpha1.TargetPhaseConverged {
			continue
		}
		r.triggerTargetRollback(ctx, promotionrun, targets, i)
	}
}

func hasActiveRollbackTargets(targets []kaprov1alpha1.TargetExecutionState) bool {
	for _, target := range targets {
		if !target.Rollback {
			continue
		}
		switch target.Phase {
		case kaprov1alpha1.TargetPhaseConverged, kaprov1alpha1.TargetPhaseFailed, kaprov1alpha1.TargetPhaseSkipped:
			continue
		default:
			return true
		}
	}
	return false
}

func (r *PromotionRunReconciler) cancelPendingStageTargets(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun, targets []kaprov1alpha1.TargetExecutionState, promotionplanRefName, stageName string) {
	log := log.FromContext(ctx)

	// List PromotionTarget objects for this promotionrun (indexed, not full scan).
	var list kaproruntimev1alpha1.TargetList
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
		case kaprov1alpha1.TargetPhaseConverged, kaprov1alpha1.TargetPhaseFailed, kaprov1alpha1.TargetPhaseSkipped:
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
			[]byte(`{"spec":{"cancelled":true,"cancelledReason":"stage halted due to peer failure (failurePolicy: halt)","cancelledPhase":"Failed"}}`))
		if err := r.Patch(ctx, rt, rawPatch); err != nil {
			log.Error(err, "cancel: failed to patch PromotionTarget spec", "name", rt.Name)
			continue
		}
		log.Info("cancel: signalled cancellation", "target", rt.Name)
		r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
			PromotionRun: promotionrun.Name,
			Plan:         promotionplanRefName,
			Stage:        stageName,
			Target:       rt.Spec.Target,
			EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
			Source:       "promotionrun-controller",
			Phase:        string(kaprov1alpha1.TargetPhaseFailed),
			Reason:       "StageHalted",
			Message:      "stage halted due to peer failure (failurePolicy: halt)",
		})

		// Also update inline targets for immediate aggregation so the parent
		// can compute the correct PromotionRun phase without waiting for child reconcile.
		for j := range targets {
			t := &targets[j]
			if t.Target == rt.Spec.Target && t.PlanRef == promotionplanRefName && t.Stage == stageName {
				t.Phase = kaprov1alpha1.TargetPhaseFailed
				t.Message = "cancelled: " + rt.Spec.CancelledReason
				break
			}
		}
	}
}

func (r *PromotionRunReconciler) markFailedStageTargetsSkipped(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun, promotionplanRefName, stageName string) error {
	var list kaproruntimev1alpha1.TargetList
	if err := r.List(ctx, &list, client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name}); err != nil {
		return err
	}
	for i := range list.Items {
		rt := &list.Items[i]
		if rt.Spec.PlanRef != promotionplanRefName || rt.Spec.Stage != stageName {
			continue
		}
		if rt.Status.Phase != kaprov1alpha1.TargetPhaseFailed {
			continue
		}
		if rt.Spec.Cancelled && rt.Spec.CancelledPhase == kaprov1alpha1.TargetPhaseSkipped {
			continue
		}
		rawPatch := client.RawPatch(types.MergePatchType,
			[]byte(`{"spec":{"cancelled":true,"cancelledReason":"skipped after failure policy","cancelledPhase":"Skipped"}}`))
		if err := r.Patch(ctx, rt, rawPatch); err != nil {
			return fmt.Errorf("mark failed PromotionTarget %s skipped: %w", rt.Name, err)
		}
		r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
			PromotionRun: promotionrun.Name,
			Plan:         promotionplanRefName,
			Stage:        stageName,
			Target:       rt.Spec.Target,
			EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
			Source:       "promotionrun-controller",
			Phase:        string(kaprov1alpha1.TargetPhaseSkipped),
			Reason:       "SkippedAfterFailurePolicy",
			Message:      "skipped after failure policy",
		})
	}
	return nil
}

func (r *PromotionRunReconciler) cancelPromotionRunTargets(ctx context.Context, promotionrunName, reason string) error {
	return r.cancelPromotionRunTargetsWithTraceReason(ctx, promotionrunName, reason, "PromotionRunTimeoutCancelled")
}

func (r *PromotionRunReconciler) cancelPromotionRunTargetsWithTraceReason(ctx context.Context, promotionrunName, reason, traceReason string) error {
	var list kaproruntimev1alpha1.TargetList
	if err := r.List(ctx, &list, client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrunName}); err != nil {
		return err
	}
	for i := range list.Items {
		rt := &list.Items[i]
		switch rt.Status.Phase {
		case kaprov1alpha1.TargetPhaseConverged, kaprov1alpha1.TargetPhaseFailed, kaprov1alpha1.TargetPhaseSkipped:
			continue
		}
		if rt.Spec.Cancelled {
			continue
		}
		rawPatch := client.RawPatch(types.MergePatchType,
			[]byte(fmt.Sprintf(`{"spec":{"cancelled":true,"cancelledReason":%q,"cancelledPhase":"Failed"}}`, reason)))
		if err := r.Patch(ctx, rt, rawPatch); err != nil {
			return fmt.Errorf("cancel PromotionTarget %s: %w", rt.Name, err)
		}
		r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
			PromotionRun: promotionrunName,
			Plan:         rt.Spec.PlanRef,
			Stage:        rt.Spec.Stage,
			Target:       rt.Spec.Target,
			EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
			Source:       "promotionrun-controller",
			Phase:        string(kaprov1alpha1.TargetPhaseFailed),
			Reason:       traceReason,
			Message:      reason,
			Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{{
				Type:   "target-cancel",
				Source: "promotionrun-controller",
				Detail: map[string]string{
					"cancelledPhase":  string(kaprov1alpha1.TargetPhaseFailed),
					"cancelledReason": reason,
					"fromPhase":       string(rt.Status.Phase),
				},
			}},
		})
	}
	return nil
}

func targetBindDecisionEvidence(target kaprov1alpha1.TargetExecutionState) map[string]string {
	detail := map[string]string{}
	addDetail(detail, "plan", target.PlanRef)
	addDetail(detail, "stage", target.Stage)
	addDetail(detail, "target", target.Target)
	addDetail(detail, "version", target.Version)
	addDetail(detail, "appKey", target.AppKey)
	addDetail(detail, "desiredVersionCount", fmt.Sprint(len(target.DesiredVersions)))
	return detail
}

func (r *PromotionRunReconciler) clearActivePromotionRun(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun, targets []kaprov1alpha1.TargetExecutionState) {
	log := log.FromContext(ctx)
	if len(targets) == 0 {
		loadedTargets, err := r.loadPromotionTargets(ctx, promotionrun)
		if err != nil {
			log.Error(err, "clearActivePromotionRun: failed to load promotion targets")
			return
		}
		targets = loadedTargets
	}
	seen := make(map[string]bool)
	for _, target := range targets {
		mcName := target.Target
		if seen[mcName] {
			continue
		}
		seen[mcName] = true
		var mc kaprov1alpha1.Cluster
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

func (r *PromotionRunReconciler) promotionTargetFromStatus(promotionrun *kaproruntimev1alpha1.PromotionRun, target kaprov1alpha1.TargetExecutionState) *kaproruntimev1alpha1.Target {
	rt := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name: promotionTargetObjectName(target),
			Labels: map[string]string{
				IndexKeyPromotionRun:     promotionrun.Name,
				"kapro.io/target":        target.Target,
				"kapro.io/promotionplan": target.PlanRef,
				"kapro.io/stage":         target.Stage,
			},
		},
		Spec: kaprov1alpha1.TargetSpec{
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
		Status: kaprov1alpha1.TargetStatus{TargetExecutionState: target},
	}
	if promotionrun.Spec.DeliveryUnitRef != "" {
		rt.Labels[kaprov1alpha1.LabelUnit] = promotionrun.Spec.DeliveryUnitRef
	}
	if err := ctrl.SetControllerReference(promotionrun, rt, r.Scheme); err == nil {
		return rt
	}
	return rt
}

func (r *PromotionRunReconciler) loadPromotionTargets(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun) ([]kaprov1alpha1.TargetExecutionState, error) {
	var list kaproruntimev1alpha1.TargetList
	if err := r.List(ctx, &list,
		client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name},
	); err != nil {
		return nil, err
	}
	targets := make([]kaprov1alpha1.TargetExecutionState, 0, len(list.Items))
	for i := range list.Items {
		rt := &list.Items[i]
		targets = append(targets, targetStatusFromPromotionTarget(rt))
	}
	sort.Slice(targets, func(i, j int) bool {
		ai := promotionTargetObjectName(targets[i])
		aj := promotionTargetObjectName(targets[j])
		return ai < aj
	})
	return targets, nil
}

func (r *PromotionRunReconciler) persistPromotionTargets(ctx context.Context, promotionrun *kaproruntimev1alpha1.PromotionRun, targets []kaprov1alpha1.TargetExecutionState) error {
	var existingList kaproruntimev1alpha1.TargetList
	if err := r.List(ctx, &existingList,
		client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name},
	); err != nil {
		return err
	}
	existing := make(map[string]*kaproruntimev1alpha1.Target, len(existingList.Items))
	for i := range existingList.Items {
		rt := existingList.Items[i]
		existing[rt.Name] = rt.DeepCopy()
	}

	for _, target := range targets {
		name := promotionTargetObjectName(target)
		desired := r.promotionTargetFromStatus(promotionrun, target)
		if _, ok := existing[name]; !ok {
			// Create new child — status starts empty, TargetReconciler will drive it.
			toCreate := desired.DeepCopy()
			toCreate.Status = kaprov1alpha1.TargetStatus{}
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

package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PipelineReconciler maintains Pipeline.status from the set of Sync objects
// that reference this Pipeline.
//
// A Pipeline is a flat DAG of Stages. For each stage, ReleaseReconciler creates
// one Sync per matching Environment. This reconciler has NO side effects — it
// only writes Pipeline.status. All scheduling decisions are made by
// ReleaseReconciler; all per-environment gate evaluation is done by SyncReconciler.
type PipelineReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=syncs,verbs=get;list;watch

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pipeline kaprov1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List all Syncs that reference this Pipeline (by Pipeline CR name).
	// Syncs are labelled kapro.io/pipeline=<pipeline-name>.
	// Sync is cluster-scoped — do NOT filter by namespace.
	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.MatchingLabels{"kapro.io/pipeline": pipeline.Name},
		client.Limit(2000),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Syncs for pipeline %s: %w", pipeline.Name, err)
	}

	// Build stage-name → aggregate sync counts map.
	// A stage may have multiple Syncs (one per Environment × pipeline instance).
	// We report: a stage is Complete only if ALL its Syncs are Converged;
	// Failed if any Sync is Failed; otherwise Progressing/Pending.
	type stageSummary struct {
		total    int
		synced   int // Converged
		failed   int
	}
	summaryByStage := make(map[string]*stageSummary, len(pipeline.Spec.Stages))
	for _, s := range pipeline.Spec.Stages {
		summaryByStage[s.Name] = &stageSummary{}
	}
	for _, s := range syncList.Items {
		stageName := s.Labels["kapro.io/stage"]
		sum, known := summaryByStage[stageName]
		if !known {
			continue // orphan from a previous stage definition
		}
		sum.total++
		switch s.Status.Phase {
		case kaprov1alpha1.SyncPhaseConverged:
			sum.synced++
		case kaprov1alpha1.SyncPhaseFailed:
			sum.failed++
		}
	}

	// Build StageProgress in spec order.
	stages := pipeline.Spec.Stages
	progress := make([]kaprov1alpha1.StageProgressEntry, 0, len(stages))
	totalStages := len(stages)
	completedStages := 0
	anyFailed := false
	anyInProgress := false

	for _, stage := range stages {
		entry := kaprov1alpha1.StageProgressEntry{Name: stage.Name}
		sum := summaryByStage[stage.Name]
		switch {
		case sum.total == 0:
			entry.Phase = "Pending"
		case sum.failed > 0:
			entry.Phase = "Failed"
			anyFailed = true
		case sum.synced > 0 && sum.synced == sum.total:
			entry.Phase = "Complete"
			completedStages++
		default:
			entry.Phase = "Progressing"
			anyInProgress = true
		}
		progress = append(progress, entry)
	}

	// Derive overall pipeline phase.
	var phase string
	switch {
	case anyFailed:
		phase = "Failed"
	case completedStages == totalStages && totalStages > 0:
		phase = "Complete"
	case anyInProgress || completedStages > 0:
		phase = "Progressing"
	default:
		phase = "Pending"
	}

	// Find the active stage (first non-complete, non-pending stage).
	activeStage := ""
	for _, p := range progress {
		if p.Phase != "Complete" && p.Phase != "Pending" && p.Phase != "" {
			activeStage = p.Name
			break
		}
	}

	// Skip patch if nothing changed — avoids unnecessary status writes.
	existing := pipeline.Status
	if existing.Phase == phase &&
		existing.ActiveStage == activeStage &&
		existing.TotalStages == totalStages &&
		existing.CompletedStages == completedStages &&
		existing.ObservedGeneration == pipeline.Generation &&
		stageProgressEqual(existing.StageProgress, progress) {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status.Phase = phase
	pipeline.Status.ActiveStage = activeStage
	pipeline.Status.TotalStages = totalStages
	pipeline.Status.CompletedStages = completedStages
	pipeline.Status.StageProgress = progress
	pipeline.Status.ObservedGeneration = pipeline.Generation

	condStatus := metav1.ConditionFalse
	condReason := "Progressing"
	condMsg := fmt.Sprintf("%d/%d stages complete", completedStages, totalStages)
	if phase == "Complete" {
		condStatus = metav1.ConditionTrue
		condReason = "Complete"
		condMsg = "all stages complete"
	} else if phase == "Failed" {
		condReason = "Failed"
		condMsg = "one or more stages failed"
	}
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMsg,
		ObservedGeneration: pipeline.Generation,
	})

	if err := r.Status().Patch(ctx, &pipeline, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Pipeline %s status: %w", pipeline.Name, err)
	}

	log.Info("Pipeline status updated",
		"phase", phase,
		"completed", completedStages,
		"total", totalStages,
	)
	return ctrl.Result{}, nil
}

// stageProgressEqual returns true if two StageProgress slices are identical.
func stageProgressEqual(a, b []kaprov1alpha1.StageProgressEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha1.Pipeline{}).
		// Whenever a Sync referencing this Pipeline changes phase,
		// re-reconcile the Pipeline to update its status summary.
		Watches(
			&kaprov1alpha1.Sync{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				pipelineName := obj.GetLabels()["kapro.io/pipeline"]
				if pipelineName == "" {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: client.ObjectKey{Name: pipelineName, Namespace: obj.GetNamespace()},
				}}
			}),
		).
		Complete(r)
}

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PipelineReconciler maintains Pipeline.status from the set of owned BatchRun objects.
//
// Pipeline is the observable unit of progression: operators and dashboards read
// Pipeline.status.batchProgress to see "where is this rollout?" without needing
// to query BatchRun objects individually.
//
// This reconciler has NO side effects — it only writes Pipeline.status.
// All promotion/batch decisions are made by ReleaseReconciler and BatchRunReconciler.
type PipelineReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns,verbs=get;list;watch

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pipeline kaprov1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List all BatchRuns owned by this Pipeline (via ownerReference label).
	var brList kaprov1alpha1.BatchRunList
	if err := r.List(ctx, &brList,
		client.InNamespace(pipeline.Namespace),
		client.MatchingLabels{"kapro.io/pipeline": pipeline.Name},
		client.Limit(500),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list BatchRuns for pipeline %s: %w", pipeline.Name, err)
	}

	// Build a name→BatchRun map for O(1) lookup.
	brByName := make(map[string]*kaprov1alpha1.BatchRun, len(brList.Items))
	for i := range brList.Items {
		br := &brList.Items[i]
		brByName[br.Labels["kapro.io/batch"]] = br
	}

	// Build BatchProgress from Pipeline.spec.progression.batches order.
	batches := pipeline.Spec.Progression.Batches
	progress := make([]kaprov1alpha1.BatchProgressEntry, 0, len(batches))
	totalBatches := len(batches)
	completedBatches := 0
	anyFailed := false
	anyInProgress := false

	for _, batch := range batches {
		entry := kaprov1alpha1.BatchProgressEntry{Name: batch.Name}
		if br, ok := brByName[batch.Name]; ok {
			entry.Phase = string(br.Status.Phase)
			entry.BatchRunRef = br.Name
			switch br.Status.Phase {
			case kaprov1alpha1.BatchPhaseComplete:
				completedBatches++
			case kaprov1alpha1.BatchPhaseFailed:
				anyFailed = true
			default:
				// Pending, Resolving, WaitingPromotions, GateCheck, WaitingApproval
				anyInProgress = true
			}
		} else {
			entry.Phase = "Pending" // not yet created
		}
		progress = append(progress, entry)
	}

	// Derive overall pipeline phase.
	var phase string
	switch {
	case anyFailed:
		phase = "Failed"
	case completedBatches == totalBatches && totalBatches > 0:
		phase = "Complete"
	case anyInProgress || completedBatches > 0:
		phase = "Progressing"
	default:
		phase = "Pending"
	}

	// Find the active step (first non-complete batch).
	activeStep := ""
	for _, p := range progress {
		if p.Phase != "Complete" && p.Phase != "" {
			activeStep = p.Name
			break
		}
	}

	// Skip patch if nothing changed — avoids unnecessary status writes.
	existing := pipeline.Status
	if existing.Phase == phase &&
		existing.ActiveStep == activeStep &&
		existing.TotalBatches == totalBatches &&
		existing.CompletedBatches == completedBatches &&
		existing.ObservedGeneration == pipeline.Generation &&
		batchProgressEqual(existing.BatchProgress, progress) {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status.Phase = phase
	pipeline.Status.ActiveStep = activeStep
	pipeline.Status.TotalBatches = totalBatches
	pipeline.Status.CompletedBatches = completedBatches
	pipeline.Status.BatchProgress = progress
	pipeline.Status.ObservedGeneration = pipeline.Generation

	condStatus := metav1.ConditionFalse
	condReason := "Progressing"
	condMsg := fmt.Sprintf("%d/%d batches complete", completedBatches, totalBatches)
	if phase == "Complete" {
		condStatus = metav1.ConditionTrue
		condReason = "Complete"
		condMsg = "all batches complete"
	} else if phase == "Failed" {
		condReason = "Failed"
		condMsg = "one or more batches failed"
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
		"completed", completedBatches,
		"total", totalBatches,
	)
	return ctrl.Result{}, nil
}

// batchProgressEqual returns true if two BatchProgress slices are identical.
func batchProgressEqual(a, b []kaprov1alpha1.BatchProgressEntry) bool {
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
		// Whenever a BatchRun owned by this Pipeline changes phase,
		// re-reconcile the Pipeline to update its status summary.
		Owns(&kaprov1alpha1.BatchRun{}).
		Complete(r)
}

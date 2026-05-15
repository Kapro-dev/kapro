package controller

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	internalgate "kapro.io/kapro/internal/gate"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/planner"
)

// releaseFinalizer uses the canonical constant from the API package
// to avoid mismatch between controller and external tooling.
const releaseFinalizer = kaprov1alpha1.ReleaseFinalizer

const (
	maxGateRunsPerTarget       = 16
	maxGateResultsPerGateRun   = 16
	maxReleaseReadyMessageSize = 256
	maxPlannerResultsPerStage  = 32
)

// ReleaseReconciler is the main brain of Kapro.
// It drives two DAG levels:
//
//  1. Pipeline DAG — Release.spec.pipelines[].dependsOn orders which pipelines
//     run in sequence (or parallel when no deps). Useful when the same fleet is
//     partitioned into logical "apps" that must be released in a fixed order.
//
//  2. Stage DAG — Pipeline.spec.stages[].dependsOn orders stages within each
//     pipeline (pilot → canary → global). Each stage expands to N Syncs — one
//     per matching target — which run in parallel.
//
// State machine:
//
//	Pending → Progressing → Complete | Failed
type ReleaseReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	Scheme           *runtime.Scheme
	ActuatorRegistry *actuator.Registry
	Notifier         notification.Notifier
	ApprovalSecret   []byte
	ExternalURL      string

	// GateRegistry resolves every gate by name — both FSM-phase gates
	// ("soak", "metrics", "approval", "verification") and template-dispatch
	// gates (GateTemplate.spec.type). Never nil in production.
	GateRegistry *gate.Registry

	// ShardPredicate optionally filters objects by shard label for horizontal scaling.
	// When nil, all objects are processed.
	ShardPredicate predicate.Predicate

	// Planner orders and filters target clusters using scheduler-style extension phases.
	// Nil means the default empty planner.
	Planner *planner.Framework
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=releases/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *ReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	resultLabel := "success"
	defer func() {
		kaprometrics.ControllerReconciles.WithLabelValues("release", resultLabel).Inc()
		kaprometrics.ControllerReconcileDuration.WithLabelValues("release").Observe(time.Since(start).Seconds())
	}()

	log := log.FromContext(ctx)

	var release kaprov1alpha1.Release
	if err := r.Get(ctx, req.NamespacedName, &release); err != nil {
		resultLabel = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Release",
		"name", release.Name,
		"phase", release.Status.Phase,
		"version", release.Spec.Version,
	)

	if !release.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &release)
	}

	if !controllerutil.ContainsFinalizer(&release, releaseFinalizer) {
		patch := client.MergeFrom(release.DeepCopy())
		controllerutil.AddFinalizer(&release, releaseFinalizer)
		if err := r.Patch(ctx, &release, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if release.Spec.Suspended {
		log.Info("Release is suspended — skipping FSM advancement")
		patch := client.MergeFrom(release.DeepCopy())
		r.setReleaseReadyCondition(&release, metav1.ConditionFalse, "Suspended", "Release is suspended")
		r.setReconcilingCondition(&release, metav1.ConditionFalse, "Suspended", "Release is suspended")
		apimeta.RemoveStatusCondition(&release.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
		release.Status.ObservedGeneration = release.Generation
		if patchErr := r.patchReleaseStatus(ctx, &release, patch); patchErr != nil {
			resultLabel = "error"
			return ctrl.Result{}, fmt.Errorf("patch suspended conditions: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	switch release.Status.Phase {
	case "", kaprov1alpha1.ReleasePhasePending:
		return r.handlePending(ctx, &release)
	case kaprov1alpha1.ReleasePhaseProgressing:
		return r.handleProgressing(ctx, &release)
	case kaprov1alpha1.ReleasePhaseFailed:
		if r.hasActiveRollbackTargets(&release) {
			return r.handleFailed(ctx, &release)
		}
		return ctrl.Result{}, nil
	case kaprov1alpha1.ReleasePhaseComplete:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) patchReleaseStatus(ctx context.Context, release *kaprov1alpha1.Release, patch client.Patch) error {
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		kaprometrics.StatusWrites.WithLabelValues("release", "error").Inc()
		return err
	}
	kaprometrics.StatusWrites.WithLabelValues("release", "success").Inc()
	return nil
}

// handlePending validates the release revisions and transitions to Progressing.
func (r *ReleaseReconciler) handlePending(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	desiredVersions := releaseDesiredVersionsFromSpec(release)
	if len(desiredVersions) == 0 {
		patch := client.MergeFrom(release.DeepCopy())
		r.setReleaseReadyCondition(release, metav1.ConditionFalse, "NoVersion", "spec.version or spec.versions is required")
		r.setStalledCondition(release, "NoVersion", "spec.version or spec.versions is required")
		release.Status.ObservedGeneration = release.Generation
		if err := r.patchReleaseStatus(ctx, release, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch stalled: %w", err)
		}
		return ctrl.Result{}, nil
	}

	resolvedVersion := releasePrimaryVersion(release, desiredVersions)
	log.Info("version resolved", "version", resolvedVersion, "versions", len(desiredVersions))

	progress := make([]kaprov1alpha1.PipelineProgress, 0, len(release.Spec.Pipelines))
	for _, ref := range release.Spec.Pipelines {
		progress = append(progress, kaprov1alpha1.PipelineProgress{
			Name: ref.Name, Pipeline: ref.Pipeline, Phase: "Pending",
		})
	}

	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseProgressing
	release.Status.ResolvedVersion = resolvedVersion
	release.Status.PipelineProgress = progress
	release.Status.ObservedGeneration = release.Generation
	release.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Progressing", "release is advancing")
	r.clearStalledCondition(release)
	r.setReconcilingCondition(release, metav1.ConditionTrue, "Progressing", "advancing through pipeline DAG")
	pipelineNames := make([]string, 0, len(release.Spec.Pipelines))
	for _, p := range release.Spec.Pipelines {
		pipelineNames = append(pipelineNames, p.Pipeline)
	}
	r.Recorder.Eventf(release, corev1.EventTypeNormal, "Started",
		"release %s started: version=%s pipelines=%v", release.Name, resolvedVersion, pipelineNames)
	if err := r.patchReleaseStatus(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release phase: %w", err)
	}
	r.notifyReleaseEvent(ctx, release, notification.EventReleaseStarted, "release started")
	return ctrl.Result{Requeue: true}, nil
}

// handleProgressing drives the two-level DAG:
//
//	Pipeline DAG (outer) → Stage DAG (inner) → Targets per Stage
//
// For each pipeline whose dependencies are complete, we walk its stages in
// dependsOn order. For each eligible stage we list matching Targets,
// upsert an TargetStatus entry in release.Status.Targets, and
// observe current phases. advanceAllTargets then moves each non-terminal
// env one FSM step forward. A single Status().Patch() persists everything.
func (r *ReleaseReconciler) handleProgressing(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check global Release timeout — fail the entire Release if it exceeded.
	if release.Spec.Timeout != "" && release.Status.StartedAt != "" {
		timeout, err := time.ParseDuration(release.Spec.Timeout)
		if err == nil {
			startedAt, parseErr := time.Parse(time.RFC3339, release.Status.StartedAt)
			if parseErr == nil && time.Since(startedAt) > timeout {
				log.Info("Release exceeded timeout", "timeout", release.Spec.Timeout,
					"startedAt", release.Status.StartedAt, "elapsed", time.Since(startedAt))
				return r.handleTimeout(ctx, release)
			}
		}
	}

	// CRITICAL: take snapshot BEFORE any mutations to release.Status.
	// advanceAllTargets, upsertTarget, cancelPendingStageTargets, and
	// triggerRollbackTargets all mutate release.Status in-place; one patch at the
	// bottom persists the full diff.
	patch := client.MergeFrom(release.DeepCopy())

	if err := r.loadReleaseTargets(ctx, release); err != nil {
		return ctrl.Result{}, fmt.Errorf("load release targets: %w", err)
	}

	// Build pipeline phase map from current PipelineProgress.
	pipelinePhase := make(map[string]string, len(release.Status.PipelineProgress))
	for _, p := range release.Status.PipelineProgress {
		pipelinePhase[p.Name] = p.Phase
	}

	// Track updated progress (written back once at the end).
	updatedPipelines := make([]kaprov1alpha1.PipelineProgress, 0, len(release.Spec.Pipelines))
	allPipelinesComplete := true
	var failureMsg string
	var pendingCancels []cancelRequest
	var nextRequeue time.Duration

	for _, pipelineRef := range release.Spec.Pipelines {
		currentPhase := pipelinePhase[pipelineRef.Name]

		if currentPhase == "Complete" {
			updatedPipelines = append(updatedPipelines, kaprov1alpha1.PipelineProgress{
				Name: pipelineRef.Name, Pipeline: pipelineRef.Pipeline, Phase: "Complete",
			})
			continue
		}
		if currentPhase == "Failed" {
			allPipelinesComplete = false
			updatedPipelines = append(updatedPipelines, kaprov1alpha1.PipelineProgress{
				Name: pipelineRef.Name, Pipeline: pipelineRef.Pipeline, Phase: "Failed",
			})
			continue
		}

		// Check pipeline-level dependencies.
		depsComplete := true
		for _, dep := range pipelineRef.DependsOn {
			if pipelinePhase[dep] != "Complete" {
				depsComplete = false
				break
			}
		}
		if !depsComplete {
			allPipelinesComplete = false
			updatedPipelines = append(updatedPipelines, kaprov1alpha1.PipelineProgress{
				Name: pipelineRef.Name, Pipeline: pipelineRef.Pipeline, Phase: "Pending",
			})
			continue
		}

		// Pipeline is eligible — resolve its stage DAG.
		var pipeline kaprov1alpha1.Pipeline
		if err := r.Get(ctx, client.ObjectKey{Name: pipelineRef.Pipeline}, &pipeline); err != nil {
			return ctrl.Result{}, fmt.Errorf("pipeline %s not found: %w", pipelineRef.Pipeline, err)
		}

		stageProgress, pipelineDone, pipelineFailed, requeueAfter, cancels, err := r.reconcilePipelineStages(
			ctx, release, pipelineRef.Name, &pipeline,
		)
		if err != nil {
			return ctrl.Result{}, err
		}
		pendingCancels = append(pendingCancels, cancels...)
		if requeueAfter > 0 && (nextRequeue == 0 || requeueAfter < nextRequeue) {
			nextRequeue = requeueAfter
		}

		newPhase := "Progressing"
		if pipelineFailed {
			newPhase = "Failed"
			allPipelinesComplete = false
			failureMsg = fmt.Sprintf("pipeline %s (%s) failed", pipelineRef.Name, pipelineRef.Pipeline)
		} else if pipelineDone {
			newPhase = "Complete"
			log.Info("pipeline complete", "pipelineRef", pipelineRef.Name)
		} else {
			allPipelinesComplete = false
		}

		// Derive active stage for quick "where are we?" in k9s.
		activeStage := ""
		for i := len(stageProgress) - 1; i >= 0; i-- {
			if stageProgress[i].Phase == "Progressing" || stageProgress[i].Phase == "Failed" {
				activeStage = stageProgress[i].Name
				break
			}
			if stageProgress[i].Phase == "Complete" && activeStage == "" {
				activeStage = stageProgress[i].Name
			}
		}

		updatedPipelines = append(updatedPipelines, kaprov1alpha1.PipelineProgress{
			Name:          pipelineRef.Name,
			Pipeline:      pipelineRef.Pipeline,
			Phase:         newPhase,
			ActiveStage:   activeStage,
			StageProgress: stageProgress,
		})

		if pipelineFailed {
			// Fail fast: mark release failed using the outer patch (which already
			// includes any target mutations from upsertTarget/cancelPendingStageTargets).
			release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
			release.Status.ObservedGeneration = release.Generation
			release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			release.Status.PipelineProgress = updatedPipelines
			release.Status.Report = r.computeReport(release)
			r.normalizeReleaseStatus(release)
			if err := r.persistReleaseTargets(ctx, release); err != nil {
				return ctrl.Result{}, fmt.Errorf("persist release targets: %w", err)
			}
			// Apply deferred cancellations AFTER persistReleaseTargets so the
			// cache-based spec writes don't overwrite spec.cancelled.
			for _, c := range pendingCancels {
				r.cancelPendingStageTargets(ctx, release, c.pipelineRef, c.stage)
			}
			hasRollbacks := r.hasActiveRollbackTargets(release)
			release.Status.Targets = nil
			r.setReleaseReadyCondition(release, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			r.setStalledCondition(release, "SubResourceFailed", failureMsg)
			if hasRollbacks {
				r.setReconcilingCondition(release, metav1.ConditionTrue, "RollbackInProgress", "release failed and rollback targets are still progressing")
			} else {
				r.setReconcilingCondition(release, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			}
			r.Recorder.Event(release, corev1.EventTypeWarning, "Failed", failureMsg)
			if patchErr := r.patchReleaseStatus(ctx, release, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch Release status on failure: %w", patchErr)
			}
			r.notifyReleaseEvent(ctx, release, notification.EventReleaseFailed, failureMsg)
			if hasRollbacks {
				return ctrl.Result{Requeue: true}, nil
			}
			r.clearActiveRelease(ctx, release)
			return ctrl.Result{}, nil
		}
	}

	// Child ReleaseTarget reconciles advance per-target FSM state; the Release
	// reconcile only persists orchestration-side mutations (upserts, cancels,
	// rollback target creation) and aggregates child state.
	release.Status.PipelineProgress = updatedPipelines
	release.Status.ObservedGeneration = release.Generation
	// Set terminal phase fields BEFORE computeReport so the report captures the
	// correct Phase and CompletedAt (B50: previously set after targets were cleared).
	if allPipelinesComplete {
		r.appendAuditEntry(ctx, release)
		release.Status.Phase = kaprov1alpha1.ReleasePhaseComplete
		release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Compute report while targets are still in memory; normalization and
	// persistence happen after so the report reflects the full target set.
	release.Status.Report = r.computeReport(release)
	r.normalizeReleaseStatus(release)
	if err := r.persistReleaseTargets(ctx, release); err != nil {
		return ctrl.Result{}, fmt.Errorf("persist release targets: %w", err)
	}
	for _, c := range pendingCancels {
		r.cancelPendingStageTargets(ctx, release, c.pipelineRef, c.stage)
	}
	release.Status.Targets = nil

	if allPipelinesComplete {
		r.setReleaseReadyCondition(release, metav1.ConditionTrue, "Complete", "all pipelines complete")
		r.clearStalledCondition(release)
		r.setReconcilingCondition(release, metav1.ConditionFalse, "Complete", "all pipelines complete")
		duration := ""
		if release.Status.StartedAt != "" {
			if startT, err := time.Parse(time.RFC3339, release.Status.StartedAt); err == nil {
				duration = time.Since(startT).Truncate(time.Second).String()
			}
		}
		r.Recorder.Eventf(release, corev1.EventTypeNormal, "Complete",
			"all pipelines complete: version=%s targets=%d duration=%s",
			release.Spec.Version, release.Status.Report.TotalTargets, duration)
	} else {
		r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Progressing", releaseProgressSummary(release))
		r.clearStalledCondition(release)
		r.setReconcilingCondition(release, metav1.ConditionTrue, "Progressing", "release is advancing through pipeline DAG")
	}
	if err := r.patchReleaseStatus(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release status: %w", err)
	}

	if allPipelinesComplete {
		r.notifyReleaseEvent(ctx, release, notification.EventReleaseCompleted, "release completed")
		r.clearActiveRelease(ctx, release)
		annPatch := client.MergeFrom(release.DeepCopy())
		if release.Annotations == nil {
			release.Annotations = make(map[string]string)
		}
		release.Annotations["kapro.io/previous-version"] = release.Status.ResolvedVersion
		if annErr := r.Patch(ctx, release, annPatch); annErr != nil {
			log.Error(annErr, "failed to annotate previous-version on Release")
		}
		log.Info("Release complete", "name", release.Name)
		if nextRequeue > 0 {
			return ctrl.Result{RequeueAfter: nextRequeue}, nil
		}
		return ctrl.Result{}, nil
	}
	// Not all pipelines complete — requeue as a safety net in case a
	// ReleaseTarget watch event is missed (cache lag, informer backpressure).
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *ReleaseReconciler) handleTimeout(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
	release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("release exceeded timeout (%s)", release.Spec.Timeout)
	r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Timeout", msg)
	r.setStalledCondition(release, "Timeout", msg)
	if err := r.patchReleaseStatus(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release status (timeout): %w", err)
	}
	r.Recorder.Eventf(release, corev1.EventTypeWarning, "Timeout", msg)
	r.notifyReleaseEvent(ctx, release, notification.EventReleaseFailed, msg)
	log.FromContext(ctx).Info(msg)
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) handleFailed(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	patch := client.MergeFrom(release.DeepCopy())

	if err := r.loadReleaseTargets(ctx, release); err != nil {
		return ctrl.Result{}, fmt.Errorf("load release targets: %w", err)
	}

	release.Status.ObservedGeneration = release.Generation
	release.Status.Report = r.computeReport(release)
	r.normalizeReleaseStatus(release)
	if err := r.persistReleaseTargets(ctx, release); err != nil {
		return ctrl.Result{}, fmt.Errorf("persist release targets: %w", err)
	}
	hasRollbacks := r.hasActiveRollbackTargets(release)
	release.Status.Targets = nil
	r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Failed", "release failed")

	if hasRollbacks {
		r.setReconcilingCondition(release, metav1.ConditionTrue, "RollbackInProgress", "release failed and rollback targets are still progressing")
		r.setStalledCondition(release, "Failed", "release failed and rollback is in progress")
	} else {
		r.setReconcilingCondition(release, metav1.ConditionFalse, "Failed", "release failed")
		r.setStalledCondition(release, "Failed", "release failed")
	}

	if err := r.patchReleaseStatus(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch failed Release status: %w", err)
	}

	if !hasRollbacks {
		r.clearActiveRelease(ctx, release)
	}
	return ctrl.Result{}, nil
}

// reconcilePipelineStages walks the stage DAG for one pipeline instance.
//
// For each stage whose dependencies are satisfied it:
//  1. Lists target clusters matching the stage selector.
//  2. Upserts a TargetStatus entry for each (idempotent).
//  3. Observes current target phases → derives stage phase.
//
// Returns (stageProgress, allComplete, anyFailed, error).
// cancelRequest records a stage that needs its pending targets cancelled after
// persistReleaseTargets has run (to avoid the cache overwriting the patch).
type cancelRequest struct {
	pipelineRef string
	stage       string
}

func (r *ReleaseReconciler) reconcilePipelineStages(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
) ([]kaprov1alpha1.StageProgress, bool, bool, time.Duration, []cancelRequest, error) {
	log := log.FromContext(ctx)

	// stagePhase maps stage name → "Pending"|"Progressing"|"Complete"|"Failed"
	stagePhase := make(map[string]string, len(pipeline.Spec.Stages))
	stageProgress := make([]kaprov1alpha1.StageProgress, 0, len(pipeline.Spec.Stages))

	allComplete := true
	anyFailed := false
	var nextRequeue time.Duration
	var cancels []cancelRequest

	for _, stage := range pipeline.Spec.Stages {
		// Check stage-level dependencies (with optional soak time and strategy).
		depsComplete := true
		for _, dep := range stage.DependsOn {
			satisfied, wait, err := r.stageDependencySatisfied(ctx, release, pipelineRefName, pipeline, dep)
			if err != nil {
				return nil, false, false, 0, nil, err
			}
			if !satisfied {
				depsComplete = false
				if wait > 0 && (nextRequeue == 0 || wait < nextRequeue) {
					nextRequeue = wait
				}
				break
			}
		}
		if !depsComplete {
			allComplete = false
			stagePhase[stage.Name] = "Pending"
			stageProgress = append(stageProgress, kaprov1alpha1.StageProgress{
				Name: stage.Name, Phase: "Pending",
			})
			continue
		}

		// Plan clusters matching this stage's selector.
		planned, err := r.planTargetsForStage(ctx, pipelineRefName, pipeline, stage, release)
		if err != nil {
			return nil, false, false, 0, nil, fmt.Errorf("list targets for stage %s: %w", stage.Name, err)
		}
		envList := planned.Targets
		if len(envList) == 0 {
			log.Info("stage has no matching clusters — treating as complete",
				"stage", stage.Name, "pipeline", pipeline.Name, "pipelineRef", pipelineRefName)
			stagePhase[stage.Name] = "Complete"
			stageProgress = append(stageProgress, kaprov1alpha1.StageProgress{
				Name: stage.Name, Phase: "Complete", Total: 0, PlannerResults: apiPlannerResults(planned.Decisions),
			})
			continue
		}

		bindTargets, deferred, strategyDecisions := r.applyStageStrategy(release, pipelineRefName, stage, envList)
		plannerResults := apiPlannerResults(append(planned.Decisions, strategyDecisions...))

		resolvedGate, err := resolveStageGate(pipeline, stage)
		if err != nil {
			return nil, false, false, 0, nil, err
		}

		// Upsert selected target entries; observe phases across the full planned set.
		for _, target := range bindTargets {
			i, err := r.upsertTarget(release, pipelineRefName, pipeline, stage, target, resolvedGate)
			if err != nil {
				return nil, false, false, 0, nil, err
			}
			_ = i
		}

		total, synced, failed := len(envList), 0, 0
		plannedNames := make(map[string]struct{}, len(envList))
		for _, target := range envList {
			plannedNames[target.Name] = struct{}{}
		}
		for _, target := range release.Status.Targets {
			if target.PipelineRef != pipelineRefName || target.Stage != stage.Name {
				continue
			}
			if _, ok := plannedNames[target.Target]; !ok {
				continue
			}
			switch target.Phase {
			case kaprov1alpha1.TargetPhaseConverged:
				synced++
			case kaprov1alpha1.TargetPhaseSkipped:
				// Skipped targets (onFailure=continue) are terminal — count them
				// as synced so the stage can complete instead of deadlocking.
				synced++
			case kaprov1alpha1.TargetPhaseFailed:
				failed++
			}
		}

		// Derive stage phase from target observations.
		var sp kaprov1alpha1.StageProgress
		sp.Name = stage.Name
		sp.Total = total
		sp.Synced = synced
		sp.Failed = failed
		sp.Deferred = deferred
		sp.PlannerResults = plannerResults

		// Build human-readable message for k9s describe view.
		sp.Message = stageProgressMessage(stage, release, pipelineRefName, total, synced, failed, deferred)

		if failed > 0 {
			onFailure := stage.OnFailure
			switch onFailure {
			case kaprov1alpha1.StageFailurePolicySkip:
				log.Info("stage has failed targets with OnFailure=skip, treating as complete",
					"stage", stage.Name, "pipelineRef", pipelineRefName, "failed", failed)
				sp.Phase = "Complete"
				stagePhase[stage.Name] = "Complete"
				// Transition Failed targets to Skipped so they are properly terminal
				// and don't pollute the release report with stale failure counts.
				for idx := range release.Status.Targets {
					t := &release.Status.Targets[idx]
					if t.Stage == stage.Name && t.PipelineRef == pipelineRefName && t.Phase == kaprov1alpha1.TargetPhaseFailed {
						t.Phase = kaprov1alpha1.TargetPhaseSkipped
					}
				}
			case kaprov1alpha1.StageFailurePolicyRollback:
				log.Info("stage has failed targets with OnFailure=rollback",
					"stage", stage.Name, "pipelineRef", pipelineRefName)
				r.triggerRollbackTargets(ctx, release, pipelineRefName, pipeline, stage.Name)
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
			default: // halt
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
				// Defer cancellation until after persistReleaseTargets to avoid
				// the stale-cache overwriting spec.cancelled.
				cancels = append(cancels, cancelRequest{pipelineRef: pipelineRefName, stage: stage.Name})
			}
		} else if synced == total {
			sp.Phase = "Complete"
			stagePhase[stage.Name] = "Complete"
		} else {
			sp.Phase = "Progressing"
			stagePhase[stage.Name] = "Progressing"
			allComplete = false
		}

		if sp.Phase == "Complete" && previousStagePhase(release, pipelineRefName, stage.Name) != "Complete" {
			r.notifyStageEvent(ctx, release, pipelineRefName, stage.Name, notification.EventStageCompleted, "stage completed")
		}
		stageProgress = append(stageProgress, sp)

		if anyFailed {
			break // fail fast within a pipeline
		}
	}

	return stageProgress, allComplete, anyFailed, nextRequeue, cancels, nil
}

func previousStagePhase(release *kaprov1alpha1.Release, pipelineRef, stageName string) string {
	for _, pipelineProgress := range release.Status.PipelineProgress {
		if pipelineProgress.Name != pipelineRef {
			continue
		}
		for _, stageProgress := range pipelineProgress.StageProgress {
			if stageProgress.Name == stageName {
				return stageProgress.Phase
			}
		}
	}
	return ""
}

// stageProgressMessage builds a human-readable status line for k9s describe view.
// Examples: "3/5 converged", "blocked: waiting for approval on de-prod", "1/8 failed"
func stageProgressMessage(stage kaprov1alpha1.Stage, release *kaprov1alpha1.Release, pipelineRef string, total, synced, failed, deferred int) string {
	if total == 0 {
		return "no matching clusters"
	}
	if synced == total {
		return fmt.Sprintf("%d/%d converged", synced, total)
	}

	// Find the most interesting phase among non-terminal targets.
	waitingApproval := 0
	applying := 0
	soaking := 0
	metricsCheck := 0
	for i := range release.Status.Targets {
		t := &release.Status.Targets[i]
		if t.Stage != stage.Name || t.PipelineRef != pipelineRef {
			continue
		}
		switch t.Phase {
		case kaprov1alpha1.TargetPhaseWaitingApproval:
			waitingApproval++
		case kaprov1alpha1.TargetPhaseApplying:
			applying++
		case kaprov1alpha1.TargetPhaseSoaking:
			soaking++
		case kaprov1alpha1.TargetPhaseMetricsCheck:
			metricsCheck++
		}
	}

	parts := fmt.Sprintf("%d/%d converged", synced, total)
	if failed > 0 {
		parts += fmt.Sprintf(", %d failed", failed)
	}
	if waitingApproval > 0 {
		parts += fmt.Sprintf(", %d awaiting approval", waitingApproval)
	}
	if applying > 0 {
		parts += fmt.Sprintf(", %d applying", applying)
	}
	if soaking > 0 {
		parts += fmt.Sprintf(", %d soaking", soaking)
	}
	if metricsCheck > 0 {
		parts += fmt.Sprintf(", %d checking metrics", metricsCheck)
	}
	if deferred > 0 {
		parts += fmt.Sprintf(", %d deferred", deferred)
	}
	return parts
}

func (r *ReleaseReconciler) stageDependencySatisfied(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
	dep kaprov1alpha1.StageDependency,
) (bool, time.Duration, error) {
	depStage, ok := pipelineStageByName(pipeline, dep.Stage)
	if !ok {
		return false, 0, fmt.Errorf("stage dependency %q not found in pipeline %s", dep.Stage, pipeline.Name)
	}

	planned, err := r.planTargetsForStage(ctx, pipelineRefName, pipeline, depStage, release)
	if err != nil {
		return false, 0, fmt.Errorf("list dependency targets for stage %s: %w", dep.Stage, err)
	}
	targets := planned.Targets
	if len(targets) == 0 {
		return true, 0, nil
	}

	expected := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		expected[target.Name] = struct{}{}
	}

	strategy := dep.Strategy
	if strategy == "" {
		strategy = kaprov1alpha1.StageDependencyAll
	}

	soak := time.Duration(0)
	if dep.RequiredSoakTime != nil {
		soak = dep.RequiredSoakTime.Duration
	}

	now := time.Now().UTC()
	successful := 0
	var shortestWait time.Duration

	for idx := range release.Status.Targets {
		target := &release.Status.Targets[idx]
		if target.PipelineRef != pipelineRefName || target.Stage != dep.Stage {
			continue
		}
		if _, ok := expected[target.Target]; !ok {
			continue
		}
		if !dependencyTargetSucceeded(target.Phase) {
			continue
		}

		successful++
		if soak == 0 {
			if strategy == kaprov1alpha1.StageDependencyAny {
				return true, 0, nil
			}
			continue
		}

		remaining := dependencySoakRemaining(target.FinishedAt, now, soak)
		if remaining <= 0 {
			if strategy == kaprov1alpha1.StageDependencyAny {
				return true, 0, nil
			}
			continue
		}
		if shortestWait == 0 || remaining < shortestWait {
			shortestWait = remaining
		}
	}

	switch strategy {
	case kaprov1alpha1.StageDependencyAny:
		return false, shortestWait, nil
	case kaprov1alpha1.StageDependencyAll:
		if successful < len(expected) {
			return false, 0, nil
		}
		return shortestWait == 0, shortestWait, nil
	default:
		return false, 0, fmt.Errorf("stage dependency %q has unsupported strategy %q", dep.Stage, dep.Strategy)
	}
}

func pipelineStageByName(pipeline *kaprov1alpha1.Pipeline, name string) (kaprov1alpha1.Stage, bool) {
	for _, stage := range pipeline.Spec.Stages {
		if stage.Name == name {
			return stage, true
		}
	}
	return kaprov1alpha1.Stage{}, false
}

func dependencyTargetSucceeded(phase kaprov1alpha1.TargetPhase) bool {
	return phase == kaprov1alpha1.TargetPhaseConverged || phase == kaprov1alpha1.TargetPhaseSkipped
}

func dependencySoakRemaining(finishedAt string, now time.Time, soak time.Duration) time.Duration {
	if finishedAt == "" {
		return soak
	}
	finished, err := time.Parse(time.RFC3339, finishedAt)
	if err != nil {
		return soak
	}
	if elapsed := now.Sub(finished); elapsed < soak {
		return soak - elapsed
	}
	return 0
}

// listRawTargetsForStage returns all MemberClusters that match the stage selector,
// filtered to spec.scope.targets when a scope is set on the Release.
func (r *ReleaseReconciler) listRawTargetsForStage(ctx context.Context, stage kaprov1alpha1.Stage, release *kaprov1alpha1.Release) ([]kaprov1alpha1.MemberCluster, error) {
	var mcList kaprov1alpha1.MemberClusterList
	sel, err := metav1.LabelSelectorAsSelector(&stage.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid stage selector: %w", err)
	}
	listOpts := []client.ListOption{
		client.MatchingLabelsSelector{Selector: sel},
	}
	if err := r.List(ctx, &mcList, listOpts...); err != nil {
		return nil, err
	}
	clusters := mcList.Items

	// Filter out suspended clusters — spec.suspend means "do not deploy to this cluster".
	filtered := clusters[:0]
	for _, mc := range clusters {
		if mc.Spec.Suspend {
			log.FromContext(ctx).Info("skipping suspended cluster", "cluster", mc.Name, "stage", stage.Name)
			continue
		}
		filtered = append(filtered, mc)
	}
	clusters = filtered

	// Apply scope filter when an explicit cluster allowlist is provided.
	if release.Spec.Scope != nil && len(release.Spec.Scope.Targets) > 0 {
		allowed := make(map[string]struct{}, len(release.Spec.Scope.Targets))
		for _, t := range release.Spec.Scope.Targets {
			allowed[t] = struct{}{}
		}
		scopeFiltered := clusters[:0]
		for _, mc := range clusters {
			if _, ok := allowed[mc.Name]; ok {
				scopeFiltered = append(scopeFiltered, mc)
			}
		}
		if len(scopeFiltered) == 0 && len(clusters) > 0 {
			log.FromContext(ctx).Info("scope filter eliminated all clusters for stage — treating as no-op",
				"stage", stage.Name, "scopeTargets", release.Spec.Scope.Targets)
		}
		clusters = scopeFiltered
	}

	return clusters, nil
}

// listTargetsForStage returns the planned MemberClusters for a stage.
func (r *ReleaseReconciler) listTargetsForStage(ctx context.Context, pipelineRefName string, pipeline *kaprov1alpha1.Pipeline, stage kaprov1alpha1.Stage, release *kaprov1alpha1.Release) ([]kaprov1alpha1.MemberCluster, error) {
	planned, err := r.planTargetsForStage(ctx, pipelineRefName, pipeline, stage, release)
	if err != nil {
		return nil, err
	}
	return planned.Targets, nil
}

// planTargetsForStage runs the scheduler-style planner for a stage and returns
// both eligible targets and recorded skip decisions.
func (r *ReleaseReconciler) planTargetsForStage(ctx context.Context, pipelineRefName string, pipeline *kaprov1alpha1.Pipeline, stage kaprov1alpha1.Stage, release *kaprov1alpha1.Release) (planner.Result, error) {
	clusters, err := r.listRawTargetsForStage(ctx, stage, release)
	if err != nil {
		return planner.Result{}, err
	}
	framework := r.Planner
	if framework == nil {
		framework = planner.NewDefaultFramework()
	}
	return framework.PlanWithResult(ctx, planner.Request{
		Release:         release,
		PipelineRefName: pipelineRefName,
		Pipeline:        pipeline,
		Stage:           stage,
	}, clusters)
}

func (r *ReleaseReconciler) applyStageStrategy(
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	stage kaprov1alpha1.Stage,
	targets []kaprov1alpha1.MemberCluster,
) ([]kaprov1alpha1.MemberCluster, int, []planner.Decision) {
	if stage.Strategy == nil || stage.Strategy.MaxParallel <= 0 {
		return targets, 0, nil
	}

	planned := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		planned[target.Name] = struct{}{}
	}

	active := 0
	bound := make(map[string]struct{}, len(targets))
	for _, target := range release.Status.Targets {
		if target.PipelineRef != pipelineRefName || target.Stage != stage.Name {
			continue
		}
		if _, ok := planned[target.Target]; !ok {
			continue
		}
		bound[target.Target] = struct{}{}
		if !planningTargetTerminal(target.Phase) {
			active++
		}
	}

	capacity := int(stage.Strategy.MaxParallel) - active
	if capacity < 0 {
		capacity = 0
	}

	bindTargets := make([]kaprov1alpha1.MemberCluster, 0, len(targets))
	decisions := make([]planner.Decision, 0)
	deferred := 0
	for _, target := range targets {
		if _, ok := bound[target.Name]; ok {
			continue
		}
		if capacity > 0 {
			bindTargets = append(bindTargets, target)
			capacity--
			continue
		}
		deferred++
		decisions = append(decisions, planner.Decision{
			Target:  target.Name,
			Plugin:  "stage-strategy",
			Phase:   "Bind",
			Reason:  "MaxParallel",
			Message: fmt.Sprintf("deferred by stage strategy maxParallel=%d", stage.Strategy.MaxParallel),
		})
	}

	return bindTargets, deferred, decisions
}

func planningTargetTerminal(phase kaprov1alpha1.TargetPhase) bool {
	return phase == kaprov1alpha1.TargetPhaseConverged ||
		phase == kaprov1alpha1.TargetPhaseFailed ||
		phase == kaprov1alpha1.TargetPhaseSkipped
}

func apiPlannerResults(decisions []planner.Decision) []kaprov1alpha1.PlannerResult {
	if len(decisions) == 0 {
		return nil
	}
	limit := len(decisions)
	if limit > maxPlannerResultsPerStage {
		limit = maxPlannerResultsPerStage
	}
	results := make([]kaprov1alpha1.PlannerResult, 0, limit)
	for i := 0; i < limit; i++ {
		decision := decisions[i]
		results = append(results, kaprov1alpha1.PlannerResult{
			Target:  decision.Target,
			Plugin:  decision.Plugin,
			Phase:   decision.Phase,
			Reason:  decision.Reason,
			Message: decision.Message,
		})
	}
	return results
}

// upsertTarget finds an existing TargetStatus entry for
// (pipelineRefName, stage.Name, mc.Name) or appends a new one.
// Returns the slice index of the entry (stable within a single reconcile).
func (r *ReleaseReconciler) upsertTarget(
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
	stage kaprov1alpha1.Stage,
	mc kaprov1alpha1.MemberCluster,
	resolvedGate *kaprov1alpha1.GatePolicySpec,
) (int, error) {
	desiredVersions := releaseDesiredVersions(release)
	version, appKey := primaryDesiredVersion(desiredVersions, release.Status.ResolvedVersion, releaseAppKey(release))
	key := syncKey(pipelineRefName, stage.Name, mc.Name)
	for i, target := range release.Status.Targets {
		if syncKey(target.PipelineRef, target.Stage, target.Target) == key {
			target := &release.Status.Targets[i]
			target.Version = version
			target.Gate = resolvedGate
			target.AppKey = appKey
			target.DesiredVersions = copyStringMap(desiredVersions)
			return i, nil
		}
	}
	newTarget := kaprov1alpha1.TargetStatus{
		ReleaseRef:      release.Name,
		Target:          mc.Name,
		PipelineRef:     pipelineRefName,
		Pipeline:        pipeline.Name,
		Stage:           stage.Name,
		Version:         version,
		Gate:            resolvedGate,
		AppKey:          appKey,
		DesiredVersions: copyStringMap(desiredVersions),
	}
	release.Status.Targets = append(release.Status.Targets, newTarget)
	return len(release.Status.Targets) - 1, nil
}

func resolveStageGate(pipeline *kaprov1alpha1.Pipeline, stage kaprov1alpha1.Stage) (*kaprov1alpha1.GatePolicySpec, error) {
	if stage.Gate == nil {
		return nil, nil
	}
	gatePolicy := stage.Gate.DeepCopy()
	if len(gatePolicy.Gate.Metrics) == 0 {
		return gatePolicy, nil
	}
	presets := map[string]kaprov1alpha1.MetricGate{}
	if pipeline != nil {
		presets = pipeline.Spec.MetricPresets
	}
	for i, metric := range gatePolicy.Gate.Metrics {
		if metric.Preset == "" {
			continue
		}
		preset, ok := presets[metric.Preset]
		if !ok {
			return nil, fmt.Errorf("stage %q metric[%d] references unknown metric preset %q", stage.Name, i, metric.Preset)
		}
		gatePolicy.Gate.Metrics[i] = mergeMetricPreset(preset, metric)
	}
	return gatePolicy, nil
}

func mergeMetricPreset(preset, override kaprov1alpha1.MetricGate) kaprov1alpha1.MetricGate {
	out := preset
	out.Preset = override.Preset
	if override.Provider != "" {
		out.Provider = override.Provider
	}
	if override.Query != "" {
		out.Query = override.Query
	}
	if override.Window != "" {
		out.Window = override.Window
	}
	if override.Interval != "" {
		out.Interval = override.Interval
	}
	if override.Endpoint != "" {
		out.Endpoint = override.Endpoint
	}
	if override.Threshold != nil {
		out.Threshold = override.Threshold
	}
	if len(override.Config) > 0 {
		out.Config = override.Config
	}
	return out
}

// triggerRollbackTargets appends rollback TargetStatus entries for every
// converged target in the failed stage and all earlier stages in the same
// pipeline instance. In-memory only; caller patches.
func (r *ReleaseReconciler) triggerRollbackTargets(ctx context.Context, release *kaprov1alpha1.Release, pipelineRefName string, pipeline *kaprov1alpha1.Pipeline, stageName string) {
	eligibleStages := make(map[string]struct{}, len(pipeline.Spec.Stages))
	for _, stage := range pipeline.Spec.Stages {
		eligibleStages[stage.Name] = struct{}{}
		if stage.Name == stageName {
			break
		}
	}
	n := len(release.Status.Targets) // capture length before appending
	for i := 0; i < n; i++ {
		target := &release.Status.Targets[i]
		if target.PipelineRef != pipelineRefName {
			continue
		}
		if _, ok := eligibleStages[target.Stage]; !ok {
			continue
		}
		if target.Phase != kaprov1alpha1.TargetPhaseConverged {
			continue
		}
		r.triggerTargetRollback(ctx, release, i)
	}
}

func (r *ReleaseReconciler) notifyReleaseEvent(ctx context.Context, release *kaprov1alpha1.Release, eventType, message string) {
	if r.Notifier == nil {
		return
	}
	policy := r.notificationPolicyForRelease(ctx, release)
	r.Notifier.Notify(ctx, notification.Event{
		Type:      eventType,
		Phase:     string(release.Status.Phase),
		Version:   release.Status.ResolvedVersion,
		Release:   release.Name,
		Message:   message,
		IsFailure: eventType == notification.EventReleaseFailed,
	}, policy)
}

func (r *ReleaseReconciler) notifyStageEvent(ctx context.Context, release *kaprov1alpha1.Release, pipelineRef, stage, eventType, message string) {
	if r.Notifier == nil {
		return
	}
	policy := r.notificationPolicyForStage(ctx, release, pipelineRef, stage)
	r.Notifier.Notify(ctx, notification.Event{
		Type:     eventType,
		Phase:    "Complete",
		Version:  release.Status.ResolvedVersion,
		Release:  release.Name,
		Pipeline: pipelineRef,
		Stage:    stage,
		Message:  message,
	}, policy)
}

func (r *ReleaseReconciler) notificationPolicyForRelease(ctx context.Context, release *kaprov1alpha1.Release) notification.NotificationPolicy {
	policies := make([]notification.NotificationPolicy, 0)
	for _, pipelineRef := range release.Spec.Pipelines {
		var pipeline kaprov1alpha1.Pipeline
		if err := r.Get(ctx, client.ObjectKey{Name: pipelineRef.Pipeline}, &pipeline); err != nil {
			log.FromContext(ctx).Error(err, "failed to load pipeline for release notification policy", "pipeline", pipelineRef.Pipeline)
			continue
		}
		for _, stage := range pipeline.Spec.Stages {
			policies = append(policies, notificationPolicyFrom(stage.Gate))
		}
	}
	return mergeNotificationPolicies(policies...)
}

func (r *ReleaseReconciler) notificationPolicyForStage(ctx context.Context, release *kaprov1alpha1.Release, pipelineRefName, stageName string) notification.NotificationPolicy {
	for _, pipelineRef := range release.Spec.Pipelines {
		if pipelineRef.Name != pipelineRefName {
			continue
		}
		var pipeline kaprov1alpha1.Pipeline
		if err := r.Get(ctx, client.ObjectKey{Name: pipelineRef.Pipeline}, &pipeline); err != nil {
			log.FromContext(ctx).Error(err, "failed to load pipeline for stage notification policy", "pipeline", pipelineRef.Pipeline)
			return notification.EmptyPolicy
		}
		for _, stage := range pipeline.Spec.Stages {
			if stage.Name == stageName {
				return notificationPolicyFrom(stage.Gate)
			}
		}
	}
	return notification.EmptyPolicy
}

func (r *ReleaseReconciler) hasActiveRollbackTargets(release *kaprov1alpha1.Release) bool {
	for _, target := range release.Status.Targets {
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

// cancelPendingStageTargets signals non-terminal targets in the stage to stop.
// This implements failurePolicy: halt — sibling targets stop advancing.
//
// Ownership contract: the parent writes spec.cancelled (parent owns spec),
// the child ReleaseTargetReconciler observes it and transitions to Failed
// (child owns status). This avoids cross-controller status writes.
func (r *ReleaseReconciler) cancelPendingStageTargets(ctx context.Context, release *kaprov1alpha1.Release, pipelineRefName, stageName string) {
	log := log.FromContext(ctx)

	// List ReleaseTarget objects for this release (indexed, not full scan).
	var list kaprov1alpha1.ReleaseTargetList
	if err := r.List(ctx, &list, client.MatchingFields{IndexKeyReleaseTargetRelease: release.Name}); err != nil {
		log.Error(err, "cancel: failed to list ReleaseTargets")
		return
	}

	for i := range list.Items {
		rt := &list.Items[i]
		if rt.Spec.PipelineRef != pipelineRefName || rt.Spec.Stage != stageName {
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
			[]byte(`{"spec":{"cancelled":true,"cancelledReason":"stage halted due to peer failure (failurePolicy: halt)"}}`))
		if err := r.Patch(ctx, rt, rawPatch); err != nil {
			log.Error(err, "cancel: failed to patch ReleaseTarget spec", "name", rt.Name)
			continue
		}
		log.Info("cancel: signalled cancellation", "target", rt.Name)

		// Also update inline targets for immediate aggregation so the parent
		// can compute the correct Release phase without waiting for child reconcile.
		for j := range release.Status.Targets {
			t := &release.Status.Targets[j]
			if t.Target == rt.Spec.Target && t.PipelineRef == pipelineRefName && t.Stage == stageName {
				t.Phase = kaprov1alpha1.TargetPhaseFailed
				t.Message = "cancelled: " + rt.Spec.CancelledReason
				break
			}
		}
	}
}

// clearActiveRelease clears mc.status.activeRelease for all MemberClusters
// targeted by this Release, found via release.Status.Targets.
func (r *ReleaseReconciler) clearActiveRelease(ctx context.Context, release *kaprov1alpha1.Release) {
	log := log.FromContext(ctx)
	if len(release.Status.Targets) == 0 {
		if err := r.loadReleaseTargets(ctx, release); err != nil {
			log.Error(err, "clearActiveRelease: failed to load release targets")
			return
		}
	}
	seen := make(map[string]bool)
	for _, target := range release.Status.Targets {
		mcName := target.Target
		if seen[mcName] {
			continue
		}
		seen[mcName] = true
		var mc kaprov1alpha1.MemberCluster
		if err := r.Get(ctx, client.ObjectKey{Name: mcName}, &mc); err != nil {
			continue
		}
		if mc.Status.ActiveRelease == release.Name {
			patch := client.MergeFrom(mc.DeepCopy())
			mc.Status.ActiveRelease = ""
			if err := r.Status().Patch(ctx, &mc, patch); err != nil {
				log.Error(err, "clearActiveRelease: failed to clear activeRelease", "cluster", mcName)
			}
		}
	}
}

func releaseTargetObjectName(target kaprov1alpha1.TargetStatus) string {
	name := syncName(target.ReleaseRef, target.PipelineRef, target.Stage, target.Target)
	if target.Rollback {
		return name + "-rollback"
	}
	return name
}

// ReleaseTargetObjectNameForTest exposes the deterministic child-object naming
// contract to external tests without widening production behavior.
func ReleaseTargetObjectNameForTest(target kaprov1alpha1.TargetStatus) string {
	return releaseTargetObjectName(target)
}

func (r *ReleaseReconciler) releaseTargetFromStatus(release *kaprov1alpha1.Release, target kaprov1alpha1.TargetStatus) *kaprov1alpha1.ReleaseTarget {
	rt := &kaprov1alpha1.ReleaseTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: releaseTargetObjectName(target),
			Labels: map[string]string{
				IndexKeyRelease:     release.Name,
				"kapro.io/target":   target.Target,
				"kapro.io/pipeline": target.PipelineRef,
				"kapro.io/stage":    target.Stage,
			},
		},
		Spec: kaprov1alpha1.ReleaseTargetSpec{
			ReleaseRef:      target.ReleaseRef,
			Target:          target.Target,
			PipelineRef:     target.PipelineRef,
			Pipeline:        target.Pipeline,
			Stage:           target.Stage,
			Version:         target.Version,
			Gate:            target.Gate,
			AppKey:          target.AppKey,
			DesiredVersions: copyStringMap(target.DesiredVersions),
			Rollback:        target.Rollback,
		},
		Status: kaprov1alpha1.ReleaseTargetStatus{TargetStatus: target},
	}
	if err := ctrl.SetControllerReference(release, rt, r.Scheme); err == nil {
		return rt
	}
	return rt
}

func targetStatusFromReleaseTarget(rt *kaprov1alpha1.ReleaseTarget) kaprov1alpha1.TargetStatus {
	target := rt.Status.TargetStatus
	target.ReleaseRef = rt.Spec.ReleaseRef
	target.Target = rt.Spec.Target
	target.PipelineRef = rt.Spec.PipelineRef
	target.Pipeline = rt.Spec.Pipeline
	target.Stage = rt.Spec.Stage
	target.Version = rt.Spec.Version
	target.Gate = rt.Spec.Gate
	target.AppKey = rt.Spec.AppKey
	target.DesiredVersions = copyStringMap(rt.Spec.DesiredVersions)
	target.Rollback = rt.Spec.Rollback
	return target
}

func (r *ReleaseReconciler) loadReleaseTargets(ctx context.Context, release *kaprov1alpha1.Release) error {
	var list kaprov1alpha1.ReleaseTargetList
	if err := r.List(ctx, &list,
		client.MatchingFields{IndexKeyReleaseTargetRelease: release.Name},
	); err != nil {
		return err
	}
	targets := make([]kaprov1alpha1.TargetStatus, 0, len(list.Items))
	for i := range list.Items {
		rt := &list.Items[i]
		targets = append(targets, targetStatusFromReleaseTarget(rt))
	}
	sort.Slice(targets, func(i, j int) bool {
		ai := releaseTargetObjectName(targets[i])
		aj := releaseTargetObjectName(targets[j])
		return ai < aj
	})
	release.Status.Targets = targets
	return nil
}

// persistReleaseTargets ensures a ReleaseTarget CRD exists for each in-memory
// target entry. The parent creates new children and updates their specs/labels/
// ownerRefs, but NEVER writes child status — that's owned by ReleaseTargetReconciler.
func (r *ReleaseReconciler) persistReleaseTargets(ctx context.Context, release *kaprov1alpha1.Release) error {
	var existingList kaprov1alpha1.ReleaseTargetList
	if err := r.List(ctx, &existingList,
		client.MatchingFields{IndexKeyReleaseTargetRelease: release.Name},
	); err != nil {
		return err
	}
	existing := make(map[string]*kaprov1alpha1.ReleaseTarget, len(existingList.Items))
	for i := range existingList.Items {
		rt := existingList.Items[i]
		existing[rt.Name] = rt.DeepCopy()
	}

	for _, target := range release.Status.Targets {
		name := releaseTargetObjectName(target)
		desired := r.releaseTargetFromStatus(release, target)
		if _, ok := existing[name]; !ok {
			// Create new child — status starts empty, ReleaseTargetReconciler will drive it.
			toCreate := desired.DeepCopy()
			toCreate.Status = kaprov1alpha1.ReleaseTargetStatus{}
			if err := r.Create(ctx, toCreate); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create ReleaseTarget %s: %w", name, err)
			}
		} else {
			// Update spec/labels/ownerRefs only — never touch status.
			// Skip the patch if nothing actually changed (avoids O(N) API writes
			// per reconcile when targets are stable).
			current := existing[name]
			if releaseTargetSpecEqual(current, desired) {
				continue
			}
			specPatch := client.MergeFrom(current.DeepCopy())
			current.Labels = desired.Labels
			current.Spec = desired.Spec
			current.OwnerReferences = desired.OwnerReferences
			if err := r.Patch(ctx, current, specPatch); err != nil {
				return fmt.Errorf("patch ReleaseTarget %s: %w", name, err)
			}
		}
	}
	return nil
}

// handleDeletion clears MemberCluster activeRelease references and removes the finalizer.
// Targets are inline status — nothing to delete externally.
func (r *ReleaseReconciler) handleDeletion(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling Release deletion", "name", release.Name)

	// Ensure targets are loaded so clearActiveRelease can find all clusters to
	// clear. If this fails, retry deletion rather than removing the finalizer
	// with stale activeRelease claims still pointing at this Release.
	if len(release.Status.Targets) == 0 {
		if err := r.loadReleaseTargets(ctx, release); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleDeletion: load release targets for cleanup: %w", err)
		}
	}
	r.clearActiveRelease(ctx, release)

	patch := client.MergeFrom(release.DeepCopy())
	controllerutil.RemoveFinalizer(release, releaseFinalizer)
	if err := r.Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}

	log.Info("Release cleanup complete", "name", release.Name)
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	// Index Approvals by release label — used to map Approval changes back to
	// the owning Release.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Approval{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index Approval by %s: %w", IndexKeyRelease, err)
	}

	// Index ReleaseTargets by owning Release and target cluster so MemberCluster
	// and ReleaseTarget watches can route directly to affected Releases.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.ReleaseTarget{}, IndexKeyActiveCluster,
		ActiveClusterExtractor,
	); err != nil {
		return fmt.Errorf("index ReleaseTarget by %s: %w", IndexKeyActiveCluster, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.ReleaseTarget{}, IndexKeyReleaseTargetRelease,
		ReleaseTargetReleaseExtractor,
	); err != nil {
		return fmt.Errorf("index ReleaseTarget by %s: %w", IndexKeyReleaseTargetRelease, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Release{}, IndexKeyReleaseProgressing,
		ReleaseProgressingExtractor,
	); err != nil {
		return fmt.Errorf("index Release by %s: %w", IndexKeyReleaseProgressing, err)
	}

	forPredicates := []predicate.Predicate{predicate.GenerationChangedPredicate{}}
	if r.ShardPredicate != nil {
		forPredicates = append(forPredicates, r.ShardPredicate)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha1.Release{},
			builder.WithPredicates(forPredicates...),
		).
		// Watch MemberClusters — if a new cluster is registered whose labels match
		// an in-progress stage, wake up the Release so a new target entry is created.
		Watches(
			&kaprov1alpha1.MemberCluster{},
			handler.EnqueueRequestsFromMapFunc(r.releasesForMemberCluster),
			builder.WithPredicates(releaseMemberClusterPredicates()),
		).
		// Watch Approvals — when an Approval CR is created for a WaitingApproval target,
		// wake up the Release so the target can advance to Applying.
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(approvalForRelease),
		).
		Watches(
			&kaprov1alpha1.ReleaseTarget{},
			handler.EnqueueRequestsFromMapFunc(releaseForTarget),
		).
		Complete(r)
}

func releaseMemberClusterPredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldMC, okOld := e.ObjectOld.(*kaprov1alpha1.MemberCluster)
			newMC, okNew := e.ObjectNew.(*kaprov1alpha1.MemberCluster)
			if !okOld || !okNew {
				return true
			}
			if oldMC.GetGeneration() != newMC.GetGeneration() {
				return true
			}
			if !labels.Equals(labels.Set(oldMC.GetLabels()), labels.Set(newMC.GetLabels())) {
				return true
			}
			return false
		},
	}
}

func (r *ReleaseReconciler) releasesForMemberCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha1.MemberCluster)
	if !ok {
		return nil
	}
	// Use the active-cluster field index to find only release targets that
	// reference this specific cluster. This avoids scanning the entire Release
	// fleet on every MemberCluster status update.
	var targetList kaprov1alpha1.ReleaseTargetList
	if err := r.List(ctx, &targetList,
		client.MatchingFields{IndexKeyActiveCluster: mc.Name},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list release targets for member cluster", "cluster", mc.Name)
		return nil
	}
	if len(targetList.Items) == 0 {
		return r.progressingReleasesForNewCluster(ctx, mc)
	}
	seen := make(map[client.ObjectKey]struct{}, len(targetList.Items))
	reqs := make([]ctrl.Request, 0, len(targetList.Items))
	for i := range targetList.Items {
		rt := &targetList.Items[i]
		key := client.ObjectKey{Name: rt.Spec.ReleaseRef, Namespace: rt.Namespace}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var rel kaprov1alpha1.Release
		if err := r.Get(ctx, key, &rel); err != nil {
			continue
		}
		if rel.Status.Phase == kaprov1alpha1.ReleasePhaseComplete || rel.Status.Phase == kaprov1alpha1.ReleasePhaseFailed {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: key})
	}
	return reqs
}

// ReleasesForMemberClusterForTest exposes the watch-mapper logic to external
// tests without widening the production watch contract.
func (r *ReleaseReconciler) ReleasesForMemberClusterForTest(ctx context.Context, mc *kaprov1alpha1.MemberCluster) []ctrl.Request {
	return r.releasesForMemberCluster(ctx, mc)
}

// ProgressingReleasesForNewClusterForTest exposes the new-cluster fallback path
// to external tests.
func (r *ReleaseReconciler) ProgressingReleasesForNewClusterForTest(ctx context.Context, mc *kaprov1alpha1.MemberCluster) []ctrl.Request {
	return r.progressingReleasesForNewCluster(ctx, mc)
}

// progressingReleasesForNewCluster handles the case where a newly registered
// cluster is not yet referenced by any Release.status.targets entry. The
// active-cluster index cannot find these releases, so we fall back to checking
// only non-terminal releases and enqueue those whose Pipeline selectors could
// target the cluster.
func (r *ReleaseReconciler) progressingReleasesForNewCluster(ctx context.Context, mc *kaprov1alpha1.MemberCluster) []ctrl.Request {
	var releaseList kaprov1alpha1.ReleaseList
	if err := r.List(ctx, &releaseList, client.MatchingFields{IndexKeyReleaseProgressing: "true"}); err != nil {
		// Some tests and ad-hoc fake clients do not register field indexes. Fall back
		// to a full list there; production SetupWithManager always installs the index.
		if err := r.List(ctx, &releaseList); err != nil {
			log.FromContext(ctx).Error(err, "failed to list releases for new cluster fallback", "cluster", mc.Name)
			return nil
		}
	}

	pipelineCache := make(map[string]*kaprov1alpha1.Pipeline)
	reqs := make([]ctrl.Request, 0)

	for i := range releaseList.Items {
		rel := &releaseList.Items[i]
		if rel.Status.Phase == kaprov1alpha1.ReleasePhaseComplete || rel.Status.Phase == kaprov1alpha1.ReleasePhaseFailed {
			continue
		}
		if !releaseScopeAllowsCluster(rel, mc.Name) {
			continue
		}
		if r.releaseCouldTargetCluster(ctx, rel, mc, pipelineCache) {
			reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rel)})
		}
	}

	return reqs
}

func releaseScopeAllowsCluster(release *kaprov1alpha1.Release, clusterName string) bool {
	if release.Spec.Scope == nil || len(release.Spec.Scope.Targets) == 0 {
		return true
	}
	for _, name := range release.Spec.Scope.Targets {
		if name == clusterName {
			return true
		}
	}
	return false
}

func (r *ReleaseReconciler) releaseCouldTargetCluster(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	mc *kaprov1alpha1.MemberCluster,
	pipelineCache map[string]*kaprov1alpha1.Pipeline,
) bool {
	for _, ref := range release.Spec.Pipelines {
		pipeline, ok := pipelineCache[ref.Pipeline]
		if !ok {
			var fetched kaprov1alpha1.Pipeline
			if err := r.Get(ctx, client.ObjectKey{Name: ref.Pipeline}, &fetched); err != nil {
				continue
			}
			pipelineCache[ref.Pipeline] = &fetched
			pipeline = &fetched
		}
		for _, stage := range pipeline.Spec.Stages {
			selector, err := metav1.LabelSelectorAsSelector(&stage.Selector)
			if err != nil {
				continue
			}
			if selector.Matches(labels.Set(mc.Labels)) {
				return true
			}
		}
	}
	return false
}

func releaseForTarget(_ context.Context, obj client.Object) []ctrl.Request {
	rt, ok := obj.(*kaprov1alpha1.ReleaseTarget)
	if !ok {
		return nil
	}
	if rt.Spec.ReleaseRef == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{
			Name:      rt.Spec.ReleaseRef,
			Namespace: rt.Namespace,
		},
	}}
}

// syncKey builds a unique map key for one target rollout entry:
// <pipelineRefName>/<stage>/<target>.
func syncKey(pipelineRefName, stage, target string) string {
	return pipelineRefName + "/" + stage + "/" + target
}

// syncName builds the deterministic name for one target rollout entry.
// Format: <release-prefix>-<hashed logical key>. The hash makes the name
// collision-safe even when individual units contain hyphens.
func syncName(release, pipelineRef, stage, target string) string {
	key := fmt.Sprintf("%s/%s", release, syncKey(pipelineRef, stage, target))
	h := fnv.New32a()
	_, _ = fmt.Fprint(h, key)
	prefix := release
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	return fmt.Sprintf("%s-%08x", prefix, h.Sum32())
}

// releaseAppKey returns the key used in MemberCluster.status.currentVersions.
func releaseAppKey(release *kaprov1alpha1.Release) string {
	return "default"
}

func releaseDesiredVersionsFromSpec(release *kaprov1alpha1.Release) map[string]string {
	desired := make(map[string]string, len(release.Spec.Versions)+1)
	if release.Spec.Version != "" {
		desired[releaseAppKey(release)] = release.Spec.Version
	}
	for unit, version := range release.Spec.Versions {
		if unit == "" || version == "" {
			continue
		}
		desired[unit] = version
	}
	if len(desired) == 0 {
		return nil
	}
	return desired
}

func releaseDesiredVersions(release *kaprov1alpha1.Release) map[string]string {
	if len(release.Spec.Versions) > 0 {
		return releaseDesiredVersionsFromSpec(release)
	}
	if release.Status.ResolvedVersion == "" {
		return nil
	}
	return map[string]string{"default": release.Status.ResolvedVersion}
}

func releasePrimaryVersion(release *kaprov1alpha1.Release, desired map[string]string) string {
	if version := desired[releaseAppKey(release)]; version != "" {
		return version
	}
	keys := make([]string, 0, len(desired))
	for unit := range desired {
		keys = append(keys, unit)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return desired[keys[0]]
}

func primaryDesiredVersion(desired map[string]string, fallbackVersion, fallbackAppKey string) (string, string) {
	if len(desired) == 0 {
		return fallbackVersion, fallbackAppKey
	}
	keys := make([]string, 0, len(desired))
	for appKey := range desired {
		keys = append(keys, appKey)
	}
	sort.Strings(keys)
	appKey := keys[0]
	return desired[appKey], appKey
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *ReleaseReconciler) setReleaseReadyCondition(release *kaprov1alpha1.Release, status metav1.ConditionStatus, reason, message string) {
	if len(message) > maxReleaseReadyMessageSize {
		message = message[:maxReleaseReadyMessageSize]
	}
	apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		ObservedGeneration: release.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *ReleaseReconciler) setReconcilingCondition(release *kaprov1alpha1.Release, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeReconciling,
		Status:             status,
		Reason:             reason,
		ObservedGeneration: release.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *ReleaseReconciler) setStalledCondition(release *kaprov1alpha1.Release, reason, message string) {
	apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeStalled,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		ObservedGeneration: release.Generation,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (r *ReleaseReconciler) clearStalledCondition(release *kaprov1alpha1.Release) {
	apimeta.RemoveStatusCondition(&release.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func releaseProgressSummary(release *kaprov1alpha1.Release) string {
	activePipelines := 0
	for _, p := range release.Status.PipelineProgress {
		if p.Phase == "Progressing" || p.Phase == "Pending" {
			activePipelines++
		}
	}

	activeTargets := 0
	for _, target := range release.Status.Targets {
		if target.Rollback {
			continue
		}
		if target.Phase != kaprov1alpha1.TargetPhaseConverged && target.Phase != kaprov1alpha1.TargetPhaseFailed {
			activeTargets++
		}
	}

	return fmt.Sprintf("release progressing: %d active pipelines, %d active targets", activePipelines, activeTargets)
}

// normalizeReleaseStatus deduplicates Release.status.targets and bounds per-target
// gate history. It never drops target execution rows, because those rows are the
// source of truth for in-flight rollout state.
func (r *ReleaseReconciler) normalizeReleaseStatus(release *kaprov1alpha1.Release) {
	if len(release.Status.Targets) == 0 {
		return
	}

	// Keep the latest current-state row for each logical target, plus at most one
	// rollback row. This prevents Release.status.targets from becoming an append-only log.
	seen := make(map[string]struct{}, len(release.Status.Targets))
	normalized := make([]kaprov1alpha1.TargetStatus, 0, len(release.Status.Targets))
	for i := len(release.Status.Targets) - 1; i >= 0; i-- {
		target := release.Status.Targets[i]
		r.normalizeTargetEntry(&target)
		key := syncKey(target.PipelineRef, target.Stage, target.Target)
		if target.Rollback {
			key += "/rollback"
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, target)
	}

	for i, j := 0, len(normalized)-1; i < j; i, j = i+1, j-1 {
		normalized[i], normalized[j] = normalized[j], normalized[i]
	}

	release.Status.Targets = normalized
}

func (r *ReleaseReconciler) normalizeTargetEntry(target *kaprov1alpha1.TargetStatus) {
	if len(target.Gates) > maxGateRunsPerTarget {
		target.Gates = target.Gates[len(target.Gates)-maxGateRunsPerTarget:]
	}
	for i := range target.Gates {
		if len(target.Gates[i].Results) > maxGateResultsPerGateRun {
			target.Gates[i].Results = target.Gates[i].Results[len(target.Gates[i].Results)-maxGateResultsPerGateRun:]
		}
	}
}

// appendAuditEntry records the delivery provenance of a completed Release in
// Release.status.auditTrail. It is idempotent — an entry for the same Release
// version is only appended once. AuditTrail is capped at 50 entries (oldest trimmed).
// This method modifies release.Status.AuditTrail in-place; the caller must include
// release in a status patch for the change to persist.
func (r *ReleaseReconciler) appendAuditEntry(_ context.Context, release *kaprov1alpha1.Release) {
	// Idempotency: already have an entry for this release.
	for _, e := range release.Status.AuditTrail {
		if e.Release == release.Name && e.Artifact == release.Spec.Version {
			return
		}
	}

	var scope []string
	if release.Spec.Scope != nil {
		scope = release.Spec.Scope.Targets
	}

	entry := kaprov1alpha1.AuditEntry{
		Artifact:    release.Spec.Version,
		Release:     release.Name,
		Scope:       scope,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	release.Status.AuditTrail = append(release.Status.AuditTrail, entry)

	const maxAuditTrail = 50
	if len(release.Status.AuditTrail) > maxAuditTrail {
		release.Status.AuditTrail = release.Status.AuditTrail[len(release.Status.AuditTrail)-maxAuditTrail:]
	}
}

// computeReport builds the inline ReleaseReportSummary from Release.status.targets.
// It is a bounded, counter-only summary; per-target detail lives in status.targets.
func (r *ReleaseReconciler) computeReport(release *kaprov1alpha1.Release) kaprov1alpha1.ReleaseReportSummary {
	now := time.Now().UTC()

	st := kaprov1alpha1.ReleaseReportSummary{
		Phase:           release.Status.Phase,
		Artifact:        release.Spec.Version,
		ResolvedVersion: release.Status.ResolvedVersion,
		StartedAt:       release.Status.StartedAt,
		CompletedAt:     release.Status.CompletedAt,
	}
	st.TotalArtifacts = 1
	st.DeltaArtifacts = 1

	if st.StartedAt != "" {
		if started, err := time.Parse(time.RFC3339, st.StartedAt); err == nil {
			end := now
			if st.CompletedAt != "" {
				if completed, err := time.Parse(time.RFC3339, st.CompletedAt); err == nil {
					end = completed
				}
			}
			st.Duration = end.Sub(started).Round(time.Second).String()
		}
	}

	// Count targets from inline status; list pending approvals by deterministic name.
	// Key by (pipelineRef, stage, cluster) to avoid undercounting when the same cluster
	// is targeted by multiple pipelines or stages.
	targetPhases := make(map[string]kaprov1alpha1.TargetPhase, len(release.Status.Targets))
	var rolledBack int
	var pendingApprovals []string
	for _, target := range release.Status.Targets {
		if target.Rollback {
			rolledBack++
			continue
		}
		key := target.PipelineRef + "\x00" + target.Stage + "\x00" + target.Target
		targetPhases[key] = target.Phase
		if target.Phase == kaprov1alpha1.TargetPhaseWaitingApproval {
			pendingApprovals = append(pendingApprovals, internalgate.ApprovalName(release.Name, syncName(release.Name, target.PipelineRef, target.Stage, target.Target)))
		}
	}

	var totalTargets, synced, failed, pending int
	for _, phase := range targetPhases {
		totalTargets++
		switch phase {
		case kaprov1alpha1.TargetPhaseConverged, kaprov1alpha1.TargetPhaseSkipped:
			synced++
		case kaprov1alpha1.TargetPhaseFailed:
			failed++
		default:
			pending++
		}
	}
	st.TotalTargets = totalTargets
	st.SyncedTargets = synced
	st.FailedTargets = failed
	st.PendingTargets = pending
	st.RolledBackTargets = rolledBack
	st.PendingApprovals = pendingApprovals

	return st
}

package controller

import (
	"context"
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

const releaseFinalizer = "kapro.io/release-cleanup"

const (
	maxReleaseTargetRows       = 1024
	maxGateRunsPerTarget       = 16
	maxGateResultsPerGateRun   = 16
	maxReleaseReadyMessageSize = 256
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
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	// Gate dependencies (migrated from SyncReconciler after Sync CRD fold).
	ActuatorRegistry *actuator.Registry
	SoakGate         gate.Gate
	MetricsGate      gate.Gate
	ApprovalGate     gate.Gate
	VerificationGate gate.Gate
	Notifier         notification.Notifier
	ApprovalSecret   []byte
	ExternalURL      string
	GateRegistry     *gate.Registry
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=releases/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=artifacts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

func (r *ReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var release kaprov1alpha1.Release
	if err := r.Get(ctx, req.NamespacedName, &release); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Release",
		"name", release.Name,
		"phase", release.Status.Phase,
		"artifact", release.Spec.Artifact,
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
		if patchErr := r.Status().Patch(ctx, &release, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch suspended conditions: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	switch release.Status.Phase {
	case "", kaprov1alpha1.ReleasePhasePending:
		return r.handlePending(ctx, &release)
	case kaprov1alpha1.ReleasePhaseProgressing:
		return r.handleProgressing(ctx, &release)
	case kaprov1alpha1.ReleasePhaseComplete, kaprov1alpha1.ReleasePhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// handlePending resolves the Artifact OCI digest and transitions to Progressing.
// It also initialises PipelineProgress entries so the status table is populated
// before any target rollout entries are created.
func (r *ReleaseReconciler) handlePending(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	resolvedVersion := release.Status.ResolvedVersion

	if resolvedVersion == "" && release.Spec.Artifact != "" {
		var artifact kaprov1alpha1.Artifact
		if err := r.Get(ctx, client.ObjectKey{Name: release.Spec.Artifact}, &artifact); err != nil {
			if apierrors.IsNotFound(err) {
				patch := client.MergeFrom(release.DeepCopy())
				r.setReleaseReadyCondition(release, metav1.ConditionFalse, "ArtifactNotFound", "artifact "+release.Spec.Artifact+" not found")
				r.setStalledCondition(release, "ArtifactNotFound", "waiting for artifact "+release.Spec.Artifact+" to be created")
				r.setReconcilingCondition(release, metav1.ConditionFalse, "ArtifactNotFound", "stalled: artifact not found")
				release.Status.ObservedGeneration = release.Generation
				if patchErr := r.Status().Patch(ctx, release, patch); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("patch stalled condition: %w", patchErr)
				}
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			return ctrl.Result{RequeueAfter: requeueNormal},
				fmt.Errorf("get artifact %s: %w", release.Spec.Artifact, err)
		}
		for _, src := range artifact.Spec.Sources {
			if src.OCI != nil && src.OCI.Digest != "" {
				resolvedVersion = src.OCI.Repository + "@" + src.OCI.Digest
				break
			}
		}
		if resolvedVersion == "" {
			patch := client.MergeFrom(release.DeepCopy())
			r.setReleaseReadyCondition(release, metav1.ConditionFalse, "ArtifactNotReady", "artifact "+release.Spec.Artifact+" has no OCI source with digest")
			r.setStalledCondition(release, "ArtifactNotReady", "artifact "+release.Spec.Artifact+" has no OCI digest")
			r.setReconcilingCondition(release, metav1.ConditionFalse, "ArtifactNotReady", "stalled: artifact not ready")
			release.Status.ObservedGeneration = release.Generation
			if patchErr := r.Status().Patch(ctx, release, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch stalled condition: %w", patchErr)
			}
			return ctrl.Result{RequeueAfter: requeueNormal}, nil
		}
		log.Info("resolved artifact OCI digest", "artifact", release.Spec.Artifact, "resolved", resolvedVersion)
	}

	// Initialise pipeline progress entries so the status table is pre-populated.
	progress := make([]kaprov1alpha1.PipelineProgress, 0, len(release.Spec.Pipelines))
	for _, ref := range release.Spec.Pipelines {
		progress = append(progress, kaprov1alpha1.PipelineProgress{
			Name:     ref.Name,
			Pipeline: ref.Pipeline,
			Phase:    "Pending",
		})
	}

	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseProgressing
	release.Status.ResolvedVersion = resolvedVersion
	release.Status.PipelineProgress = progress
	release.Status.ObservedGeneration = release.Generation
	release.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Progressing", "release is resolving pipelines and targets")
	r.clearStalledCondition(release)
	r.setReconcilingCondition(release, metav1.ConditionTrue, "Progressing", "release is advancing through pipeline DAG")
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Release → Progressing")
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release phase: %w", err)
	}

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

	// CRITICAL: take snapshot BEFORE any mutations to release.Status.
	// advanceAllTargets, upsertTarget, cancelPendingStageTargets, and
	// triggerRollbackTargets all mutate release.Status in-place; one patch at the
	// bottom persists the full diff.
	patch := client.MergeFrom(release.DeepCopy())

	// Build pipeline phase map from current PipelineProgress.
	pipelinePhase := make(map[string]string, len(release.Status.PipelineProgress))
	for _, p := range release.Status.PipelineProgress {
		pipelinePhase[p.Name] = p.Phase
	}

	// Track updated progress (written back once at the end).
	updatedPipelines := make([]kaprov1alpha1.PipelineProgress, 0, len(release.Spec.Pipelines))
	allPipelinesComplete := true
	var failureMsg string

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

		stageProgress, pipelineDone, pipelineFailed, err := r.reconcilePipelineStages(
			ctx, release, pipelineRef.Name, &pipeline,
		)
		if err != nil {
			return ctrl.Result{}, err
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

		updatedPipelines = append(updatedPipelines, kaprov1alpha1.PipelineProgress{
			Name:          pipelineRef.Name,
			Pipeline:      pipelineRef.Pipeline,
			Phase:         newPhase,
			StageProgress: stageProgress,
		})

		if pipelineFailed {
			// Fail fast: mark release failed using the outer patch (which already
			// includes any target mutations from upsertTarget/cancelPendingStageTargets).
			release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
			release.Status.ObservedGeneration = release.Generation
			release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			release.Status.PipelineProgress = updatedPipelines
			r.normalizeReleaseStatus(release)
			r.setReleaseReadyCondition(release, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			r.setStalledCondition(release, "SubResourceFailed", failureMsg)
			r.setReconcilingCondition(release, metav1.ConditionFalse, "SubResourceFailed", failureMsg)
			r.Recorder.Event(release, corev1.EventTypeWarning, "Failed", failureMsg)
			release.Status.Report = r.computeReport(release, nil)
			if patchErr := r.Status().Patch(ctx, release, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch Release status on failure: %w", patchErr)
			}
			r.clearActiveRelease(ctx, release)
			return ctrl.Result{}, nil
		}
	}

	// Advance every non-terminal target by one FSM step (mutations are in-memory).
	// Capture the result — target transitions return Requeue:true so we propagate
	// the urgency instead of always waiting requeueNormal (30s).
	advResult, err := r.advanceAllTargets(ctx, release)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("advanceAllTargets: %w", err)
	}

	// Single status patch persists pipeline progress + all target FSM mutations.
	release.Status.PipelineProgress = updatedPipelines
	release.Status.ObservedGeneration = release.Generation
	r.normalizeReleaseStatus(release)

	if allPipelinesComplete {
		r.appendAuditEntry(ctx, release)
		release.Status.Phase = kaprov1alpha1.ReleasePhaseComplete
		release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		r.setReleaseReadyCondition(release, metav1.ConditionTrue, "Complete", "all pipelines complete")
		r.clearStalledCondition(release)
		r.setReconcilingCondition(release, metav1.ConditionFalse, "Complete", "all pipelines complete")
		r.Recorder.Event(release, corev1.EventTypeNormal, "Complete", "All pipelines complete")
	} else {
		r.setReleaseReadyCondition(release, metav1.ConditionFalse, "Progressing", releaseProgressSummary(release))
		r.clearStalledCondition(release)
		r.setReconcilingCondition(release, metav1.ConditionTrue, "Progressing", "release is advancing through pipeline DAG")
	}

	release.Status.Report = r.computeReport(release, nil)
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release status: %w", err)
	}

	if allPipelinesComplete {
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
		return ctrl.Result{}, nil
	}

	// Propagate the target FSM urgency: immediate requeue when a target just
	// transitioned, or the shortest poll interval across all active targets.
	if advResult.Requeue || (advResult.RequeueAfter > 0 && advResult.RequeueAfter < requeueNormal) {
		return advResult, nil
	}
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

// reconcilePipelineStages walks the stage DAG for one pipeline instance.
//
// For each stage whose dependencies are satisfied it:
//  1. Lists target clusters matching the stage selector.
//  2. Upserts a TargetStatus entry for each (idempotent).
//  3. Observes current target phases → derives stage phase.
//
// Returns (stageProgress, allComplete, anyFailed, error).
func (r *ReleaseReconciler) reconcilePipelineStages(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
) ([]kaprov1alpha1.StageProgress, bool, bool, error) {
	log := log.FromContext(ctx)

	// stagePhase maps stage name → "Pending"|"Progressing"|"Complete"|"Failed"
	stagePhase := make(map[string]string, len(pipeline.Spec.Stages))
	stageProgress := make([]kaprov1alpha1.StageProgress, 0, len(pipeline.Spec.Stages))

	allComplete := true
	anyFailed := false

	for _, stage := range pipeline.Spec.Stages {
		// Check stage-level dependencies.
		depsComplete := true
		for _, dep := range stage.DependsOn {
			if stagePhase[dep] != "Complete" {
				depsComplete = false
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

		// List clusters matching this stage's selector.
		envList, err := r.listTargetsForStage(ctx, stage, release)
		if err != nil {
			return nil, false, false, fmt.Errorf("list targets for stage %s: %w", stage.Name, err)
		}
		if len(envList) == 0 {
			log.Info("stage has no matching clusters — treating as complete",
				"stage", stage.Name, "pipeline", pipeline.Name, "pipelineRef", pipelineRefName)
			stagePhase[stage.Name] = "Complete"
			stageProgress = append(stageProgress, kaprov1alpha1.StageProgress{
				Name: stage.Name, Phase: "Complete", Total: 0,
			})
			continue
		}

		// Upsert target entries; observe phases.
		total, synced, failed := 0, 0, 0
		for _, target := range envList {
			total++
			i := r.upsertTarget(release, pipelineRefName, pipeline, stage, target)
			phase := release.Status.Targets[i].Phase

			switch phase {
			case kaprov1alpha1.SyncPhaseConverged:
				synced++
			case kaprov1alpha1.SyncPhaseFailed:
				failed++
			}
		}

		// Derive stage phase from target observations.
		var sp kaprov1alpha1.StageProgress
		sp.Name = stage.Name
		sp.Total = total
		sp.Synced = synced
		sp.Failed = failed

		if failed > 0 {
			onFailure := stage.OnFailure
			switch onFailure {
			case kaprov1alpha1.StageFailurePolicySkip:
				log.Info("stage has failed targets with OnFailure=skip, treating as complete",
					"stage", stage.Name, "pipelineRef", pipelineRefName, "failed", failed)
				sp.Phase = "Complete"
				stagePhase[stage.Name] = "Complete"
			case kaprov1alpha1.StageFailurePolicyRollback:
				log.Info("stage has failed targets with OnFailure=rollback",
					"stage", stage.Name, "pipelineRef", pipelineRefName)
				r.triggerRollbackTargets(ctx, release, pipelineRefName, stage.Name)
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
			default: // halt
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
				r.cancelPendingStageTargets(ctx, release, pipelineRefName, stage.Name)
			}
		} else if synced == total {
			sp.Phase = "Complete"
			stagePhase[stage.Name] = "Complete"
		} else {
			sp.Phase = "Progressing"
			stagePhase[stage.Name] = "Progressing"
			allComplete = false
		}

		stageProgress = append(stageProgress, sp)

		if anyFailed {
			break // fail fast within a pipeline
		}
	}

	return stageProgress, allComplete, anyFailed, nil
}

// listTargetsForStage returns all MemberClusters that match the stage selector,
// filtered to spec.scope.targets when a scope is set on the Release.
func (r *ReleaseReconciler) listTargetsForStage(ctx context.Context, stage kaprov1alpha1.Stage, release *kaprov1alpha1.Release) ([]kaprov1alpha1.MemberCluster, error) {
	var mcList kaprov1alpha1.MemberClusterList
	listOpts := []client.ListOption{}
	sel, err := metav1.LabelSelectorAsSelector(&stage.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid stage selector: %w", err)
	}
	listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
	if err := r.List(ctx, &mcList, listOpts...); err != nil {
		return nil, err
	}
	clusters := mcList.Items

	// Apply scope filter when an explicit cluster allowlist is provided.
	if release.Spec.Scope != nil && len(release.Spec.Scope.Targets) > 0 {
		allowed := make(map[string]struct{}, len(release.Spec.Scope.Targets))
		for _, t := range release.Spec.Scope.Targets {
			allowed[t] = struct{}{}
		}
		filtered := clusters[:0]
		for _, mc := range clusters {
			if _, ok := allowed[mc.Name]; ok {
				filtered = append(filtered, mc)
			}
		}
		if len(filtered) == 0 && len(clusters) > 0 {
			log.FromContext(ctx).Info("scope filter eliminated all clusters for stage — treating as no-op",
				"stage", stage.Name, "scopeTargets", release.Spec.Scope.Targets)
		}
		clusters = filtered
	}

	return clusters, nil
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
) int {
	key := syncKey(pipelineRefName, stage.Name, mc.Name)
	for i, target := range release.Status.Targets {
		if syncKey(target.PipelineRef, target.Stage, target.Target) == key {
			return i
		}
	}
	newTarget := kaprov1alpha1.TargetStatus{
		ReleaseRef:  release.Name,
		Target:      mc.Name,
		PipelineRef: pipelineRefName,
		Pipeline:    pipeline.Name,
		Stage:       stage.Name,
		Version:     release.Status.ResolvedVersion,
		Gate:        stage.Gate,
		AppKey:      releaseAppKey(release),
	}
	release.Status.Targets = append(release.Status.Targets, newTarget)
	return len(release.Status.Targets) - 1
}

// triggerRollbackTargets appends rollback TargetStatus entries for every
// Converged target in the given pipeline/stage. In-memory only; caller patches.
func (r *ReleaseReconciler) triggerRollbackTargets(ctx context.Context, release *kaprov1alpha1.Release, pipelineRefName, stageName string) {
	n := len(release.Status.Targets) // capture length before appending
	for i := 0; i < n; i++ {
		target := &release.Status.Targets[i]
		if target.PipelineRef != pipelineRefName || target.Stage != stageName {
			continue
		}
		if target.Phase != kaprov1alpha1.SyncPhaseConverged {
			continue
		}
		r.triggerEnvRollback(ctx, release, i)
	}
}

// cancelPendingStageTargets marks every non-terminal target in the stage as Failed
// in-memory. This implements failurePolicy: halt — sibling targets stop advancing.
func (r *ReleaseReconciler) cancelPendingStageTargets(_ context.Context, release *kaprov1alpha1.Release, pipelineRefName, stageName string) {
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range release.Status.Targets {
		target := &release.Status.Targets[i]
		if target.PipelineRef != pipelineRefName || target.Stage != stageName {
			continue
		}
		switch target.Phase {
		case kaprov1alpha1.SyncPhaseConverged, kaprov1alpha1.SyncPhaseFailed:
			continue
		}
		target.Phase = kaprov1alpha1.SyncPhaseFailed
		target.Message = "cancelled: stage halted due to peer failure (failurePolicy: halt)"
		target.FinishedAt = now
	}
}

// clearActiveRelease clears mc.status.activeRelease for all MemberClusters
// targeted by this Release, found via release.Status.Targets.
func (r *ReleaseReconciler) clearActiveRelease(ctx context.Context, release *kaprov1alpha1.Release) {
	log := log.FromContext(ctx)
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

// handleDeletion clears MemberCluster activeRelease references and removes the finalizer.
// Targets are inline status — nothing to delete externally.
func (r *ReleaseReconciler) handleDeletion(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling Release deletion", "name", release.Name)

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

	// Index Releases by every active cluster in status.targets — used to
	// map MemberCluster changes back to only the affected Release(s).
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Release{}, IndexKeyActiveCluster,
		activeClusterExtractor,
	); err != nil {
		return fmt.Errorf("index Release by %s: %w", IndexKeyActiveCluster, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha1.Release{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Watch MemberClusters — if a new cluster is registered whose labels match
		// an in-progress stage, wake up the Release so a new target entry is created.
		Watches(
			&kaprov1alpha1.MemberCluster{},
			handler.EnqueueRequestsFromMapFunc(r.releasesForMemberCluster),
		).
		// Watch Approvals — when an Approval CR is created for a WaitingApproval target,
		// wake up the Release so the target can advance to Applying.
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(approvalForRelease),
		).
		Complete(r)
}

func (r *ReleaseReconciler) releasesForMemberCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha1.MemberCluster)
	if !ok {
		return nil
	}
	// Use the active-cluster field index to find only releases that reference this
	// specific cluster. This avoids scanning the entire Release fleet on every
	// MemberCluster status update.
	var releaseList kaprov1alpha1.ReleaseList
	if err := r.List(ctx, &releaseList,
		client.MatchingFields{IndexKeyActiveCluster: mc.Name},
	); err != nil {
		return nil
	}
	if len(releaseList.Items) == 0 {
		return r.progressingReleasesForNewCluster(ctx, mc)
	}
	reqs := make([]ctrl.Request, 0, len(releaseList.Items))
	for i := range releaseList.Items {
		rel := &releaseList.Items[i]
		if rel.Status.Phase == kaprov1alpha1.ReleasePhaseComplete || rel.Status.Phase == kaprov1alpha1.ReleasePhaseFailed {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rel)})
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
	if err := r.List(ctx, &releaseList); err != nil {
		return nil
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

// syncKey builds a unique map key for one target rollout entry:
// <pipelineRefName>/<stage>/<target>.
func syncKey(pipelineRefName, stage, target string) string {
	return pipelineRefName + "/" + stage + "/" + target
}

// syncName builds the deterministic name for one target rollout entry.
// Format: <release>-<pipelineRef>-<stage>-<target>
func syncName(release, pipelineRef, stage, target string) string {
	return fmt.Sprintf("%s-%s-%s-%s", release, pipelineRef, stage, target)
}

// releaseAppKey returns the key used in MemberCluster.status.currentVersions.
func releaseAppKey(release *kaprov1alpha1.Release) string {
	if release.Spec.AppKey != "" {
		return release.Spec.AppKey
	}
	return release.Spec.Artifact
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
		if target.Phase != kaprov1alpha1.SyncPhaseConverged && target.Phase != kaprov1alpha1.SyncPhaseFailed {
			activeTargets++
		}
	}

	return fmt.Sprintf("release progressing: %d active pipelines, %d active targets", activePipelines, activeTargets)
}

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

	if len(normalized) > maxReleaseTargetRows {
		normalized = normalized[len(normalized)-maxReleaseTargetRows:]
	}
	release.Status.Targets = normalized
}

func (r *ReleaseReconciler) normalizeTargetEntry(target *kaprov1alpha1.TargetStatus) {
	if len(target.Gates) > maxGateRunsPerTarget {
		target.Gates = target.Gates[len(target.Gates)-maxGateRunsPerTarget:]
	}
	for i := range target.Gates {
		if len(target.Gates[i].Results) > maxGateResultsPerGateRun {
			target.Gates[i].Results = target.Gates[i].Results[:maxGateResultsPerGateRun]
		}
	}
}

// appendAuditEntry records the delivery provenance of a completed Release in
// Release.status.auditTrail. It is idempotent — an entry for the same Release
// artifact is only appended once. AuditTrail is capped at 50 entries (oldest trimmed).
// This method modifies release.Status.AuditTrail in-place; the caller must include
// release in a status patch for the change to persist.
func (r *ReleaseReconciler) appendAuditEntry(ctx context.Context, release *kaprov1alpha1.Release) {
	// Idempotency: already have an entry for this artifact.
	for _, e := range release.Status.AuditTrail {
		if e.Release == release.Name && e.Artifact == release.Spec.Artifact {
			return
		}
	}

	// Fetch the artifact to capture lineage fields (best-effort).
	var derivedFrom string
	var changedComponents []string
	if release.Spec.Artifact != "" {
		var artifact kaprov1alpha1.Artifact
		if err := r.Get(ctx, types.NamespacedName{Name: release.Spec.Artifact}, &artifact); err == nil {
			derivedFrom = artifact.Spec.Metadata.DerivedFrom
			changedComponents = artifact.Spec.Metadata.ChangedComponents
		}
	}

	var scope []string
	if release.Spec.Scope != nil {
		scope = release.Spec.Scope.Targets
	}

	entry := kaprov1alpha1.AuditEntry{
		Artifact:           release.Spec.Artifact,
		Release:            release.Name,
		DerivedFrom:        derivedFrom,
		ReleaseDerivedFrom: release.Spec.DerivedFrom,
		ChangedComponents:  changedComponents,
		Scope:              scope,
		CompletedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	release.Status.AuditTrail = append(release.Status.AuditTrail, entry)

	const maxAuditTrail = 50
	if len(release.Status.AuditTrail) > maxAuditTrail {
		release.Status.AuditTrail = release.Status.AuditTrail[len(release.Status.AuditTrail)-maxAuditTrail:]
	}
}

// computeReport builds the inline ReleaseReportSummary from Release.status.targets.
// It replaces the standalone ReleaseReport CRD/controller. The result should be stored
// in release.Status.Report before the status patch.
func (r *ReleaseReconciler) computeReport(release *kaprov1alpha1.Release, approvals []kaprov1alpha1.Approval) kaprov1alpha1.ReleaseReportSummary {
	now := time.Now().UTC()

	st := kaprov1alpha1.ReleaseReportSummary{
		Phase:           release.Status.Phase,
		Artifact:        release.Spec.Artifact,
		ResolvedVersion: release.Status.ResolvedVersion,
		StartedAt:       release.Status.StartedAt,
		CompletedAt:     release.Status.CompletedAt,
	}

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

	// Count targets from inline status.
	targetPhases := make(map[string]kaprov1alpha1.SyncPhase, len(release.Status.Targets))
	var rolledBack int
	for _, target := range release.Status.Targets {
		if target.Rollback {
			rolledBack++
			continue
		}
		targetPhases[target.Target] = target.Phase
	}

	var totalTargets, synced, failed, pending int
	for _, phase := range targetPhases {
		totalTargets++
		switch phase {
		case kaprov1alpha1.SyncPhaseConverged:
			synced++
		case kaprov1alpha1.SyncPhaseFailed:
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

	targetReports := make([]kaprov1alpha1.TargetReport, 0, len(release.Status.Targets))
	seen := make(map[string]bool)
	for _, target := range release.Status.Targets {
		if target.Rollback || seen[target.Target] {
			continue
		}
		seen[target.Target] = true
		targetReports = append(targetReports, kaprov1alpha1.TargetReport{
			Name:        target.Target,
			Phase:       string(target.Phase),
			PipelineRef: target.Pipeline,
			Stage:       target.Stage,
			Version:     target.Version,
			SyncedAt:    target.FinishedAt,
		})
	}
	st.Targets = targetReports

	gateReports := make([]kaprov1alpha1.GateReport, 0)
	for _, target := range release.Status.Targets {
		if target.Gate == nil {
			continue
		}
		var result string
		switch target.Phase {
		case kaprov1alpha1.SyncPhaseConverged:
			result = "Passed"
		case kaprov1alpha1.SyncPhaseFailed:
			result = "Failed"
		case kaprov1alpha1.SyncPhaseMetricsCheck, kaprov1alpha1.SyncPhaseSoaking,
			kaprov1alpha1.SyncPhaseVerification, kaprov1alpha1.SyncPhaseHealthCheck:
			result = "Running"
		default:
			result = "Pending"
		}
		gateReports = append(gateReports, kaprov1alpha1.GateReport{
			Type:        target.Stage,
			PipelineRef: target.Pipeline,
			Stage:       target.Stage,
			Target:      target.Target,
			Result:      result,
		})
	}
	st.Gates = gateReports

	pendingApprovals := make([]string, 0)
	for _, a := range approvals {
		if a.Spec.Kind == kaprov1alpha1.ApprovalKindStage || a.Spec.Kind == kaprov1alpha1.ApprovalKindSync {
			pendingApprovals = append(pendingApprovals, a.Name)
		}
	}
	st.PendingApprovals = pendingApprovals

	return st
}

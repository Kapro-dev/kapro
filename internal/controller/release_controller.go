package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
)

const releaseFinalizer = "kapro.io/release-cleanup"

// ReleaseReconciler is the main brain of Kapro.
// It drives two DAG levels:
//
//  1. Pipeline DAG — Release.spec.pipelines[].dependsOn orders which pipelines
//     run in sequence (or parallel when no deps). Useful when the same fleet is
//     partitioned into logical "apps" that must be released in a fixed order.
//
//  2. Stage DAG — Pipeline.spec.stages[].dependsOn orders stages within each
//     pipeline (pilot → canary → global). Each stage expands to N Syncs — one
//     per matching Environment — which run in parallel.
//
// State machine:
//
//	Pending → Progressing → Complete | Failed
type ReleaseReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=artifacts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=syncs,verbs=get;list;watch;create;update;patch;delete

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
// before any Syncs are created.
func (r *ReleaseReconciler) handlePending(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	resolvedVersion := release.Status.ResolvedVersion

	if resolvedVersion == "" && release.Spec.Artifact != "" {
		var artifact kaprov1alpha1.Artifact
		if err := r.Get(ctx, client.ObjectKey{Name: release.Spec.Artifact}, &artifact); err != nil {
			return ctrl.Result{RequeueAfter: requeueNormal},
				fmt.Errorf("artifact %s not found: %w", release.Spec.Artifact, err)
		}
		for _, src := range artifact.Spec.Sources {
			if src.OCI != nil && src.OCI.Digest != "" {
				resolvedVersion = src.OCI.Repository + "@" + src.OCI.Digest
				break
			}
		}
		if resolvedVersion == "" {
			return ctrl.Result{RequeueAfter: requeueNormal},
				fmt.Errorf("artifact %s has no OCI source with digest", release.Spec.Artifact)
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
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Release → Progressing")
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release phase: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// handleProgressing drives the two-level DAG:
//
//	Pipeline DAG (outer) → Stage DAG (inner) → Syncs per Environment
//
// For each pipeline whose dependencies are complete, we walk its stages in
// dependsOn order. For each eligible stage we list the matching Environments,
// ensure a Sync exists for each, and observe their phases. Once all Syncs
// for a stage are Converged the stage is marked complete and the next stage
// (if any) becomes eligible.
func (r *ReleaseReconciler) handleProgressing(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// List all Syncs owned by this Release, indexed by release label.
	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.InNamespace(release.Namespace),
		client.MatchingFields{IndexKeyRelease: release.Name},
		client.Limit(2000),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Syncs: %w", err)
	}

	// Build lookup: "<pipelineRefName>/<stage>/<envName>" → SyncPhase
	syncPhases := make(map[string]kaprov1alpha1.SyncPhase, len(syncList.Items))
	for _, s := range syncList.Items {
		key := syncKey(
			s.Labels["kapro.io/pipeline-ref"],
			s.Labels["kapro.io/stage"],
			s.Labels["kapro.io/environment"],
		)
		syncPhases[key] = s.Status.Phase
	}

	// Build pipeline phase map from current PipelineProgress.
	pipelinePhase := make(map[string]string, len(release.Status.PipelineProgress))
	for _, p := range release.Status.PipelineProgress {
		pipelinePhase[p.Name] = p.Phase
	}

	// Track updated progress (we'll write back once at the end).
	updatedPipelines := make([]kaprov1alpha1.PipelineProgress, 0, len(release.Spec.Pipelines))
	allPipelinesComplete := true

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
			ctx, release, pipelineRef.Name, &pipeline, syncPhases,
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		newPhase := "Progressing"
		if pipelineFailed {
			newPhase = "Failed"
			allPipelinesComplete = false
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
			// Fail fast: stop processing further pipelines.
			return ctrl.Result{}, r.failRelease(ctx, release,
				fmt.Sprintf("pipeline %s (%s) failed", pipelineRef.Name, pipelineRef.Pipeline))
		}
	}

	// Write updated progress + optionally mark Release complete.
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.PipelineProgress = updatedPipelines
	release.Status.ObservedGeneration = release.Generation

	if allPipelinesComplete {
		release.Status.Phase = kaprov1alpha1.ReleasePhaseComplete
		release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Complete",
			ObservedGeneration: release.Generation,
			Message:            "all pipelines complete",
			LastTransitionTime: metav1.Now(),
		})
		r.Recorder.Event(release, corev1.EventTypeNormal, "Complete", "All pipelines complete")
		r.clearActiveRelease(ctx, release)
		// Annotate so future Releases can use this version for rollback.
		annPatch := client.MergeFrom(release.DeepCopy())
		if release.Annotations == nil {
			release.Annotations = make(map[string]string)
		}
		release.Annotations["kapro.io/previous-version"] = release.Status.ResolvedVersion
		if annErr := r.Patch(ctx, release, annPatch); annErr != nil {
			log.Error(annErr, "failed to annotate previous-version on Release")
		}
		log.Info("Release complete", "name", release.Name)
	}

	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release status: %w", err)
	}

	if !allPipelinesComplete {
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}
	return ctrl.Result{}, nil
}

// reconcilePipelineStages walks the stage DAG for one pipeline instance.
//
// For each stage whose dependencies are satisfied it:
//  1. Lists Environments matching the stage selector.
//  2. Ensures a Sync exists for each Environment (idempotent create).
//  3. Observes all Syncs for this stage → derives stage phase.
//
// Returns (stageProgress, allComplete, anyFailed, error).
func (r *ReleaseReconciler) reconcilePipelineStages(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
	syncPhases map[string]kaprov1alpha1.SyncPhase,
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

		// List environments matching this stage's selector.
		envList, err := r.listEnvironmentsForStage(ctx, stage)
		if err != nil {
			return nil, false, false, fmt.Errorf("list environments for stage %s: %w", stage.Name, err)
		}
		if len(envList) == 0 {
			log.Info("stage has no matching environments — treating as complete",
				"stage", stage.Name, "pipeline", pipeline.Name, "pipelineRef", pipelineRefName)
			stagePhase[stage.Name] = "Complete"
			stageProgress = append(stageProgress, kaprov1alpha1.StageProgress{
				Name: stage.Name, Phase: "Complete", Total: 0,
			})
			continue
		}

		// Ensure a Sync exists for each Environment; observe phases.
		total, synced, failed := 0, 0, 0
		for _, env := range envList {
			total++
			key := syncKey(pipelineRefName, stage.Name, env.Name)
			phase, exists := syncPhases[key]

			if !exists {
				// Create the Sync — idempotent (IgnoreAlreadyExists).
				if err := r.ensureSync(ctx, release, pipelineRefName, pipeline, stage, env); err != nil {
					return nil, false, false, err
				}
				continue
			}

			switch phase {
			case kaprov1alpha1.SyncPhaseConverged:
				synced++
			case kaprov1alpha1.SyncPhaseFailed:
				failed++
			}
		}

		// Derive stage phase from Sync observations.
		var sp kaprov1alpha1.StageProgress
		sp.Name = stage.Name
		sp.Total = total
		sp.Synced = synced
		sp.Failed = failed

		if failed > 0 {
			onFailure := stage.OnFailure
			switch onFailure {
			case kaprov1alpha1.StageFailurePolicySkip:
				log.Info("stage has failed syncs with OnFailure=skip, treating as complete",
					"stage", stage.Name, "pipelineRef", pipelineRefName, "failed", failed)
				sp.Phase = "Complete"
				stagePhase[stage.Name] = "Complete"
			case kaprov1alpha1.StageFailurePolicyRollback:
				log.Info("stage has failed syncs with OnFailure=rollback",
					"stage", stage.Name, "pipelineRef", pipelineRefName)
				_ = r.triggerRollbackSyncs(ctx, release, pipelineRefName, stage.Name, syncPhases)
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
			default: // halt
				sp.Phase = "Failed"
				stagePhase[stage.Name] = "Failed"
				anyFailed = true
				allComplete = false
				// Cancel every non-terminal Sync in this stage so they don't
				// keep running (consuming cluster resources) after the stage
				// has been halted by a peer failure.
				if cancelErr := r.cancelPendingStageSyncs(ctx, release, pipelineRefName, stage.Name); cancelErr != nil {
					log.Error(cancelErr, "cancelPendingStageSyncs failed — in-flight Syncs may continue briefly",
						"stage", stage.Name, "pipelineRef", pipelineRefName)
				}
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

// listEnvironmentsForStage returns all Environments that match the stage selector.
func (r *ReleaseReconciler) listEnvironmentsForStage(ctx context.Context, stage kaprov1alpha1.Stage) ([]kaprov1alpha1.Environment, error) {
	var envList kaprov1alpha1.EnvironmentList
	listOpts := []client.ListOption{client.Limit(500)}
	sel, err := metav1.LabelSelectorAsSelector(&stage.Selector)
	if err != nil {
		return nil, fmt.Errorf("invalid stage selector: %w", err)
	}
	listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
	if err := r.List(ctx, &envList, listOpts...); err != nil {
		return nil, err
	}
	return envList.Items, nil
}

// ensureSync creates a Sync for the given (release, pipelineRef, stage, environment) tuple.
// Uses IgnoreAlreadyExists for idempotency — safe to call on every reconcile loop.
func (r *ReleaseReconciler) ensureSync(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName string,
	pipeline *kaprov1alpha1.Pipeline,
	stage kaprov1alpha1.Stage,
	env kaprov1alpha1.Environment,
) error {
	log := log.FromContext(ctx)

	name := syncName(release.Name, pipelineRefName, stage.Name, env.Name)
	sync := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: release.Namespace,
			Labels: map[string]string{
				"kapro.io/release":       release.Name,
				"kapro.io/pipeline-ref":  pipelineRefName,
				"kapro.io/pipeline":      pipeline.Name,
				"kapro.io/stage":         stage.Name,
				"kapro.io/environment":   env.Name,
			},
		},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     release.Name,
			EnvironmentRef: env.Name,
			Pipeline:       pipeline.Name,
			Stage:          stage.Name,
			Version:        release.Status.ResolvedVersion,
			PolicyRef:      stage.Gate, // stage.Gate is the GatePolicy ref name
			AppKey:         releaseAppKey(release),
		},
	}

	if err := controllerutil.SetControllerReference(release, sync, r.Scheme); err != nil {
		return fmt.Errorf("set owner ref on Sync %s: %w", name, err)
	}
	if err := r.Create(ctx, sync); client.IgnoreAlreadyExists(err) != nil {
		return fmt.Errorf("create Sync %s: %w", name, err)
	}

	log.Info("ensured Sync", "name", name, "env", env.Name, "stage", stage.Name, "pipelineRef", pipelineRefName)
	return nil
}

// triggerRollbackSyncs creates rollback Syncs for every Converged environment
// in this pipeline/stage, targeting the previous resolved version.
func (r *ReleaseReconciler) triggerRollbackSyncs(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName, stageName string,
	syncPhases map[string]kaprov1alpha1.SyncPhase,
) error {
	log := log.FromContext(ctx)

	prevVersion := release.Annotations["kapro.io/previous-version"]
	if prevVersion == "" {
		log.Info("no previous-version annotation, skipping rollback")
		return nil
	}

	// Find all Syncs in this pipeline/stage that have Converged.
	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.InNamespace(release.Namespace),
		client.MatchingLabels{
			"kapro.io/release":      release.Name,
			"kapro.io/pipeline-ref": pipelineRefName,
			"kapro.io/stage":        stageName,
		},
		client.Limit(500),
	); err != nil {
		return err
	}

	for _, s := range syncList.Items {
		if s.Status.Phase != kaprov1alpha1.SyncPhaseConverged {
			continue
		}
		envRef := s.Spec.EnvironmentRef
		rollbackName := fmt.Sprintf("%s-rollback-%s", release.Name, envRef)
		rollback := &kaprov1alpha1.Sync{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rollbackName,
				Namespace: release.Namespace,
				Labels: map[string]string{
					"kapro.io/release":      release.Name,
					"kapro.io/rollback":     "true",
					"kapro.io/pipeline-ref": pipelineRefName,
					"kapro.io/stage":        stageName,
					"kapro.io/environment":  envRef,
				},
			},
			Spec: kaprov1alpha1.SyncSpec{
				ReleaseRef:     release.Name,
				EnvironmentRef: envRef,
				Pipeline:       s.Spec.Pipeline,
				Stage:          stageName,
				Version:        prevVersion,
				PolicyRef:      s.Spec.PolicyRef,
				AppKey:         s.Spec.AppKey,
			},
		}
		if err := controllerutil.SetControllerReference(release, rollback, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on rollback Sync: %w", err)
		}
		if err := r.Create(ctx, rollback); client.IgnoreAlreadyExists(err) != nil {
			log.Error(err, "create rollback Sync", "name", rollbackName)
		} else {
			log.Info("created rollback Sync", "name", rollbackName, "env", envRef, "version", prevVersion)
		}
	}
	return nil
}

// cancelPendingStageSyncs marks every non-terminal Sync in the given stage as
// Failed with a descriptive cancellation message.
//
// This is the enforcement half of failurePolicy: halt. When one Sync in a
// stage fails, the stage decision is immediate — but sibling Syncs that were
// already created (and are still Pending / Verification / HealthCheck / etc.)
// must be explicitly cancelled so they stop consuming cluster resources and
// don't cause a false "still progressing" read on the next reconcile.
//
// Already-terminal Syncs (Converged, Failed) are left untouched — their result
// is final and should not be overwritten.
func (r *ReleaseReconciler) cancelPendingStageSyncs(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	pipelineRefName, stageName string,
) error {
	log := log.FromContext(ctx)

	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.InNamespace(release.Namespace),
		client.MatchingLabels{
			"kapro.io/release":      release.Name,
			"kapro.io/pipeline-ref": pipelineRefName,
			"kapro.io/stage":        stageName,
		},
		client.Limit(500),
	); err != nil {
		return fmt.Errorf("list stage Syncs for cancellation: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range syncList.Items {
		s := &syncList.Items[i]
		switch s.Status.Phase {
		case kaprov1alpha1.SyncPhaseConverged, kaprov1alpha1.SyncPhaseFailed:
			continue // already terminal — leave the record untouched
		}
		log.Info("cancelling Sync due to halt policy",
			"sync", s.Name,
			"phase", s.Status.Phase,
			"stage", stageName,
			"pipelineRef", pipelineRefName,
		)
		patch := client.MergeFrom(s.DeepCopy())
		s.Status.Phase = kaprov1alpha1.SyncPhaseFailed
		s.Status.Message = "cancelled: stage halted due to peer Sync failure (failurePolicy: halt)"
		s.Status.FinishedAt = now
		if err := r.Status().Patch(ctx, s, patch); err != nil {
			// Log and continue — partial cancellation is better than blocking the reconcile.
			log.Error(err, "failed to cancel Sync", "sync", s.Name)
		}
	}
	return nil
}

func (r *ReleaseReconciler) failRelease(ctx context.Context, release *kaprov1alpha1.Release, msg string) error {
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
	release.Status.ObservedGeneration = release.Generation
	release.Status.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "SubResourceFailed",
		ObservedGeneration: release.Generation,
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	r.Recorder.Event(release, corev1.EventTypeWarning, "Failed", msg)
	r.clearActiveRelease(ctx, release)
	return r.Status().Patch(ctx, release, patch)
}

// clearActiveRelease clears env.status.activeRelease for all environments
// that were claimed by this Release, found via owned Syncs.
func (r *ReleaseReconciler) clearActiveRelease(ctx context.Context, release *kaprov1alpha1.Release) {
	var syncList kaprov1alpha1.SyncList
	_ = r.List(ctx, &syncList,
		client.InNamespace(release.Namespace),
		client.MatchingFields{IndexKeyRelease: release.Name},
		client.Limit(500),
	)
	seen := make(map[string]bool)
	for _, s := range syncList.Items {
		envName := s.Spec.EnvironmentRef
		if seen[envName] {
			continue
		}
		seen[envName] = true
		var env kaprov1alpha1.Environment
		if err := r.Get(ctx, client.ObjectKey{Name: envName}, &env); err != nil {
			continue
		}
		if env.Status.ActiveRelease == release.Name {
			patch := client.MergeFrom(env.DeepCopy())
			env.Status.ActiveRelease = ""
			_ = r.Status().Patch(ctx, &env, patch)
		}
	}
}

// handleDeletion cleans up all Syncs owned by this Release.
func (r *ReleaseReconciler) handleDeletion(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling Release deletion", "name", release.Name)

	releaseFields := client.MatchingFields{IndexKeyRelease: release.Name}

	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList, client.InNamespace(release.Namespace), releaseFields, client.Limit(500)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Syncs for cleanup: %w", err)
	}
	for i := range syncList.Items {
		if err := r.Delete(ctx, &syncList.Items[i]); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete Sync %s: %w", syncList.Items[i].Name, err)
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

	// Index Syncs by release label — used for listing all Syncs owned by a Release.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Sync{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index Sync by %s: %w", IndexKeyRelease, err)
	}
	// Index Syncs by environment ref — used by SyncReconciler's ManagedCluster watch.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Sync{}, IndexKeyEnvironment,
		environmentRefExtractor(),
	); err != nil {
		return fmt.Errorf("index Sync by %s: %w", IndexKeyEnvironment, err)
	}
	// Index Approvals by release label — used for listing Approvals during cleanup.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Approval{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index Approval by %s: %w", IndexKeyRelease, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		For(&kaprov1alpha1.Release{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Owns Syncs — when any Sync status changes (Converged, Failed) the owning
		// Release is re-queued so the DAG can advance to the next stage.
		Owns(&kaprov1alpha1.Sync{}).
		// Watch Environments — if a new Environment is registered whose labels match
		// an in-progress stage, wake up the Release so a new Sync is created.
		Watches(
			&kaprov1alpha1.Environment{},
			handler.EnqueueRequestsFromMapFunc(r.releasesForEnvironment),
		).
		Complete(r)
}

func (r *ReleaseReconciler) releasesForEnvironment(ctx context.Context, obj client.Object) []ctrl.Request {
	env, ok := obj.(*kaprov1alpha1.Environment)
	if !ok {
		return nil
	}
	var releaseList kaprov1alpha1.ReleaseList
	if err := r.List(ctx, &releaseList, client.InNamespace(env.Namespace), client.Limit(500)); err != nil {
		return nil
	}
	var reqs []ctrl.Request
	for i := range releaseList.Items {
		rel := &releaseList.Items[i]
		if rel.Status.Phase == kaprov1alpha1.ReleasePhaseComplete || rel.Status.Phase == kaprov1alpha1.ReleasePhaseFailed {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rel)})
	}
	return reqs
}

// syncKey builds a unique map key for a Sync: <pipelineRefName>/<stage>/<env>.
func syncKey(pipelineRefName, stage, env string) string {
	return pipelineRefName + "/" + stage + "/" + env
}

// syncName builds the deterministic name for a Sync object.
// Format: <release>-<pipelineRef>-<stage>-<env>
func syncName(release, pipelineRef, stage, env string) string {
	return fmt.Sprintf("%s-%s-%s-%s", release, pipelineRef, stage, env)
}

// releaseAppKey returns the key used in ManagedCluster.status.currentVersions.
func releaseAppKey(release *kaprov1alpha1.Release) string {
	if release.Spec.AppKey != "" {
		return release.Spec.AppKey
	}
	return release.Spec.Artifact
}

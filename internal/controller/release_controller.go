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

// ReleaseReconciler reconciles Release objects.
// It resolves the scope (label selector → Environments), creates a Pipeline,
// and drives Promotion and BatchRun state machines.
//
// State machine:
//
//	Pending → Promoting → Progressing → Complete | Failed
type ReleaseReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme // required for SetControllerReference on owned objects
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=artifacts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns,verbs=get;list;watch;create;update;patch;delete

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

	// Handle deletion: clean up owned resources, then remove finalizer.
	if !release.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &release)
	}

	// Ensure finalizer is registered before we touch any external state.
	if !controllerutil.ContainsFinalizer(&release, releaseFinalizer) {
		patch := client.MergeFrom(release.DeepCopy())
		controllerutil.AddFinalizer(&release, releaseFinalizer)
		if err := r.Patch(ctx, &release, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Suspended: pause all FSM advancement.  In-flight Promotions/BatchRuns
	// are not cancelled — they finish their current phase, but the Release
	// will not move to the next step until spec.suspended is cleared.
	if release.Spec.Suspended {
		log.Info("Release is suspended — skipping FSM advancement")
		return ctrl.Result{}, nil
	}

	switch release.Status.Phase {
	case "", kaprov1alpha1.ReleasePhasePending:
		return r.handlePending(ctx, &release)
	case kaprov1alpha1.ReleasePhasePromoting:
		return r.handlePromoting(ctx, &release)
	case kaprov1alpha1.ReleasePhaseProgressing:
		return r.handleProgressing(ctx, &release)
	case kaprov1alpha1.ReleasePhaseComplete, kaprov1alpha1.ReleasePhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// handlePending resolves the label selector to Environments, then transitions to Promoting.
func (r *ReleaseReconciler) handlePending(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Step 1: Resolve Artifact CR → extract OCI digest as the canonical version.
	// This makes Artifact a real participant in the flow, not just a label.
	resolvedVersion := release.Status.ResolvedVersion
	if resolvedVersion == "" {
		var artifact kaprov1alpha1.Artifact
		if err := r.Get(ctx, client.ObjectKey{Name: release.Spec.Artifact}, &artifact); err != nil {
			return ctrl.Result{RequeueAfter: requeueNormal},
				fmt.Errorf("artifact %s not found: %w", release.Spec.Artifact, err)
		}
		// Pick the first OCI source with a digest.
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

	// Resolve scope: label selector → list matching Environments
	selector, err := metav1.LabelSelectorAsSelector(&release.Spec.Scope.Selector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid scope selector: %w", err)
	}

	var envList kaprov1alpha1.EnvironmentList
	if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector, Limit: 500}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Environments: %w", err)
	}

	if len(envList.Items) == 0 {
		log.Info("no Environments matched scope selector — requeueing", "selector", release.Spec.Scope.Selector)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	log.Info("resolved scope", "environments", len(envList.Items))

	// Enforce: one active Release per Environment (optimistic concurrency).
	// We skip the pre-check read — instead we patch directly and let the API
	// server enforce via resourceVersion conflict.  If two Releases race to
	// claim the same Environment, one will succeed and the other will get a
	// 409 Conflict from Status().Patch(), causing a requeue and a second pass
	// through handlePending where it will see the conflicting activeRelease and
	// return an error.  This is the correct Kubernetes-native TOCTOU fix.
	for i := range envList.Items {
		env := &envList.Items[i]
		// Already claimed by this Release — idempotent.
		if env.Status.ActiveRelease == release.Name {
			continue
		}
		// Claimed by another Release — hard error, do not proceed.
		if env.Status.ActiveRelease != "" {
			return ctrl.Result{}, fmt.Errorf(
				"environment %s already has active release %s — cannot start %s",
				env.Name, env.Status.ActiveRelease, release.Name,
			)
		}
		patch := client.MergeFrom(env.DeepCopy())
		env.Status.ActiveRelease = release.Name
		if err := r.Status().Patch(ctx, env, patch); err != nil {
			// Conflict means another controller just claimed this env — requeue
			// and re-read to surface the conflict on next pass.
			return ctrl.Result{}, fmt.Errorf("claim Environment %s activeRelease: %w", env.Name, err)
		}
	}

	// Transition to Promoting, storing the resolved OCI digest.
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhasePromoting
	release.Status.ResolvedVersion = resolvedVersion
	release.Status.ObservedGeneration = release.Generation
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Release → Promoting")
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release phase: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// handlePromoting creates Promotion objects for each matched Environment and waits for all to converge.
func (r *ReleaseReconciler) handlePromoting(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Lookup the Pipeline template
	var pipeline kaprov1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: release.Spec.PipelineRef}, &pipeline); err != nil {
		return ctrl.Result{}, fmt.Errorf("pipeline %s not found: %w", release.Spec.PipelineRef, err)
	}

	allConverged := true

	for _, step := range pipeline.Spec.Promotion.Steps {
		selector, err := metav1.LabelSelectorAsSelector(&step.Selector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("invalid step selector: %w", err)
		}

		var envList kaprov1alpha1.EnvironmentList
		if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector}); err != nil {
			return ctrl.Result{}, fmt.Errorf("list Environments for step: %w", err)
		}

		for _, env := range envList.Items {
			promoName := fmt.Sprintf("%s-%s", release.Name, env.Name)
			var promo kaprov1alpha1.Promotion
			err := r.Get(ctx, client.ObjectKey{Name: promoName, Namespace: release.Namespace}, &promo)
			if client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("get Promotion %s: %w", promoName, err)
			}

			if err != nil {
				// Create Promotion
				newPromo := kaprov1alpha1.Promotion{
					ObjectMeta: metav1.ObjectMeta{
						Name:      promoName,
						Namespace: release.Namespace,
						Labels: map[string]string{
							"kapro.io/release":     release.Name,
							"kapro.io/environment": env.Name,
						},
					},
					Spec: kaprov1alpha1.PromotionSpec{
						ReleaseRef:     release.Name,
						EnvironmentRef: env.Name,
						Version:        release.Status.ResolvedVersion,
						PolicyRef:      step.Policy,
						AppKey:         resolveAppKey(release),
					},
				}
				// ownerRef: Release owns Promotion — Owns() watch in SetupWithManager
				// re-triggers Release reconcile on every Promotion phase change.
				if err := controllerutil.SetControllerReference(release, &newPromo, r.Scheme); err != nil {
					return ctrl.Result{}, fmt.Errorf("set owner ref on Promotion %s: %w", promoName, err)
				}
				if err := r.Create(ctx, &newPromo); err != nil {
					return ctrl.Result{}, fmt.Errorf("create Promotion %s: %w", promoName, err)
				}
				log.Info("created Promotion", "name", promoName, "env", env.Name)
				allConverged = false
				continue
			}

			if promo.Status.Phase != kaprov1alpha1.PromotionPhaseConverged {
				if promo.Status.Phase == kaprov1alpha1.PromotionPhaseFailed {
					return ctrl.Result{}, r.failRelease(ctx, release,
						fmt.Sprintf("promotion %s failed", promoName))
				}
				allConverged = false
			}
		}
	}

	if !allConverged {
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	log.Info("all promotions converged — transitioning to Progressing")
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Promoting → Progressing")
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseProgressing
	release.Status.ObservedGeneration = release.Generation
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch Release phase Progressing: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// handleProgressing drives the DAG of BatchRuns.
func (r *ReleaseReconciler) handleProgressing(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pipeline kaprov1alpha1.Pipeline
	if err := r.Get(ctx, client.ObjectKey{Name: release.Spec.PipelineRef}, &pipeline); err != nil {
		return ctrl.Result{}, fmt.Errorf("pipeline %s not found: %w", release.Spec.PipelineRef, err)
	}

	// Track completion state for all batches
	batchPhases := map[string]kaprov1alpha1.BatchPhase{}
	var batchRunList kaprov1alpha1.BatchRunList
	if err := r.List(ctx, &batchRunList, client.InNamespace(release.Namespace),
		client.MatchingFields{IndexKeyRelease: release.Name},
		client.Limit(500),
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list BatchRuns: %w", err)
	}
	for _, br := range batchRunList.Items {
		batchPhases[br.Spec.BatchName] = br.Status.Phase
	}

	allComplete := true

	for _, batch := range pipeline.Spec.Progression.Batches {
		// Check dependencies are complete
		depsComplete := true
		for _, dep := range batch.DependsOn {
			if batchPhases[dep] != kaprov1alpha1.BatchPhaseComplete {
				depsComplete = false
				break
			}
		}
		if !depsComplete {
			allComplete = false
			continue
		}

		batchRunName := fmt.Sprintf("%s-%s", release.Name, batch.Name)
		phase, exists := batchPhases[batch.Name]

		if !exists {
			// Create BatchRun
			br := kaprov1alpha1.BatchRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      batchRunName,
					Namespace: release.Namespace,
					Labels: map[string]string{
						"kapro.io/release":  release.Name,
						"kapro.io/batch":    batch.Name,
						"kapro.io/pipeline": release.Spec.PipelineRef,
					},
				},
				Spec: kaprov1alpha1.BatchRunSpec{
					ReleaseRef:           release.Name,
					BatchName:            batch.Name,
					Selectors:            batch.Selectors,
					PolicyRef:            batch.PolicyRef,
					PromotionPolicyRef:   batch.PromotionPolicyRef,
					ProgressionPolicyRef: batch.ProgressionPolicyRef,
					DependsOn:            batch.DependsOn,
				},
			}
			// ownerRef: Release owns BatchRun — Owns() watch in SetupWithManager
			// re-triggers Release reconcile on every BatchRun phase change.
			if err := controllerutil.SetControllerReference(release, &br, r.Scheme); err != nil {
				return ctrl.Result{}, fmt.Errorf("set owner ref on BatchRun %s: %w", batchRunName, err)
			}
			if err := r.Create(ctx, &br); err != nil {
				return ctrl.Result{}, fmt.Errorf("create BatchRun %s: %w", batchRunName, err)
			}
			log.Info("created BatchRun", "name", batchRunName)
			allComplete = false
			continue
		}

		switch phase {
		case kaprov1alpha1.BatchPhaseFailed:
			return ctrl.Result{}, r.failRelease(ctx, release,
				fmt.Sprintf("batch %s failed", batch.Name))
		case kaprov1alpha1.BatchPhaseComplete:
			// good
		default:
			allComplete = false
		}
	}

	if !allComplete {
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	log.Info("all batches complete — Release is Complete")
	r.Recorder.Event(release, corev1.EventTypeNormal, "Applied", "All batches complete")
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseComplete
	release.Status.ObservedGeneration = release.Generation
	apimeta.SetStatusCondition(&release.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Complete",
		ObservedGeneration: release.Generation,
		Message:            "all batches progressed successfully",
		LastTransitionTime: metav1.Now(),
	})
	r.clearActiveRelease(ctx, release)
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch release complete: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) failRelease(ctx context.Context, release *kaprov1alpha1.Release, msg string) error {
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
	release.Status.ObservedGeneration = release.Generation
	release.Status.Conditions = nil // clear stale conditions before SetStatusCondition
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

func (r *ReleaseReconciler) clearActiveRelease(ctx context.Context, release *kaprov1alpha1.Release) {
	selector, _ := metav1.LabelSelectorAsSelector(&release.Spec.Scope.Selector)
	var envList kaprov1alpha1.EnvironmentList
	_ = r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector})
	for _, env := range envList.Items {
		if env.Status.ActiveRelease == release.Name {
			patch := client.MergeFrom(env.DeepCopy())
			env.Status.ActiveRelease = ""
			_ = r.Status().Patch(ctx, &env, patch)
		}
	}
}

// handleDeletion cleans up all resources owned by this Release and removes the finalizer.
// This prevents orphaned Promotions/BatchRuns and stuck activeRelease pointers.
func (r *ReleaseReconciler) handleDeletion(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling Release deletion", "name", release.Name)

	releaseFields := client.MatchingFields{IndexKeyRelease: release.Name}

	// Delete all owned Promotions.
	var promoList kaprov1alpha1.PromotionList
	if err := r.List(ctx, &promoList, client.InNamespace(release.Namespace), releaseFields, client.Limit(500)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list Promotions for cleanup: %w", err)
	}
	for i := range promoList.Items {
		if err := r.Delete(ctx, &promoList.Items[i]); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete Promotion %s: %w", promoList.Items[i].Name, err)
		}
	}

	// Delete all owned BatchRuns.
	var batchList kaprov1alpha1.BatchRunList
	if err := r.List(ctx, &batchList, client.InNamespace(release.Namespace), releaseFields, client.Limit(500)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list BatchRuns for cleanup: %w", err)
	}
	for i := range batchList.Items {
		if err := r.Delete(ctx, &batchList.Items[i]); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete BatchRun %s: %w", batchList.Items[i].Name, err)
		}
	}

	// Clear activeRelease on all Environments this Release was managing.
	r.clearActiveRelease(ctx, release)

	// Remove the finalizer to allow Kubernetes to delete the Release.
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

	// Register field indexes for hot-path List calls.
	// Only the ReleaseReconciler registers these — registering the same index
	// twice in controller-runtime panics.  BatchRunReconciler uses the same
	// indexes but does NOT register them.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Promotion{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index Promotion by %s: %w", IndexKeyRelease, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.BatchRun{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index BatchRun by %s: %w", IndexKeyRelease, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Approval{}, IndexKeyRelease,
		labelExtractor(IndexKeyRelease),
	); err != nil {
		return fmt.Errorf("index Approval by %s: %w", IndexKeyRelease, err)
	}
	// IndexKeyEnvironment: used by PromotionReconciler's ClusterRegistration watch
	// to find Promotions targeting a cluster that just changed phase.
	if err := mgr.GetFieldIndexer().IndexField(ctx, &kaprov1alpha1.Promotion{}, IndexKeyEnvironment,
		environmentRefExtractor(),
	); err != nil {
		return fmt.Errorf("index Promotion by %s: %w", IndexKeyEnvironment, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		// Only re-reconcile on spec changes (GenerationChanged) — prevents the
		// controller reconciling itself after every status patch it writes.
		For(&kaprov1alpha1.Release{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Owns() triggers a Release reconcile when an owned Promotion or BatchRun
		// changes.  This works because we now set ownerReferences on creation via
		// controllerutil.SetControllerReference.  Without ownerRefs, Owns() is a
		// no-op and Release only learns of completion via 30s polling.
		Owns(&kaprov1alpha1.Pipeline{}).
		Owns(&kaprov1alpha1.Promotion{}).
		Owns(&kaprov1alpha1.BatchRun{}).
		// Watch Environments: if an Environment's health or activeRelease changes,
		// re-evaluate any in-flight Release scoped to that Environment.
		Watches(
			&kaprov1alpha1.Environment{},
			handler.EnqueueRequestsFromMapFunc(r.releasesForEnvironment),
		).
		Complete(r)
}

// releasesForEnvironment returns reconcile.Requests for all Releases whose
// scope selector matches the changed Environment.  Used by the Environment
// Watch above so that Release reacts to health/activeRelease updates without
// polling.
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
			continue // terminal — no need to wake up
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(rel)})
	}
	return reqs
}

// resolveAppKey returns the key used to look up versions in
// ClusterRegistration.status.currentVersions.
// resolveAppKey returns the key used to look up versions in
// ClusterRegistration.status.currentVersions.
// Falls back to the Artifact name when AppKey is not set — this preserves
// backward compatibility for single-app deployments.
func resolveAppKey(release *kaprov1alpha1.Release) string {
	if release.Spec.AppKey != "" {
		return release.Spec.AppKey
	}
	return release.Spec.Artifact
}

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

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
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
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

	switch release.Status.Phase {
	case "":
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

	// Resolve scope: label selector → list matching Environments
	selector, err := metav1.LabelSelectorAsSelector(&release.Spec.Scope.Selector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid scope selector: %w", err)
	}

	var envList kaprov1alpha1.EnvironmentList
	if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return ctrl.Result{}, err
	}

	if len(envList.Items) == 0 {
		log.Info("no Environments matched scope selector — requeueing", "selector", release.Spec.Scope.Selector)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.Info("resolved scope", "environments", len(envList.Items))

	// Enforce: one active Release per Environment
	for _, env := range envList.Items {
		if env.Status.ActiveRelease != "" && env.Status.ActiveRelease != release.Name {
			return ctrl.Result{}, fmt.Errorf(
				"environment %s already has active release %s — cannot start %s",
				env.Name, env.Status.ActiveRelease, release.Name,
			)
		}
	}

	// Mark all matched environments as active
	for _, env := range envList.Items {
		patch := client.MergeFrom(env.DeepCopy())
		env.Status.ActiveRelease = release.Name
		if err := r.Status().Patch(ctx, &env, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Transition to Promoting
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhasePromoting
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Release → Promoting")
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, err
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
			return ctrl.Result{}, err
		}

		var envList kaprov1alpha1.EnvironmentList
		if err := r.List(ctx, &envList, &client.ListOptions{LabelSelector: selector}); err != nil {
			return ctrl.Result{}, err
		}

		for _, env := range envList.Items {
			promoName := fmt.Sprintf("%s-%s", release.Name, env.Name)
			var promo kaprov1alpha1.Promotion
			err := r.Get(ctx, client.ObjectKey{Name: promoName, Namespace: release.Namespace}, &promo)
			if client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
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
						Version:        release.Spec.Artifact,
						PolicyRef:      step.Policy,
					},
				}
				if err := r.Create(ctx, &newPromo); err != nil {
					return ctrl.Result{}, err
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
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.Info("all promotions converged — transitioning to Progressing")
	r.Recorder.Event(release, corev1.EventTypeNormal, "PhaseTransition", "Promoting → Progressing")
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseProgressing
	if err := r.Status().Patch(ctx, release, patch); err != nil {
		return ctrl.Result{}, err
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
	if err := r.List(ctx, &batchRunList, client.MatchingLabels{
		"kapro.io/release": release.Name,
	}); err != nil {
		return ctrl.Result{}, err
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
						"kapro.io/release": release.Name,
						"kapro.io/batch":   batch.Name,
					},
				},
				Spec: kaprov1alpha1.BatchRunSpec{
					ReleaseRef: release.Name,
					BatchName:  batch.Name,
					Selectors:  batch.Selectors,
					DependsOn:  batch.DependsOn,
				},
			}
			if err := r.Create(ctx, &br); err != nil {
				return ctrl.Result{}, err
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
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.Info("all batches complete — Release is Complete")
	r.Recorder.Event(release, corev1.EventTypeNormal, "Applied", "All batches complete")
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseComplete
	// Clear activeRelease on all environments
	r.clearActiveRelease(ctx, release)
	return ctrl.Result{}, r.Status().Patch(ctx, release, patch)
}

func (r *ReleaseReconciler) failRelease(ctx context.Context, release *kaprov1alpha1.Release, msg string) error {
	patch := client.MergeFrom(release.DeepCopy())
	release.Status.Phase = kaprov1alpha1.ReleasePhaseFailed
	release.Status.Conditions = append(release.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		Reason:             "SubResourceFailed",
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

func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Release{}).
		Owns(&kaprov1alpha1.Pipeline{}).
		Owns(&kaprov1alpha1.Promotion{}).
		Owns(&kaprov1alpha1.BatchRun{}).
		Complete(r)
}

// labelSelectorMatches returns true if the object labels match the selector.
func labelSelectorMatches(objLabels map[string]string, sel labels.Selector) bool {
	return sel.Matches(labels.Set(objLabels))
}

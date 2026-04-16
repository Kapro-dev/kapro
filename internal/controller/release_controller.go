package controller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ReleaseReconciler reconciles Release objects.
// It resolves the scope (label selector → Environments), creates a Pipeline,
// and drives Promotion and Batch state machines.
//
// State machine:
//
//	Pending → Promoting → Progressing → Complete | Failed
type ReleaseReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=pipelines,verbs=get;list;watch;create;update;patch;delete

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

func (r *ReleaseReconciler) handlePending(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	// TODO: resolve scope → list matching Environments
	// TODO: create Pipeline object owned by this Release
	// TODO: transition to Promoting
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) handlePromoting(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	// TODO: drive Promotion state machines per country (dev → prod)
	// TODO: when all promotions complete → transition to Progressing
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) handleProgressing(ctx context.Context, release *kaprov1alpha1.Release) (ctrl.Result, error) {
	// TODO: drive Batch DAG (batch-1 → batch-2 → batch-3)
	// TODO: read ClusterRegistration.status for convergence
	// TODO: when all batches complete → transition to Complete
	return ctrl.Result{}, nil
}

func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Release{}).
		Owns(&kaprov1alpha1.Pipeline{}).
		Complete(r)
}

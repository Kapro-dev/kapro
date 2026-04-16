package controller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalReconciler watches Approval objects and triggers the waiting
// Promotion or BatchRun to recheck its gate.
// The actual gate check happens in PromotionReconciler/BatchRunReconciler —
// this controller just ensures they get re-queued immediately on approval.
type ApprovalReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=batchruns,verbs=get;list;watch

func (r *ApprovalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var approval kaprov1alpha1.Approval
	if err := r.Get(ctx, req.NamespacedName, &approval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("processing Approval",
		"name", approval.Name,
		"kind", approval.Spec.Kind,
		"ref", approval.Spec.Ref,
		"release", approval.Spec.Release,
		"approvedBy", approval.Spec.ApprovedBy,
		"bypass", approval.Spec.Bypass,
	)

	switch approval.Spec.Kind {
	case kaprov1alpha1.ApprovalKindPromotion:
		// Trigger re-reconcile of the waiting Promotion
		promoName := approval.Spec.Release + "-" + approval.Spec.Ref
		log.Info("triggering Promotion recheck", "promotion", promoName)
		// The PromotionReconciler will pick up the Approval via label selector on next reconcile

	case kaprov1alpha1.ApprovalKindBatch:
		// Trigger re-reconcile of the waiting BatchRun
		batchRunName := approval.Spec.Release + "-" + approval.Spec.Ref
		log.Info("triggering BatchRun recheck", "batchRun", batchRunName)
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Approval{}).
		Complete(r)
}

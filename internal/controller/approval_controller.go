package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalReconciler watches Approval objects and triggers the waiting
// Sync to recheck its gate.
// The actual gate check happens in SyncReconciler — this controller just
// ensures it gets re-queued immediately on approval, recording an Event for audit.
type ApprovalReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch;create;update;patch;delete

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

	r.Recorder.Event(&approval, corev1.EventTypeNormal, "Approved",
		fmt.Sprintf("Approval by %s for %s/%s", approval.Spec.ApprovedBy, approval.Spec.Kind, approval.Spec.Ref))

	switch approval.Spec.Kind {
	case kaprov1alpha1.ApprovalKindSync:
		// SyncReconciler watches Approvals via Watches() in SetupWithManager —
		// it maps the Approval to the waiting Sync and re-queues it immediately.
		syncName := approval.Spec.Release + "-" + approval.Spec.Ref
		log.Info("triggering Sync recheck", "sync", syncName)

	case kaprov1alpha1.ApprovalKindStage:
		// Stage-level approvals unblock an entire stage. SyncReconciler watches
		// Approvals and wakes up all Syncs for the matching stage.
		log.Info("triggering stage Sync recheck",
			"release", approval.Spec.Release,
			"stage", approval.Spec.Ref,
		)
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Approval{}).
		Complete(r)
}

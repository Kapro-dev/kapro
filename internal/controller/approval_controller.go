package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApprovalReconciler records an audit Event when an Approval is created or
// updated. The actual gate-unblock happens in ReleaseReconciler, which watches
// Approval objects via Watches(Approval, approvalForRelease) in SetupWithManager.
//
// This controller exists solely for audit: the Kubernetes Event stream gives
// operators an immutable, time-ordered record of every human approval without
// having to parse Release.status.targets.
type ApprovalReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=approvals/status,verbs=get;update;patch

func (r *ApprovalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var approval kaprov1alpha1.Approval
	if err := r.Get(ctx, req.NamespacedName, &approval); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("approval recorded",
		"name", approval.Name,
		"release", approval.Spec.Release,
		"target", approval.Spec.Target,
		"approvedBy", approval.Spec.ApprovedBy,
		"bypass", approval.Spec.Bypass,
	)

	// Fire an immutable audit Event on the Approval object. ReleaseReconciler
	// will wake up independently via its Approval watch and advance the gate.
	r.Recorder.Event(&approval, corev1.EventTypeNormal, "ApprovalRecorded",
		fmt.Sprintf("approved by %s for release=%s target=%s",
			approval.Spec.ApprovedBy, approval.Spec.Release, approval.Spec.Target))

	if approval.Status.Phase != kaprov1alpha1.ApprovalPhaseRecorded || approval.Status.ObservedGeneration != approval.Generation {
		patch := client.MergeFrom(approval.DeepCopy())
		approval.Status.Phase = kaprov1alpha1.ApprovalPhaseRecorded
		approval.Status.ProcessedAt = metav1.Now().UTC().Format(time.RFC3339)
		approval.Status.ObservedGeneration = approval.Generation
		apimeta.SetStatusCondition(&approval.Status.Conditions, metav1.Condition{
			Type:               "Recorded",
			Status:             metav1.ConditionTrue,
			Reason:             "ObservedByController",
			Message:            "approval has been recorded and is available to release reconciliation",
			ObservedGeneration: approval.Generation,
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Patch(ctx, &approval, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch approval status: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Approval{}).
		Complete(r)
}

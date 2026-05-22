package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const defaultAdapterPolicySyncInterval = 5 * time.Minute
const adapterPolicyBackendRefIndex = ".spec.backendRef"

// AdapterPolicyReconciler records continuous adapter discovery intent. The
// actual object materialization path is deliberately dry-run first while the
// adapter API graduates.
type AdapterPolicyReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=backends,verbs=get;list;watch

func (r *AdapterPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy kaprov1alpha2.AdapterPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var backend kaprov1alpha2.Backend
	ready := true
	reason := "DiscoveryScheduled"
	message := fmt.Sprintf("adapter %s discovery scheduled for backend %s", policy.Spec.Adapter, policy.Spec.BackendRef)
	if err := r.Get(ctx, client.ObjectKey{Name: policy.Spec.BackendRef}, &backend); err != nil {
		ready = false
		reason = "BackendNotFound"
		message = fmt.Sprintf("backend %s was not found: %v", policy.Spec.BackendRef, err)
	}

	interval := adapterPolicySyncInterval(policy.Spec.SyncInterval)
	now := metav1.Now()

	// Decide whether to patch. We must NOT patch on every reconcile —
	// the Status().Patch generates an update event the manager observes,
	// which schedules another reconcile, which patches again. With only
	// the rate limiter to bound it the controller can spin. Two distinct
	// signals warrant a patch:
	//   1) the computed material state (ready/reason/message/observedGen)
	//      differs from what's stored, or
	//   2) the recorded LastSyncTime is stale relative to syncInterval.
	materialUnchanged := policy.Status.Ready == ready &&
		policy.Status.Reason == reason &&
		policy.Status.Message == message &&
		policy.Status.ObservedGeneration == policy.Generation
	syncFresh := policy.Status.LastSyncTime != nil &&
		now.Time.Sub(policy.Status.LastSyncTime.Time) < interval/2
	if materialUnchanged && syncFresh {
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	patch := client.MergeFrom(policy.DeepCopy())
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.LastSyncTime = &now
	policy.Status.Ready = ready
	policy.Status.Reason = reason
	policy.Status.Message = message
	status := metav1.ConditionTrue
	if !ready {
		status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})
	if err := r.Status().Patch(ctx, &policy, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch AdapterPolicy status: %w", err)
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

func adapterPolicySyncInterval(raw string) time.Duration {
	if raw == "" {
		return defaultAdapterPolicySyncInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultAdapterPolicySyncInterval
	}
	return d
}

func (r *AdapterPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kaprov1alpha2.AdapterPolicy{},
		adapterPolicyBackendRefIndex,
		func(obj client.Object) []string {
			policy, ok := obj.(*kaprov1alpha2.AdapterPolicy)
			if !ok || policy.Spec.BackendRef == "" {
				return nil
			}
			return []string{policy.Spec.BackendRef}
		},
	); err != nil {
		return fmt.Errorf("index AdapterPolicy backend refs: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.AdapterPolicy{}).
		Watches(
			&kaprov1alpha2.Backend{},
			handler.EnqueueRequestsFromMapFunc(r.policiesForBackend),
		).
		Complete(r)
}

func (r *AdapterPolicyReconciler) policiesForBackend(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj == nil || obj.GetName() == "" {
		return nil
	}
	var policies kaprov1alpha2.AdapterPolicyList
	if err := r.List(ctx, &policies, client.MatchingFields{adapterPolicyBackendRefIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
	}
	return reqs
}

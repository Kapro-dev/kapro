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
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/argocd"
	"kapro.io/kapro/pkg/kapro/adapter/flux"
	"kapro.io/kapro/pkg/kapro/adapter/oci"
)

const defaultAdapterPolicySyncInterval = 5 * time.Minute
const adapterPolicyBackendRefIndex = ".spec.backendRef"

var defaultAdapterPolicyRegistry = newDefaultAdapterPolicyRegistry()

// AdapterPolicyReconciler validates that the policy can talk to its
// referenced Backend through the registered adapter and records the
// outcome on AdapterPolicy.status.
//
// The reconciler deliberately does NOT write Backend.status discovery
// fields. BackendReconciler is the single writer for Backend.status —
// having both controllers patch the same fields was producing flip-flop
// updates and merge conflicts. Discovery result details (selected /
// skipped objects, counts) surface via Kubernetes Events for now;
// adding them to AdapterPolicyStatus as first-class fields is tracked
// as a v0.2.x follow-up.
type AdapterPolicyReconciler struct {
	client.Client
	Recorder        record.EventRecorder
	AdapterRegistry *kaproadapter.Registry
}

// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=backends,verbs=get;list;watch

func (r *AdapterPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy kaprov1alpha2.AdapterPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	interval := adapterPolicySyncInterval(policy.Spec.SyncInterval)
	now := metav1.Now()
	ready, reason, message, err := r.discover(ctx, &policy)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Decide whether to patch. We must NOT patch on every reconcile —
	// the Status().Patch generates an update event the manager observes,
	// which schedules another reconcile, which patches again. With only
	// the rate limiter to bound it the controller can spin. Two distinct
	// signals warrant a patch:
	//   1) the computed material state (ready/reason/message/observedGen)
	//      differs from what's stored, or
	//   2) the recorded LastSyncTime is stale relative to syncInterval.
	materialUnchanged := adapterPolicyStatusCurrent(&policy, ready, reason, message) &&
		policy.Status.Reason == reason &&
		policy.Status.Message == message &&
		policy.Status.ObservedGeneration == policy.Generation
	syncFresh := policy.Status.LastSyncTime != nil &&
		now.Sub(policy.Status.LastSyncTime.Time) < interval/2
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

func (r *AdapterPolicyReconciler) discover(ctx context.Context, policy *kaprov1alpha2.AdapterPolicy) (bool, string, string, error) {
	var backend kaprov1alpha2.Backend
	if err := r.Get(ctx, client.ObjectKey{Name: policy.Spec.BackendRef}, &backend); err != nil {
		return false, "BackendNotFound", fmt.Sprintf("backend %s was not found: %v", policy.Spec.BackendRef, err), nil
	}
	if expected := adapterPolicyBackendAdapterName(&backend); policy.Spec.Adapter != expected {
		return false, "AdapterMismatch", fmt.Sprintf("policy adapter %q does not match backend %q adapter %q", policy.Spec.Adapter, backend.Name, expected), nil
	}
	if backend.Spec.Discovery == nil || !backend.Spec.Discovery.Enabled {
		// Backend opted out of discovery. BackendReconciler is the
		// single writer for Backend.status discovery fields; it
		// already clears them when discovery is disabled. Do not
		// mirror anything from here.
		return false, "DiscoveryDisabled", fmt.Sprintf("backend %s does not have spec.discovery.enabled=true", backend.Name), nil
	}
	a, err := r.adapterRegistry().Resolve(backend.Spec.Driver)
	if err != nil {
		return false, "AdapterResolveFailed", err.Error(), nil
	}
	result, err := a.Discover(ctx, adapterPolicyDiscoveryRequest(&backend))
	if err != nil {
		return false, "DiscoveryFailed", err.Error(), nil
	}
	reason := result.Reason
	if reason == "" {
		reason = "DiscoveryCompleted"
	}
	message := result.Message
	if message == "" {
		message = fmt.Sprintf("adapter %s discovery completed for backend %s (clusters=%d, applications=%d, applicationSets=%d)",
			policy.Spec.Adapter, backend.Name, result.DiscoveredClusters, result.DiscoveredApplications, result.DiscoveredApplicationSets)
	}
	if r.Recorder != nil {
		eventType := "Normal"
		if !result.Ready {
			eventType = "Warning"
		}
		r.Recorder.Eventf(policy, eventType, reason,
			"clusters=%d applications=%d applicationSets=%d selected=%d skipped=%d unsupported=%d errors=%d",
			result.DiscoveredClusters, result.DiscoveredApplications, result.DiscoveredApplicationSets,
			len(result.SelectedObjects), len(result.SkippedObjects), len(result.UnsupportedPatterns), len(result.DiscoveryErrors))
	}
	return result.Ready, reason, message, nil
}

func (r *AdapterPolicyReconciler) adapterRegistry() *kaproadapter.Registry {
	if r.AdapterRegistry != nil {
		return r.AdapterRegistry
	}
	return defaultAdapterPolicyRegistry
}

func newDefaultAdapterPolicyRegistry() *kaproadapter.Registry {
	reg := kaproadapter.NewRegistry()
	for _, a := range []kaproadapter.Adapter{argocd.New(), flux.New(), oci.New()} {
		if err := reg.Register(a); err != nil {
			panic(fmt.Sprintf("register built-in adapter: %v", err))
		}
	}
	return reg
}

func adapterPolicyBackendAdapterName(backend *kaprov1alpha2.Backend) string {
	if backend.Spec.Adapter != "" {
		return backend.Spec.Adapter
	}
	switch backend.Spec.Driver {
	case kaprov1alpha2.BackendDriverArgo:
		return "argo-cd"
	default:
		return string(backend.Spec.Driver)
	}
}

func adapterPolicyDiscoveryRequest(backend *kaprov1alpha2.Backend) kaproadapter.DiscoveryRequest {
	runtime := backend.Spec.Runtime
	if runtime == "" {
		runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	req := kaproadapter.DiscoveryRequest{
		Backend:    backend,
		Driver:     backend.Spec.Driver,
		Runtime:    runtime,
		Namespace:  backend.Spec.Parameters["namespace"],
		Parameters: backend.Spec.Parameters,
	}
	if backend.Spec.Discovery != nil {
		req.Selector = backend.Spec.Discovery.Selector
		req.MaxObjects = backend.Spec.Discovery.MaxObjects
	}
	if req.MaxObjects <= 0 {
		req.MaxObjects = int32(defaultBackendDiscoveryMaxObjects)
	}
	return req
}

func adapterPolicyStatusCurrent(policy *kaprov1alpha2.AdapterPolicy, ready bool, reason, message string) bool {
	if policy.Status.Ready != ready {
		return false
	}
	cond := apimeta.FindStatusCondition(policy.Status.Conditions, kaprov1alpha2.ConditionTypeReady)
	if cond == nil || cond.Reason != reason || cond.Message != message || cond.ObservedGeneration != policy.Generation {
		return false
	}
	wantStatus := metav1.ConditionTrue
	if !ready {
		wantStatus = metav1.ConditionFalse
	}
	return cond.Status == wantStatus
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

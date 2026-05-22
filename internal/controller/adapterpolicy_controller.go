package controller

import (
	"context"
	"fmt"
	"reflect"
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

// AdapterPolicyReconciler runs continuous adapter discovery and records the
// latest discovery outcome on AdapterPolicy and Backend status.
type AdapterPolicyReconciler struct {
	client.Client
	Recorder        record.EventRecorder
	AdapterRegistry *kaproadapter.Registry
}

// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=adapterpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=backends,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=backends/status,verbs=get;update;patch

func (r *AdapterPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy kaprov1alpha2.AdapterPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	interval := adapterPolicySyncInterval(policy.Spec.SyncInterval)
	now := metav1.Now()
	ready, reason, message, err := r.discover(ctx, &policy, now, interval)
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

func (r *AdapterPolicyReconciler) discover(ctx context.Context, policy *kaprov1alpha2.AdapterPolicy, now metav1.Time, interval time.Duration) (bool, string, string, error) {
	var backend kaprov1alpha2.Backend
	if err := r.Get(ctx, client.ObjectKey{Name: policy.Spec.BackendRef}, &backend); err != nil {
		return false, "BackendNotFound", fmt.Sprintf("backend %s was not found: %v", policy.Spec.BackendRef, err), nil
	}
	if expected := adapterPolicyBackendAdapterName(&backend); policy.Spec.Adapter != expected {
		ready, reason, message := false, "AdapterMismatch", fmt.Sprintf("policy adapter %q does not match backend %q adapter %q", policy.Spec.Adapter, backend.Name, expected)
		return ready, reason, message, r.patchBackendDiscoveryStatus(ctx, &backend, now, interval, kaproadapter.DiscoveryResult{Ready: ready, Reason: reason, Message: message})
	}
	if backend.Spec.Discovery == nil || !backend.Spec.Discovery.Enabled {
		ready, reason, message := false, "DiscoveryDisabled", fmt.Sprintf("backend %s does not have spec.discovery.enabled=true", backend.Name)
		return ready, reason, message, r.patchBackendDiscoveryStatus(ctx, &backend, now, interval, kaproadapter.DiscoveryResult{Ready: ready, Reason: reason, Message: message})
	}
	a, err := r.adapterRegistry().Resolve(backend.Spec.Driver)
	if err != nil {
		ready, reason, message := false, "AdapterResolveFailed", err.Error()
		return ready, reason, message, r.patchBackendDiscoveryStatus(ctx, &backend, now, interval, kaproadapter.DiscoveryResult{Ready: ready, Reason: reason, Message: message})
	}
	result, err := a.Discover(ctx, adapterPolicyDiscoveryRequest(&backend))
	if err != nil {
		ready, reason, message := false, "DiscoveryFailed", err.Error()
		return ready, reason, message, r.patchBackendDiscoveryStatus(ctx, &backend, now, interval, kaproadapter.DiscoveryResult{Ready: ready, Reason: reason, Message: message})
	}
	reason := result.Reason
	if reason == "" {
		reason = "DiscoveryCompleted"
	}
	message := result.Message
	if message == "" {
		message = fmt.Sprintf("adapter %s discovery completed for backend %s", policy.Spec.Adapter, backend.Name)
	}
	result.Reason = reason
	result.Message = message
	if err := r.patchBackendDiscoveryStatus(ctx, &backend, now, interval, result); err != nil {
		return false, "", "", err
	}
	return result.Ready, reason, message, nil
}

func (r *AdapterPolicyReconciler) patchBackendDiscoveryStatus(ctx context.Context, backend *kaprov1alpha2.Backend, now metav1.Time, interval time.Duration, result kaproadapter.DiscoveryResult) error {
	if backendDiscoveryStatusCurrent(backend, now, interval, result) {
		return nil
	}
	patch := client.MergeFrom(backend.DeepCopy())
	backend.Status.ObservedGeneration = backend.Generation
	backend.Status.Driver = backend.Spec.Driver
	backend.Status.Runtime = backend.Spec.Runtime
	if backend.Status.Runtime == "" {
		backend.Status.Runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	backend.Status.LastDiscoveryTime = &now
	backend.Status.DiscoveredClusters = result.DiscoveredClusters
	backend.Status.DiscoveredApplications = result.DiscoveredApplications
	backend.Status.DiscoveredApplicationSets = result.DiscoveredApplicationSets
	backend.Status.SelectedObjects = boundedDiscoveredObjects(result.SelectedObjects)
	backend.Status.SkippedObjects = boundedDiscoveredObjects(result.SkippedObjects)
	backend.Status.UnsupportedPatterns = boundedDiscoveredObjects(result.UnsupportedPatterns)
	backend.Status.DiscoveryErrors = boundedDiscoveryErrors(result.DiscoveryErrors)
	status := metav1.ConditionFalse
	if result.Ready {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&backend.Status.Conditions, metav1.Condition{
		Type:               "DiscoveryReady",
		Status:             status,
		Reason:             result.Reason,
		Message:            result.Message,
		ObservedGeneration: backend.Generation,
		LastTransitionTime: now,
	})
	if err := r.Status().Patch(ctx, backend, patch); err != nil {
		return fmt.Errorf("patch Backend discovery status: %w", err)
	}
	return nil
}

func backendDiscoveryStatusCurrent(backend *kaprov1alpha2.Backend, now metav1.Time, interval time.Duration, result kaproadapter.DiscoveryResult) bool {
	if backend.Status.LastDiscoveryTime == nil || now.Sub(backend.Status.LastDiscoveryTime.Time) >= interval/2 {
		return false
	}
	if backend.Status.ObservedGeneration != backend.Generation ||
		backend.Status.Driver != backend.Spec.Driver ||
		backend.Status.DiscoveredClusters != result.DiscoveredClusters ||
		backend.Status.DiscoveredApplications != result.DiscoveredApplications ||
		backend.Status.DiscoveredApplicationSets != result.DiscoveredApplicationSets ||
		!reflect.DeepEqual(backend.Status.SelectedObjects, boundedDiscoveredObjects(result.SelectedObjects)) ||
		!reflect.DeepEqual(backend.Status.SkippedObjects, boundedDiscoveredObjects(result.SkippedObjects)) ||
		!reflect.DeepEqual(backend.Status.UnsupportedPatterns, boundedDiscoveredObjects(result.UnsupportedPatterns)) ||
		!reflect.DeepEqual(backend.Status.DiscoveryErrors, boundedDiscoveryErrors(result.DiscoveryErrors)) {
		return false
	}
	wantStatus := metav1.ConditionFalse
	if result.Ready {
		wantStatus = metav1.ConditionTrue
	}
	cond := apimeta.FindStatusCondition(backend.Status.Conditions, "DiscoveryReady")
	if cond == nil {
		return false
	}
	return cond.Status == wantStatus &&
		cond.Reason == result.Reason &&
		cond.Message == result.Message &&
		cond.ObservedGeneration == backend.Generation
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

func boundedDiscoveredObjects(in []kaprov1alpha2.DiscoveredBackendObject) []kaprov1alpha2.DiscoveredBackendObject {
	if len(in) == 0 {
		return nil
	}
	limit := len(in)
	if limit > maxBackendDiscoveryStatusObjects {
		limit = maxBackendDiscoveryStatusObjects
	}
	out := make([]kaprov1alpha2.DiscoveredBackendObject, limit)
	copy(out, in[:limit])
	return out
}

func boundedDiscoveryErrors(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	limit := len(in)
	if limit > maxBackendDiscoveryStatusObjects {
		limit = maxBackendDiscoveryStatusObjects
	}
	out := make([]string, limit)
	copy(out, in[:limit])
	return out
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

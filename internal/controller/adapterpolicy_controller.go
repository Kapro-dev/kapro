package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/argocd"
	"kapro.io/kapro/pkg/kapro/adapter/flux"
	"kapro.io/kapro/pkg/kapro/adapter/oci"
)

const defaultAdapterPolicySyncInterval = 5 * time.Minute
const adapterPolicySubstrateRefIndex = ".spec.substrateRef"

var defaultAdapterPolicyRegistry = newDefaultAdapterPolicyRegistry()

// AdapterPolicyReconciler validates that the policy can talk to its
// referenced Substrate through the registered adapter and records the
// outcome on AdapterPolicy.status.
//
// The reconciler deliberately does NOT write Substrate.status discovery
// fields. SubstrateReconciler is the single writer for Substrate.status —
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
// +kubebuilder:rbac:groups=kapro.io,resources=substrates,verbs=get;list;watch

func (r *AdapterPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy kaprov1alpha1.AdapterPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	interval := adapterPolicySyncInterval(policy.Spec.SyncInterval)
	now := metav1.Now()
	outcome, err := r.discover(ctx, &policy)
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
	materialUnchanged := adapterPolicyStatusCurrent(&policy, outcome) &&
		policy.Status.Reason == outcome.reason &&
		policy.Status.Message == outcome.message &&
		policy.Status.DiscoveredObjects == outcome.discoveredObjects &&
		policy.Status.ObservedGeneration == policy.Generation
	syncFresh := policy.Status.LastSyncTime != nil &&
		now.Sub(policy.Status.LastSyncTime.Time) < interval/2
	if materialUnchanged && syncFresh {
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	patch := client.MergeFrom(policy.DeepCopy())
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.LastSyncTime = &now
	policy.Status.Ready = outcome.ready
	policy.Status.Reason = outcome.reason
	policy.Status.Message = outcome.message
	policy.Status.DiscoveredObjects = outcome.discoveredObjects
	status := metav1.ConditionTrue
	if !outcome.ready {
		status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeReady,
		Status:             status,
		Reason:             outcome.reason,
		Message:            outcome.message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})
	if err := r.Status().Patch(ctx, &policy, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch AdapterPolicy status: %w", err)
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

type adapterPolicyDiscoveryOutcome struct {
	ready             bool
	reason            string
	message           string
	discoveredObjects int32
}

func (r *AdapterPolicyReconciler) discover(ctx context.Context, policy *kaprov1alpha1.AdapterPolicy) (adapterPolicyDiscoveryOutcome, error) {
	var substrate kaprov1alpha1.Substrate
	if err := r.Get(ctx, client.ObjectKey{Name: policy.Spec.SubstrateRef}, &substrate); err != nil {
		if !apierrors.IsNotFound(err) {
			return adapterPolicyDiscoveryOutcome{}, err
		}
		return adapterPolicyDiscoveryOutcome{reason: "SubstrateNotFound", message: fmt.Sprintf("substrate %s was not found: %v", policy.Spec.SubstrateRef, err)}, nil
	}
	if expected := adapterPolicySubstrateAdapterName(&substrate); policy.Spec.Adapter != expected {
		return adapterPolicyDiscoveryOutcome{reason: "AdapterMismatch", message: fmt.Sprintf("policy adapter %q does not match substrate %q adapter %q", policy.Spec.Adapter, substrate.Name, expected)}, nil
	}
	if substrate.Spec.Discovery == nil || !substrate.Spec.Discovery.Enabled {
		// Substrate opted out of discovery. SubstrateReconciler is the
		// single writer for Substrate.status discovery fields; it
		// already clears them when discovery is disabled. Do not
		// mirror anything from here.
		return adapterPolicyDiscoveryOutcome{reason: "DiscoveryDisabled", message: fmt.Sprintf("substrate %s does not have spec.discovery.enabled=true", substrate.Name)}, nil
	}
	a, err := r.adapterRegistry().ResolveKind(substrate.Spec.SubstrateKind())
	if err != nil {
		return adapterPolicyDiscoveryOutcome{reason: "AdapterResolveFailed", message: err.Error()}, nil
	}
	if !a.Capabilities().SupportsDiscover {
		return adapterPolicyDiscoveryOutcome{reason: "DiscoveryUnsupported", message: fmt.Sprintf("adapter %s does not support discovery", policy.Spec.Adapter)}, nil
	}
	req, err := adapterPolicyDiscoveryRequest(&substrate, policy)
	if err != nil {
		return adapterPolicyDiscoveryOutcome{reason: "InvalidSelector", message: err.Error()}, nil
	}
	if policy.Spec.DryRun {
		return adapterPolicyDiscoveryOutcome{
			ready:   true,
			reason:  "DiscoveryDryRun",
			message: fmt.Sprintf("adapter policy %s validated adapter %s for substrate %s without running discovery", policy.Name, policy.Spec.Adapter, substrate.Name),
		}, nil
	}
	if adapterPolicyMirrorsSubstrateStatus(&substrate, policy) {
		return adapterPolicyOutcomeFromSubstrateStatus(&substrate, kaproadapter.DiscoveryResult{}), nil
	}
	result, err := a.Discover(ctx, req)
	if err != nil {
		return adapterPolicyDiscoveryOutcome{reason: "DiscoveryFailed", message: err.Error()}, nil
	}
	reason := result.Reason
	if reason == "" {
		reason = "DiscoveryCompleted"
	}
	message := result.Message
	if message == "" {
		message = fmt.Sprintf("adapter %s discovery completed for substrate %s (clusters=%d, applications=%d, applicationSets=%d)",
			policy.Spec.Adapter, substrate.Name, result.DiscoveredClusters, result.DiscoveredApplications, result.DiscoveredApplicationSets)
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
	return adapterPolicyDiscoveryOutcome{
		ready:             result.Ready,
		reason:            reason,
		message:           message,
		discoveredObjects: adapterPolicyDiscoveredObjects(result),
	}, nil
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

func adapterPolicySubstrateAdapterName(substrate *kaprov1alpha1.Substrate) string {
	return substrate.Spec.ActuatorName()
}

func adapterPolicyMirrorsSubstrateStatus(substrate *kaprov1alpha1.Substrate, policy *kaprov1alpha1.AdapterPolicy) bool {
	if policy.Spec.Selector != nil {
		return false
	}
	switch substrate.Spec.SubstrateKind() {
	case string(kaprov1alpha1.SubstrateDriverArgo), string(kaprov1alpha1.SubstrateDriverFlux):
		return true
	default:
		return false
	}
}

func adapterPolicyOutcomeFromSubstrateStatus(substrate *kaprov1alpha1.Substrate, fallback kaproadapter.DiscoveryResult) adapterPolicyDiscoveryOutcome {
	cond := apimeta.FindStatusCondition(substrate.Status.Conditions, "DiscoveryReady")
	if cond == nil {
		return adapterPolicyDiscoveryOutcome{
			ready:             false,
			reason:            "DiscoveryPending",
			message:           fmt.Sprintf("waiting for Substrate %s discovery status from SubstrateReconciler", substrate.Name),
			discoveredObjects: adapterPolicySubstrateStatusObjects(substrate),
		}
	}
	ready := cond.Status == metav1.ConditionTrue
	reason := cond.Reason
	if reason == "" {
		reason = fallback.Reason
	}
	if reason == "" {
		reason = "DiscoveryCompleted"
	}
	message := cond.Message
	if message == "" {
		message = fallback.Message
	}
	if message == "" {
		message = fmt.Sprintf("Substrate %s discovery status observed", substrate.Name)
	}
	return adapterPolicyDiscoveryOutcome{
		ready:             ready,
		reason:            reason,
		message:           message,
		discoveredObjects: adapterPolicySubstrateStatusObjects(substrate),
	}
}

func adapterPolicySubstrateStatusObjects(substrate *kaprov1alpha1.Substrate) int32 {
	return substrate.Status.DiscoveredClusters + substrate.Status.DiscoveredApplications + substrate.Status.DiscoveredApplicationSets
}

func adapterPolicyDiscoveryRequest(substrate *kaprov1alpha1.Substrate, policy *kaprov1alpha1.AdapterPolicy) (kaproadapter.DiscoveryRequest, error) {
	req := kaproadapter.DiscoveryRequest{
		Substrate:  substrate,
		Driver:     kaprov1alpha1.SubstrateDriver(substrate.Spec.SubstrateKind()),
		Runtime:    substrateRuntimeForDiscovery(substrate.Spec.ExecutionMode()),
		Namespace:  substrate.Spec.Parameters["namespace"],
		Parameters: substrate.Spec.Parameters,
	}
	if substrate.Spec.Discovery != nil {
		selector, err := mergeAdapterPolicySelectors(substrate.Spec.Discovery.Selector, policy.Spec.Selector)
		if err != nil {
			return req, err
		}
		req.Selector = selector
		req.MaxObjects = substrate.Spec.Discovery.MaxObjects
	} else if policy.Spec.Selector != nil {
		req.Selector = policy.Spec.Selector.DeepCopy()
	}
	if req.MaxObjects <= 0 {
		req.MaxObjects = int32(defaultSubstrateDiscoveryMaxObjects)
	}
	return req, nil
}

func substrateRuntimeForDiscovery(mode kaprov1alpha1.ExecutionMode) kaprov1alpha1.SubstrateRuntime {
	switch mode {
	case kaprov1alpha1.ExecutionModeHubPush:
		return kaprov1alpha1.SubstrateRuntimeHub
	case kaprov1alpha1.ExecutionModeSpokePull:
		return kaprov1alpha1.SubstrateRuntimeSpoke
	default:
		return kaprov1alpha1.SubstrateRuntimeBoth
	}
}

func mergeAdapterPolicySelectors(substrate, policy *metav1.LabelSelector) (*metav1.LabelSelector, error) {
	if substrate == nil && policy == nil {
		return nil, nil
	}
	if substrate == nil {
		return validatedAdapterPolicySelector(policy.DeepCopy())
	}
	merged := substrate.DeepCopy()
	if policy == nil {
		return validatedAdapterPolicySelector(merged)
	}
	if merged.MatchLabels == nil && len(policy.MatchLabels) > 0 {
		merged.MatchLabels = map[string]string{}
	}
	for key, value := range policy.MatchLabels {
		if existing, ok := merged.MatchLabels[key]; ok && existing != value {
			merged.MatchExpressions = append(merged.MatchExpressions, metav1.LabelSelectorRequirement{
				Key:      key,
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{value},
			})
			continue
		}
		merged.MatchLabels[key] = value
	}
	if len(policy.MatchExpressions) > 0 {
		merged.MatchExpressions = append(merged.MatchExpressions, policy.MatchExpressions...)
	}
	return validatedAdapterPolicySelector(merged)
}

func validatedAdapterPolicySelector(selector *metav1.LabelSelector) (*metav1.LabelSelector, error) {
	if selector == nil {
		return nil, nil
	}
	if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
		return nil, err
	}
	return selector, nil
}

func adapterPolicyDiscoveredObjects(result kaproadapter.DiscoveryResult) int32 {
	total := result.DiscoveredClusters + result.DiscoveredApplications + result.DiscoveredApplicationSets
	if total > 0 {
		return total
	}
	return int32(len(result.SelectedObjects) + len(result.SkippedObjects) + len(result.UnsupportedPatterns))
}

func adapterPolicyStatusCurrent(policy *kaprov1alpha1.AdapterPolicy, outcome adapterPolicyDiscoveryOutcome) bool {
	if policy.Status.Ready != outcome.ready || policy.Status.DiscoveredObjects != outcome.discoveredObjects {
		return false
	}
	cond := apimeta.FindStatusCondition(policy.Status.Conditions, kaprov1alpha1.ConditionTypeReady)
	if cond == nil || cond.Reason != outcome.reason || cond.Message != outcome.message || cond.ObservedGeneration != policy.Generation {
		return false
	}
	wantStatus := metav1.ConditionTrue
	if !outcome.ready {
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
		&kaprov1alpha1.AdapterPolicy{},
		adapterPolicySubstrateRefIndex,
		func(obj client.Object) []string {
			policy, ok := obj.(*kaprov1alpha1.AdapterPolicy)
			if !ok || policy.Spec.SubstrateRef == "" {
				return nil
			}
			return []string{policy.Spec.SubstrateRef}
		},
	); err != nil {
		return fmt.Errorf("index AdapterPolicy substrate refs: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.AdapterPolicy{}).
		Watches(
			&kaprov1alpha1.Substrate{},
			handler.EnqueueRequestsFromMapFunc(r.policiesForSubstrate),
		).
		Complete(r)
}

func (r *AdapterPolicyReconciler) policiesForSubstrate(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj == nil || obj.GetName() == "" {
		return nil
	}
	var policies kaprov1alpha1.AdapterPolicyList
	if err := r.List(ctx, &policies, client.MatchingFields{adapterPolicySubstrateRefIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
	}
	return reqs
}

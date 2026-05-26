package controller

import (
	"context"
	"errors"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
)

func TestSubstrateDiscoveryPolicyReconcilerRecordsDiscoveryResult(t *testing.T) {
	ctx := context.Background()
	discovered := false
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Status: kaprov1alpha1.SubstrateStatus{
				DiscoveredClusters:     1,
				DiscoveredApplications: 1,
				Conditions: []metav1.Condition{{
					Type:    "DiscoveryReady",
					Status:  metav1.ConditionTrue,
					Reason:  "DiscoverySucceeded",
					Message: "discovered 2 Argo objects",
				}},
			},
			Spec: adapterPolicySubstrateSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{
					Enabled:    true,
					MaxObjects: 50,
					Selector:   &metav1.LabelSelector{MatchLabels: map[string]string{"kapro.io/import": "true"}},
				}),
				withParameters(map[string]string{"namespace": "argocd"}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "argo",
				SubstrateRef: "argo",
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver:  kaprov1alpha1.SubstrateKindArgo,
		runtime: kaprov1alpha1.ExecutionScopeHub,
		discover: func(_ context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			discovered = true
			if req.Substrate == nil || req.Substrate.Name != "argo" {
				t.Fatalf("discovery substrate = %#v, want argo", req.Substrate)
			}
			if req.SubstrateKind != kaprov1alpha1.SubstrateKindArgo || req.ExecutionScope != kaprov1alpha1.ExecutionScopeHub {
				t.Fatalf("discovery driver/runtime = %s/%s", req.SubstrateKind, req.ExecutionScope)
			}
			if req.Namespace != "argocd" || req.MaxObjects != 50 {
				t.Fatalf("discovery namespace/max = %q/%d", req.Namespace, req.MaxObjects)
			}
			if req.Selector == nil || req.Selector.MatchLabels["kapro.io/import"] != "true" {
				t.Fatalf("discovery selector = %#v", req.Selector)
			}
			if req.Selector.MatchLabels["team"] != "payments" {
				t.Fatalf("discovery selector missing policy selector: %#v", req.Selector)
			}
			return kaproadapter.DiscoveryResult{
				Ready:                  true,
				Reason:                 "DiscoverySucceeded",
				Message:                "discovered 2 Argo objects",
				DiscoveredClusters:     1,
				DiscoveredApplications: 1,
				SelectedObjects: []kaprov1alpha1.DiscoveredSubstrateObject{
					{APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Namespace: "argocd", Name: "payments"},
				},
			}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !discovered {
		t.Fatalf("adapter discovery was not called")
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoverySucceeded" || got.Status.Message != "discovered 2 Argo objects" {
		t.Fatalf("status = %#v, want ready DiscoverySucceeded", got.Status)
	}
	if got.Status.DiscoveredObjects != 2 {
		t.Fatalf("discoveredObjects=%d, want 2", got.Status.DiscoveredObjects)
	}
	cond := adapterPolicyReadyCondition(t, got.Status.Conditions)
	if cond.Status != metav1.ConditionTrue || cond.Reason != "DiscoverySucceeded" {
		t.Fatalf("ready condition = %#v", cond)
	}
	if got.Status.LastSyncTime == nil {
		t.Fatalf("LastSyncTime was not set")
	}

	// SubstrateDiscoveryPolicyReconciler is no longer the writer for Substrate.status
	// discovery fields — SubstrateReconciler owns them. Confirm here that
	// running a single SubstrateDiscoveryPolicy reconcile does NOT touch Substrate
	// status (the assertion that previously expected LastDiscoveryTime
	// to be set is the inverse of this invariant).
	var substrate kaprov1alpha1.Substrate
	if err := c.Get(ctx, client.ObjectKey{Name: "argo"}, &substrate); err != nil {
		t.Fatalf("get substrate: %v", err)
	}
	if substrate.Status.LastDiscoveryTime != nil {
		t.Fatalf("SubstrateDiscoveryPolicyReconciler must not write Substrate.status.lastDiscoveryTime; got %v", substrate.Status.LastDiscoveryTime)
	}
	if substrate.Status.DiscoveredClusters != 1 || substrate.Status.DiscoveredApplications != 1 || len(substrate.Status.SelectedObjects) != 0 {
		t.Fatalf("SubstrateDiscoveryPolicyReconciler must not change Substrate.status discovery counts; got %#v", substrate.Status)
	}
	if cond := apimeta.FindStatusCondition(substrate.Status.Conditions, "DiscoveryReady"); cond == nil || cond.Reason != "DiscoverySucceeded" {
		t.Fatalf("SubstrateDiscoveryPolicyReconciler must not change Substrate.DiscoveryReady; got %#v", cond)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerUsesTypedConfigNamespace(t *testing.T) {
	ctx := context.Background()
	config := typedSubstrateConfigWithSpecNamespace("flux.substrate.kapro.io/v1alpha1", "FluxSubstrateConfig", "checkout", "flux-managed")
	c := adapterPolicyClient(t,
		config,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec: kaprov1alpha1.SubstrateSpec{
				ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
				ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
					APIVersion: "flux.substrate.kapro.io/v1alpha1",
					Kind:       "FluxSubstrateConfig",
					Name:       "checkout",
				},
				Execution:  &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
				Discovery:  &kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true},
				Parameters: map[string]string{"namespace": "wrong-namespace"},
			},
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-flux", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "flux",
				SubstrateRef: "flux",
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"team": "checkout"}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver:  kaprov1alpha1.SubstrateKindFlux,
		runtime: kaprov1alpha1.ExecutionScopeHub,
		discover: func(_ context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			if req.Namespace != "flux-managed" {
				t.Fatalf("discovery namespace=%q, want typed config namespace", req.Namespace)
			}
			return kaproadapter.DiscoveryResult{Ready: true, Reason: "DiscoverySucceeded", Message: "ok"}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-flux"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerMirrorsSubstrateDiscoveryStatusWithoutPolicySelector(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Status: kaprov1alpha1.SubstrateStatus{
				DiscoveredClusters:        2,
				DiscoveredApplications:    3,
				DiscoveredApplicationSets: 1,
				Conditions: []metav1.Condition{{
					Type:    "DiscoveryReady",
					Status:  metav1.ConditionTrue,
					Reason:  "DiscoverySucceeded",
					Message: "substrate discovery completed",
				}},
			},
			Spec: adapterPolicySubstrateSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "argo",
				SubstrateRef: "argo",
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha1.SubstrateKindArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("built-in SubstrateDiscoveryPolicy without policy selector must mirror Substrate.status without running reference discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoverySucceeded" || got.Status.Message != "substrate discovery completed" {
		t.Fatalf("status = %#v, want mirrored Substrate DiscoveryReady", got.Status)
	}
	if got.Status.DiscoveredObjects != 6 {
		t.Fatalf("discoveredObjects=%d, want 6", got.Status.DiscoveredObjects)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerDryRunSkipsAdapterDiscovery(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: adapterPolicySubstrateSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "argo",
				SubstrateRef: "argo",
				DryRun:       true,
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha1.SubstrateKindArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("dry-run policy must not call adapter discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoveryDryRun" {
		t.Fatalf("status = %#v, want ready DiscoveryDryRun", got.Status)
	}
	if got.Status.DiscoveredObjects != 0 {
		t.Fatalf("discoveredObjects=%d, want 0", got.Status.DiscoveredObjects)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerDryRunValidatesAdapterAndSelector(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: adapterPolicySubstrateSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "argo",
				SubstrateRef: "argo",
				DryRun:       true,
				Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "team",
					Operator: metav1.LabelSelectorOpIn,
				}}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha1.SubstrateKindArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("invalid dry-run policy must not call adapter discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "InvalidSelector" {
		t.Fatalf("status = %#v, want not ready InvalidSelector", got.Status)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerAndsSelectorConflicts(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "external"},
			Spec: adapterPolicySubstrateSpec("external", kaprov1alpha1.ExecutionModeExternalPull,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{
					Enabled:  true,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
				}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-external", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "external",
				SubstrateRef: "external",
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha1.SubstrateKindExternal,
		discover: func(_ context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			if req.Selector == nil || req.Selector.MatchLabels["team"] != "payments" {
				t.Fatalf("substrate selector not preserved: %#v", req.Selector)
			}
			if len(req.Selector.MatchExpressions) != 1 ||
				req.Selector.MatchExpressions[0].Key != "team" ||
				req.Selector.MatchExpressions[0].Operator != metav1.LabelSelectorOpIn ||
				len(req.Selector.MatchExpressions[0].Values) != 1 ||
				req.Selector.MatchExpressions[0].Values[0] != "platform" {
				t.Fatalf("policy selector was not ANDed as expression: %#v", req.Selector)
			}
			return kaproadapter.DiscoveryResult{Ready: true, Reason: "DiscoverySucceeded", DiscoveredApplications: 1}, nil
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-external"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-external"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoverySucceeded" || got.Status.DiscoveredObjects != 1 {
		t.Fatalf("status = %#v, want ready DiscoverySucceeded with one object", got.Status)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerReportsDiscoveryDisabled(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       adapterPolicySubstrateSpec("flux", kaprov1alpha1.ExecutionModeSpokePull),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-flux", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "flux",
				SubstrateRef: "flux",
			},
		},
	)
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: adapterPolicyTestRegistry(t)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-flux"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-flux"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "DiscoveryDisabled" {
		t.Fatalf("status = %#v, want not ready DiscoveryDisabled", got.Status)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerReportsSubstrateKindMismatch(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: adapterPolicySubstrateSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "flux",
				SubstrateRef: "argo",
			},
		},
	)
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: adapterPolicyTestRegistry(t)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "SubstrateKindMismatch" {
		t.Fatalf("status = %#v, want not ready SubstrateKindMismatch", got.Status)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerReportsDiscoveryError(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "external"},
			Spec: adapterPolicySubstrateSpec("external", kaprov1alpha1.ExecutionModeExternalPull,
				withDiscovery(&kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true}),
			),
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-external", Generation: 1},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				ExpectedKind: "external",
				SubstrateRef: "external",
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha1.SubstrateKindExternal,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			return kaproadapter.DiscoveryResult{}, errors.New("substrate API unavailable")
		},
	})
	r := &SubstrateDiscoveryPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-external"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha1.SubstrateDiscoveryPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-external"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "DiscoveryFailed" || got.Status.Message != "substrate API unavailable" {
		t.Fatalf("status = %#v, want not ready DiscoveryFailed", got.Status)
	}
}

func TestSubstrateDiscoveryPolicyReconcilerMapsSubstrateToPolicies(t *testing.T) {
	substrate := &kaprov1alpha1.Substrate{ObjectMeta: metav1.ObjectMeta{Name: "argo"}}
	policy := &kaprov1alpha1.SubstrateDiscoveryPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo"},
		Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
			ExpectedKind: "argo",
			SubstrateRef: "argo",
		},
	}
	c := adapterPolicyClient(t, substrate, policy)
	r := &SubstrateDiscoveryPolicyReconciler{Client: c}

	got := r.policiesForSubstrate(context.Background(), substrate)
	if len(got) != 1 || got[0].Name != "adopt-argo" {
		t.Fatalf("requests = %#v, want adopt-argo", got)
	}
}

func adapterPolicyClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha1.SubstrateDiscoveryPolicy{}, &kaprov1alpha1.Substrate{}).
		WithIndex(&kaprov1alpha1.SubstrateDiscoveryPolicy{}, adapterPolicySubstrateRefIndex, func(obj client.Object) []string {
			policy, ok := obj.(*kaprov1alpha1.SubstrateDiscoveryPolicy)
			if !ok || policy.Spec.SubstrateRef == "" {
				return nil
			}
			return []string{policy.Spec.SubstrateRef}
		}).
		Build()
}

func adapterPolicySubstrateSpec(kind string, mode kaprov1alpha1.ExecutionMode, opts ...func(*kaprov1alpha1.SubstrateSpec)) kaprov1alpha1.SubstrateSpec {
	spec := kaprov1alpha1.SubstrateSpec{
		Substrate: &kaprov1alpha1.SubstrateImplementationSpec{Kind: kind, Actuator: kind},
		Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: mode},
	}
	for _, opt := range opts {
		opt(&spec)
	}
	return spec
}

func withDiscovery(discovery *kaprov1alpha1.SubstrateDiscoverySpec) func(*kaprov1alpha1.SubstrateSpec) {
	return func(spec *kaprov1alpha1.SubstrateSpec) {
		spec.Discovery = discovery
	}
}

func withParameters(parameters map[string]string) func(*kaprov1alpha1.SubstrateSpec) {
	return func(spec *kaprov1alpha1.SubstrateSpec) {
		spec.Parameters = parameters
	}
}

type fakeDiscoveryAdapter struct {
	driver   kaprov1alpha1.SubstrateKind
	runtime  kaprov1alpha1.ExecutionScope
	discover func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error)
}

func (a *fakeDiscoveryAdapter) SubstrateKind() kaprov1alpha1.SubstrateKind { return a.driver }
func (a *fakeDiscoveryAdapter) ExecutionScope() kaprov1alpha1.ExecutionScope {
	return a.runtime
}
func (a *fakeDiscoveryAdapter) Capabilities() kaproadapter.Capabilities {
	return kaproadapter.Capabilities{
		SubstrateKind:    a.driver,
		ExecutionScope:   a.runtime,
		SupportsDiscover: true,
	}.Normalize()
}
func (a *fakeDiscoveryAdapter) Apply(context.Context, kaproadapter.Request) (kaproadapter.Result, error) {
	return kaproadapter.Result{}, nil
}
func (a *fakeDiscoveryAdapter) Observe(context.Context, kaproadapter.Request) (kaproadapter.Result, error) {
	return kaproadapter.Result{}, nil
}
func (a *fakeDiscoveryAdapter) Rollback(context.Context, kaproadapter.Request) (kaproadapter.Result, error) {
	return kaproadapter.Result{}, nil
}
func (a *fakeDiscoveryAdapter) Discover(ctx context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
	if a.discover != nil {
		return a.discover(ctx, req)
	}
	return kaproadapter.DiscoveryResult{Ready: true, Reason: "DiscoverySucceeded", Message: "discovery succeeded"}, nil
}

func adapterPolicyTestRegistry(t *testing.T, adapters ...kaproadapter.Adapter) *kaproadapter.Registry {
	t.Helper()
	reg := kaproadapter.NewRegistry()
	for _, a := range adapters {
		if err := reg.Register(a); err != nil {
			t.Fatalf("register adapter: %v", err)
		}
	}
	return reg
}

func adapterPolicyReadyCondition(t *testing.T, conditions []metav1.Condition) metav1.Condition {
	t.Helper()
	return adapterPolicyConditionByType(t, conditions, kaprov1alpha1.ConditionTypeReady)
}

func adapterPolicyConditionByType(t *testing.T, conditions []metav1.Condition, typ string) metav1.Condition {
	t.Helper()
	for _, cond := range conditions {
		if cond.Type == typ {
			return cond
		}
	}
	t.Fatalf("%s condition not found in %#v", typ, conditions)
	return metav1.Condition{}
}

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

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
)

func TestAdapterPolicyReconcilerRecordsDiscoveryResult(t *testing.T) {
	ctx := context.Background()
	discovered := false
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Status: kaprov1alpha2.BackendStatus{
				DiscoveredClusters:     1,
				DiscoveredApplications: 1,
				Conditions: []metav1.Condition{{
					Type:    "DiscoveryReady",
					Status:  metav1.ConditionTrue,
					Reason:  "DiscoverySucceeded",
					Message: "discovered 2 Argo objects",
				}},
			},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:  kaprov1alpha2.BackendDriverArgo,
				Adapter: "argo-cd",
				Runtime: kaprov1alpha2.BackendRuntimeHub,
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{
					Enabled:    true,
					MaxObjects: 50,
					Selector:   &metav1.LabelSelector{MatchLabels: map[string]string{"kapro.io/import": "true"}},
				},
				Parameters: map[string]string{"namespace": "argocd"},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "argo-cd",
				BackendRef: "argo",
				Selector:   &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver:  kaprov1alpha2.BackendDriverArgo,
		runtime: kaprov1alpha2.BackendRuntimeHub,
		discover: func(_ context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			discovered = true
			if req.Backend == nil || req.Backend.Name != "argo" {
				t.Fatalf("discovery backend = %#v, want argo", req.Backend)
			}
			if req.Driver != kaprov1alpha2.BackendDriverArgo || req.Runtime != kaprov1alpha2.BackendRuntimeHub {
				t.Fatalf("discovery driver/runtime = %s/%s", req.Driver, req.Runtime)
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
				SelectedObjects: []kaprov1alpha2.DiscoveredBackendObject{
					{APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Namespace: "argocd", Name: "payments"},
				},
			}, nil
		},
	})
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !discovered {
		t.Fatalf("adapter discovery was not called")
	}

	var got kaprov1alpha2.AdapterPolicy
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

	// AdapterPolicyReconciler is no longer the writer for Backend.status
	// discovery fields — BackendReconciler owns them. Confirm here that
	// running a single AdapterPolicy reconcile does NOT touch Backend
	// status (the assertion that previously expected LastDiscoveryTime
	// to be set is the inverse of this invariant).
	var backend kaprov1alpha2.Backend
	if err := c.Get(ctx, client.ObjectKey{Name: "argo"}, &backend); err != nil {
		t.Fatalf("get backend: %v", err)
	}
	if backend.Status.LastDiscoveryTime != nil {
		t.Fatalf("AdapterPolicyReconciler must not write Backend.status.lastDiscoveryTime; got %v", backend.Status.LastDiscoveryTime)
	}
	if backend.Status.DiscoveredClusters != 1 || backend.Status.DiscoveredApplications != 1 || len(backend.Status.SelectedObjects) != 0 {
		t.Fatalf("AdapterPolicyReconciler must not change Backend.status discovery counts; got %#v", backend.Status)
	}
	if cond := apimeta.FindStatusCondition(backend.Status.Conditions, "DiscoveryReady"); cond == nil || cond.Reason != "DiscoverySucceeded" {
		t.Fatalf("AdapterPolicyReconciler must not change Backend.DiscoveryReady; got %#v", cond)
	}
}

func TestAdapterPolicyReconcilerMirrorsBackendDiscoveryStatusWithoutPolicySelector(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Status: kaprov1alpha2.BackendStatus{
				DiscoveredClusters:        2,
				DiscoveredApplications:    3,
				DiscoveredApplicationSets: 1,
				Conditions: []metav1.Condition{{
					Type:    "DiscoveryReady",
					Status:  metav1.ConditionTrue,
					Reason:  "DiscoverySucceeded",
					Message: "backend discovery completed",
				}},
			},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:    kaprov1alpha2.BackendDriverArgo,
				Adapter:   "argo-cd",
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{Enabled: true},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "argo-cd",
				BackendRef: "argo",
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha2.BackendDriverArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("built-in AdapterPolicy without policy selector must mirror Backend.status without running reference discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoverySucceeded" || got.Status.Message != "backend discovery completed" {
		t.Fatalf("status = %#v, want mirrored Backend DiscoveryReady", got.Status)
	}
	if got.Status.DiscoveredObjects != 6 {
		t.Fatalf("discoveredObjects=%d, want 6", got.Status.DiscoveredObjects)
	}
}

func TestAdapterPolicyReconcilerDryRunSkipsAdapterDiscovery(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:    kaprov1alpha2.BackendDriverArgo,
				Adapter:   "argo-cd",
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{Enabled: true},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "argo-cd",
				BackendRef: "argo",
				DryRun:     true,
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha2.BackendDriverArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("dry-run policy must not call adapter discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
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

func TestAdapterPolicyReconcilerDryRunValidatesAdapterAndSelector(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:    kaprov1alpha2.BackendDriverArgo,
				Adapter:   "argo-cd",
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{Enabled: true},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "argo-cd",
				BackendRef: "argo",
				DryRun:     true,
				Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "team",
					Operator: metav1.LabelSelectorOpIn,
				}}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha2.BackendDriverArgo,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			t.Fatalf("invalid dry-run policy must not call adapter discovery")
			return kaproadapter.DiscoveryResult{}, nil
		},
	})
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "InvalidSelector" {
		t.Fatalf("status = %#v, want not ready InvalidSelector", got.Status)
	}
}

func TestAdapterPolicyReconcilerAndsSelectorConflicts(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "external"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:  kaprov1alpha2.BackendDriverExternal,
				Adapter: "external",
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{
					Enabled:  true,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
				},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-external", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "external",
				BackendRef: "external",
				Selector:   &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha2.BackendDriverExternal,
		discover: func(_ context.Context, req kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			if req.Selector == nil || req.Selector.MatchLabels["team"] != "payments" {
				t.Fatalf("backend selector not preserved: %#v", req.Selector)
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
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-external"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-external"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoverySucceeded" || got.Status.DiscoveredObjects != 1 {
		t.Fatalf("status = %#v, want ready DiscoverySucceeded with one object", got.Status)
	}
}

func TestAdapterPolicyReconcilerReportsDiscoveryDisabled(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver: kaprov1alpha2.BackendDriverFlux,
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-flux", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "flux",
				BackendRef: "flux",
			},
		},
	)
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: adapterPolicyTestRegistry(t)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-flux"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-flux"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "DiscoveryDisabled" {
		t.Fatalf("status = %#v, want not ready DiscoveryDisabled", got.Status)
	}
}

func TestAdapterPolicyReconcilerReportsAdapterMismatch(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:    kaprov1alpha2.BackendDriverArgo,
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{Enabled: true},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "flux",
				BackendRef: "argo",
			},
		},
	)
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: adapterPolicyTestRegistry(t)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "AdapterMismatch" {
		t.Fatalf("status = %#v, want not ready AdapterMismatch", got.Status)
	}
}

func TestAdapterPolicyReconcilerReportsDiscoveryError(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "external"},
			Spec: kaprov1alpha2.BackendSpec{
				Driver:    kaprov1alpha2.BackendDriverExternal,
				Adapter:   "external",
				Discovery: &kaprov1alpha2.BackendDiscoverySpec{Enabled: true},
			},
		},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-external", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "external",
				BackendRef: "external",
			},
		},
	)
	reg := adapterPolicyTestRegistry(t, &fakeDiscoveryAdapter{
		driver: kaprov1alpha2.BackendDriverExternal,
		discover: func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error) {
			return kaproadapter.DiscoveryResult{}, errors.New("backend API unavailable")
		},
	})
	r := &AdapterPolicyReconciler{Client: c, AdapterRegistry: reg}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-external"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-external"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.Status.Ready || got.Status.Reason != "DiscoveryFailed" || got.Status.Message != "backend API unavailable" {
		t.Fatalf("status = %#v, want not ready DiscoveryFailed", got.Status)
	}
}

func TestAdapterPolicyReconcilerMapsBackendToPolicies(t *testing.T) {
	backend := &kaprov1alpha2.Backend{ObjectMeta: metav1.ObjectMeta{Name: "argo"}}
	policy := &kaprov1alpha2.AdapterPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo"},
		Spec: kaprov1alpha2.AdapterPolicySpec{
			Adapter:    "argo-cd",
			BackendRef: "argo",
		},
	}
	c := adapterPolicyClient(t, backend, policy)
	r := &AdapterPolicyReconciler{Client: c}

	got := r.policiesForBackend(context.Background(), backend)
	if len(got) != 1 || got[0].Name != "adopt-argo" {
		t.Fatalf("requests = %#v, want adopt-argo", got)
	}
}

func adapterPolicyClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha2.AdapterPolicy{}, &kaprov1alpha2.Backend{}).
		WithIndex(&kaprov1alpha2.AdapterPolicy{}, adapterPolicyBackendRefIndex, func(obj client.Object) []string {
			policy, ok := obj.(*kaprov1alpha2.AdapterPolicy)
			if !ok || policy.Spec.BackendRef == "" {
				return nil
			}
			return []string{policy.Spec.BackendRef}
		}).
		Build()
}

type fakeDiscoveryAdapter struct {
	driver   kaprov1alpha2.BackendDriver
	runtime  kaprov1alpha2.BackendRuntime
	discover func(context.Context, kaproadapter.DiscoveryRequest) (kaproadapter.DiscoveryResult, error)
}

func (a *fakeDiscoveryAdapter) Driver() kaprov1alpha2.BackendDriver { return a.driver }
func (a *fakeDiscoveryAdapter) Runtime() kaprov1alpha2.BackendRuntime {
	return a.runtime
}
func (a *fakeDiscoveryAdapter) Capabilities() kaproadapter.Capabilities {
	return kaproadapter.Capabilities{
		Driver:           a.driver,
		Runtime:          a.runtime,
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
	return adapterPolicyConditionByType(t, conditions, kaprov1alpha2.ConditionTypeReady)
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

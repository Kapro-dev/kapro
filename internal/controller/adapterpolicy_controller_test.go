package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestAdapterPolicyReconcilerReadyWhenBackendExists(t *testing.T) {
	ctx := context.Background()
	c := adapterPolicyClient(t,
		&kaprov1alpha2.Backend{ObjectMeta: metav1.ObjectMeta{Name: "argo"}, Spec: kaprov1alpha2.BackendSpec{Driver: kaprov1alpha2.BackendDriverArgo}},
		&kaprov1alpha2.AdapterPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "adopt-argo", Generation: 1},
			Spec: kaprov1alpha2.AdapterPolicySpec{
				Adapter:    "argo-cd",
				BackendRef: "argo",
			},
		},
	)
	r := &AdapterPolicyReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adopt-argo"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got kaprov1alpha2.AdapterPolicy
	if err := c.Get(ctx, client.ObjectKey{Name: "adopt-argo"}, &got); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !got.Status.Ready || got.Status.Reason != "DiscoveryScheduled" {
		t.Fatalf("status = %#v, want ready DiscoveryScheduled", got.Status)
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
		WithStatusSubresource(&kaprov1alpha2.AdapterPolicy{}).
		WithIndex(&kaprov1alpha2.AdapterPolicy{}, adapterPolicyBackendRefIndex, func(obj client.Object) []string {
			policy, ok := obj.(*kaprov1alpha2.AdapterPolicy)
			if !ok || policy.Spec.BackendRef == "" {
				return nil
			}
			return []string{policy.Spec.BackendRef}
		}).
		Build()
}

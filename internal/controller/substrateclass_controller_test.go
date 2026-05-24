package controller

import (
	"context"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestSubstrateClassReconcilerPublishesBuiltInArgoContract(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &kaprov1alpha2.SubstrateClass{
		ObjectMeta: metav1.ObjectMeta{Name: "argo-cd", Generation: 2},
		Spec: kaprov1alpha2.SubstrateClassSpec{
			ControllerName: "kapro.io/argo-cd",
		},
	}
	r := &SubstrateClassReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(class).
			WithStatusSubresource(&kaprov1alpha2.SubstrateClass{}).
			Build(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "argo-cd"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got kaprov1alpha2.SubstrateClass
	if err := r.Get(context.Background(), client.ObjectKey{Name: "argo-cd"}, &got); err != nil {
		t.Fatal(err)
	}
	accepted := apimeta.FindStatusCondition(got.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("Accepted condition = %#v, want True", accepted)
	}
	if got.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration=%d, want 2", got.Status.ObservedGeneration)
	}
	if got.Status.ExecutionModes == nil || len(got.Status.ExecutionModes.Supported) != 2 {
		t.Fatalf("supported execution modes = %#v", got.Status.ExecutionModes)
	}
	if len(got.Status.AcceptedConfigKinds) != 1 || got.Status.AcceptedConfigKinds[0].Kind != "ArgoCDSubstrateConfig" {
		t.Fatalf("accepted config kinds = %#v", got.Status.AcceptedConfigKinds)
	}
	if got.Status.Capabilities == nil || got.Status.Capabilities.Operations == nil || !got.Status.Capabilities.Operations.Discover {
		t.Fatalf("capabilities = %#v, want discovery support", got.Status.Capabilities)
	}
}

func TestSubstrateClassReconcilerRejectsUnknownKaproController(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &kaprov1alpha2.SubstrateClass{
		ObjectMeta: metav1.ObjectMeta{Name: "unknown", Generation: 1},
		Spec: kaprov1alpha2.SubstrateClassSpec{
			ControllerName: "kapro.io/unknown",
		},
	}
	r := &SubstrateClassReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(class).
			WithStatusSubresource(&kaprov1alpha2.SubstrateClass{}).
			Build(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "unknown"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got kaprov1alpha2.SubstrateClass
	if err := r.Get(context.Background(), client.ObjectKey{Name: "unknown"}, &got); err != nil {
		t.Fatal(err)
	}
	accepted := apimeta.FindStatusCondition(got.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != "UnknownController" {
		t.Fatalf("Accepted condition = %#v, want UnknownController false", accepted)
	}
}

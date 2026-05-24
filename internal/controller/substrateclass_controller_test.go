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

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestSubstrateClassReconcilerPublishesBuiltInArgoContract(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &kaprov1alpha1.SubstrateClass{
		ObjectMeta: metav1.ObjectMeta{Name: "argo", Generation: 2},
		Spec: kaprov1alpha1.SubstrateClassSpec{
			ControllerName: "kapro.io/argo",
		},
	}
	r := &SubstrateClassReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(class).
			WithStatusSubresource(&kaprov1alpha1.SubstrateClass{}).
			Build(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "argo"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got kaprov1alpha1.SubstrateClass
	if err := r.Get(context.Background(), client.ObjectKey{Name: "argo"}, &got); err != nil {
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

func TestSubstrateClassReconcilerPublishesPublicPreviewContracts(t *testing.T) {
	tests := []struct {
		name       string
		controller string
		configKind string
		configAPI  string
		inputType  string
		discover   bool
	}{
		{
			name:       "kubernetes-apply",
			controller: "kapro.io/kubernetes-apply",
			configKind: "KubernetesApplyConfig",
			configAPI:  "kubernetes.substrate.kapro.io/v1alpha1",
			inputType:  "raw-yaml",
		},
		{
			name:       "argo",
			controller: "kapro.io/argo",
			configKind: "ArgoCDSubstrateConfig",
			configAPI:  "argocd.substrate.kapro.io/v1alpha1",
			inputType:  "git-revision",
			discover:   true,
		},
		{
			name:       "flux",
			controller: "kapro.io/flux",
			configKind: "FluxSubstrateConfig",
			configAPI:  "flux.substrate.kapro.io/v1alpha1",
			inputType:  "git-revision",
			discover:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			class := &kaprov1alpha1.SubstrateClass{
				ObjectMeta: metav1.ObjectMeta{Name: tt.name, Generation: 3},
				Spec: kaprov1alpha1.SubstrateClassSpec{
					ControllerName: tt.controller,
				},
			}
			r := &SubstrateClassReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(class).
					WithStatusSubresource(&kaprov1alpha1.SubstrateClass{}).
					Build(),
			}

			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: tt.name}}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			var got kaprov1alpha1.SubstrateClass
			if err := r.Get(context.Background(), client.ObjectKey{Name: tt.name}, &got); err != nil {
				t.Fatal(err)
			}
			accepted := apimeta.FindStatusCondition(got.Status.Conditions, "Accepted")
			if accepted == nil || accepted.Status != metav1.ConditionTrue {
				t.Fatalf("Accepted condition = %#v, want True", accepted)
			}
			if len(got.Status.AcceptedConfigKinds) != 1 {
				t.Fatalf("accepted config kinds = %#v", got.Status.AcceptedConfigKinds)
			}
			if got.Status.AcceptedConfigKinds[0].APIVersion != tt.configAPI ||
				got.Status.AcceptedConfigKinds[0].Kind != tt.configKind {
				t.Fatalf("accepted config kinds = %#v, want %s/%s", got.Status.AcceptedConfigKinds, tt.configAPI, tt.configKind)
			}
			if got.Status.Capabilities == nil || got.Status.Capabilities.Operations == nil {
				t.Fatalf("capabilities = %#v, want operation bits", got.Status.Capabilities)
			}
			if !got.Status.Capabilities.Operations.Apply || !got.Status.Capabilities.Operations.Observe || !got.Status.Capabilities.Operations.DryRun {
				t.Fatalf("capabilities = %#v, want apply/observe/dryRun", got.Status.Capabilities.Operations)
			}
			if got.Status.Capabilities.Operations.Discover != tt.discover {
				t.Fatalf("discover=%v, want %v", got.Status.Capabilities.Operations.Discover, tt.discover)
			}
			if !containsSubstrateInputType(got.Status.Capabilities.InputTypes, tt.inputType) {
				t.Fatalf("inputTypes=%v, want %q", got.Status.Capabilities.InputTypes, tt.inputType)
			}
		})
	}
}

func TestSubstrateClassReconcilerRejectsUnknownKaproController(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := &kaprov1alpha1.SubstrateClass{
		ObjectMeta: metav1.ObjectMeta{Name: "unknown", Generation: 1},
		Spec: kaprov1alpha1.SubstrateClassSpec{
			ControllerName: "kapro.io/unknown",
		},
	}
	r := &SubstrateClassReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(class).
			WithStatusSubresource(&kaprov1alpha1.SubstrateClass{}).
			Build(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "unknown"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got kaprov1alpha1.SubstrateClass
	if err := r.Get(context.Background(), client.ObjectKey{Name: "unknown"}, &got); err != nil {
		t.Fatal(err)
	}
	accepted := apimeta.FindStatusCondition(got.Status.Conditions, "Accepted")
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != "UnknownController" {
		t.Fatalf("Accepted condition = %#v, want UnknownController false", accepted)
	}
}

func containsSubstrateInputType(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

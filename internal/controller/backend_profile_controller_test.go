package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestBackendProfileReadinessBuiltIn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.BackendProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha1.BackendProfileSpec{
			Driver:  kaprov1alpha1.BackendDriverArgo,
			Runtime: kaprov1alpha1.BackendRuntimeHub,
		},
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build(),
	}

	ready, reason, _ := r.profileReadiness(context.Background(), profile)
	if !ready {
		t.Fatalf("ready=false reason=%s", reason)
	}
	if reason != "BuiltInBackendReady" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestBackendProfileReadinessExternalRequiresReadyPlugin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.BackendProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Spec: kaprov1alpha1.BackendProfileSpec{
			Driver:    kaprov1alpha1.BackendDriverExternal,
			Runtime:   kaprov1alpha1.BackendRuntimeBoth,
			PluginRef: "custom-backend",
		},
	}
	plugin := &kaprov1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-backend", Generation: 2},
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "custom",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "dns:///custom-backend:9090",
		},
		Status: kaprov1alpha1.PluginRegistrationStatus{
			ObservedGeneration: 2,
			Ready:              true,
		},
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, plugin).Build(),
	}

	ready, reason, _ := r.profileReadiness(context.Background(), profile)
	if !ready {
		t.Fatalf("ready=false reason=%s", reason)
	}
	if reason != "ExternalBackendReady" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestBackendProfileArgoDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.BackendProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha1.BackendProfileSpec{
			Driver: kaprov1alpha1.BackendDriverArgo,
			Discovery: &kaprov1alpha1.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace("argocd")
	app.SetName("checkout-prod")

	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			profile,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prod",
					Namespace: "argocd",
					Labels:    map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
				},
			},
			app,
		).Build(),
	}

	counts, reason, _ := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.clusters != 1 || counts.applications != 1 {
		t.Fatalf("clusters=%d applications=%d", counts.clusters, counts.applications)
	}
}

func TestBackendProfileFluxDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.BackendProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.BackendProfileSpec{
			Driver: kaprov1alpha1.BackendDriverFlux,
			Discovery: &kaprov1alpha1.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "flux-system"},
		},
	}
	helmRelease := &unstructured.Unstructured{}
	helmRelease.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"})
	helmRelease.SetNamespace("flux-system")
	helmRelease.SetName("checkout-api")
	kustomization := &unstructured.Unstructured{}
	kustomization.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	kustomization.SetNamespace("flux-system")
	kustomization.SetName("checkout")

	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, helmRelease, kustomization).Build(),
	}

	counts, reason, _ := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 2 {
		t.Fatalf("applications=%d", counts.applications)
	}
}

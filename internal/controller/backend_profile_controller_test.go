package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestBackendProfileReadinessBuiltIn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver:  kaprov1alpha2.BackendDriverArgo,
			Runtime: kaprov1alpha2.BackendRuntimeHub,
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
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver:    kaprov1alpha2.BackendDriverExternal,
			Runtime:   kaprov1alpha2.BackendRuntimeBoth,
			PluginRef: "custom-backend",
		},
	}
	plugin := &kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-backend", Generation: 2},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeActuator,
			Name:     "custom",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "dns:///custom-backend:9090",
		},
		Status: kaprov1alpha2.PluginStatus{
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

func TestBackendProfilesForBackendObjectMatchesDiscoveryProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kapro.io/import": "true"},
				},
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	fluxProfile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverFlux,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "flux-system"},
		},
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, fluxProfile).Build(),
	}
	app := newArgoApplication("argocd", "checkout", map[string]string{"kapro.io/import": "true"}, nil)

	requests := r.backendProfilesForBackendObject(context.Background(), app)
	if len(requests) != 1 || requests[0].Name != "argo" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestBackendProfilesForBackendObjectMatchesArgoClusterSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build(),
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "cluster-a",
		Namespace: "argocd",
		Labels:    map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
	}}

	requests := r.backendProfilesForBackendObject(context.Background(), secret)
	if len(requests) != 1 || requests[0].Name != "argo" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestBackendProfileArgoDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	app := newArgoApplication("argocd", "checkout-prod", map[string]string{"kapro.io/import": "true", "service": "checkout"}, nil)

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
	if len(counts.selected) != 2 {
		t.Fatalf("selected=%d, want cluster secret and application", len(counts.selected))
	}
	if counts.selected[1].Pattern != "application" || counts.selected[1].VersionField != "spec.source.targetRevision" {
		t.Fatalf("unexpected selected application: %#v", counts.selected[1])
	}
}

func TestBackendProfileArgoDiscoveryClassifiesBrownfieldPatterns(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
					"kapro.io/import": "true",
				}},
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	appSetOwner := []metav1.OwnerReference{{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "ApplicationSet",
		Name:       "checkout-prod",
		UID:        "apps-1",
	}}
	plain := newArgoApplication("argocd", "checkout-web-prod", map[string]string{"kapro.io/import": "true", "service": "web"}, nil)
	appSetChild := newArgoApplication("argocd", "checkout-api-prod", map[string]string{"kapro.io/import": "true", "service": "api"}, appSetOwner)
	root := newArgoApplication("argocd", "platform-root", map[string]string{"kapro.io/import": "true", "pattern": "app-of-apps"}, nil)
	appSet := newApplicationSet("argocd", "checkout-prod", map[string]string{"kapro.io/import": "true", "service": "api"})

	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, plain, appSetChild, root, appSet).Build(),
	}

	counts, reason, _ := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 3 || counts.applicationSets != 1 {
		t.Fatalf("applications=%d applicationSets=%d", counts.applications, counts.applicationSets)
	}
	if len(counts.selected) != 1 {
		t.Fatalf("selected=%d, want plain app only", len(counts.selected))
	}
	if !hasDiscoveryPattern(counts.selected, "application") {
		t.Fatalf("selected does not include plain application: %#v", counts.selected)
	}
	if len(counts.unsupported) != 1 || counts.unsupported[0].Pattern != "app-of-apps-root" {
		t.Fatalf("unsupported=%#v", counts.unsupported)
	}
	if len(counts.skipped) != 2 {
		t.Fatalf("skipped=%#v", counts.skipped)
	}
	if !hasDiscoveryPattern(counts.skipped, "applicationset-child") {
		t.Fatalf("skipped does not include applicationset-child: %#v", counts.skipped)
	}
	if !hasDiscoveryKind(counts.skipped, "ApplicationSet") {
		t.Fatalf("skipped does not include ApplicationSet: %#v", counts.skipped)
	}
}

func TestBackendProfileDiscoveryStatusSamplesAreBounded(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	objects := []client.Object{profile}
	for i := 0; i < 1000; i++ {
		objects = append(objects, newArgoApplication("argocd", fmt.Sprintf("app-%04d", i), map[string]string{"service": "checkout"}, nil))
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
	}

	counts, reason, message := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 1000 {
		t.Fatalf("applications=%d", counts.applications)
	}
	if len(counts.selected) != maxBackendDiscoveryStatusObjects {
		t.Fatalf("selected sample=%d want %d", len(counts.selected), maxBackendDiscoveryStatusObjects)
	}
	if !strings.Contains(message, "sampled selected objects") {
		t.Fatalf("summary does not identify sampled counts: %q", message)
	}
}

func TestBackendProfileDiscoveryFailsClosedWhenMaxObjectsExceeded(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverArgo,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled:    true,
				MaxObjects: 1,
			},
			Parameters: map[string]string{"namespace": "argocd"},
		},
	}
	r := &BackendProfileReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			profile,
			newArgoApplication("argocd", "app-1", map[string]string{"service": "checkout"}, nil),
			newArgoApplication("argocd", "app-2", map[string]string{"service": "checkout"}, nil),
		).Build(),
	}

	_, reason, message := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoveryLimitExceeded" {
		t.Fatalf("reason=%s message=%s", reason, message)
	}
}

func TestBackendProfileFluxDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: kaprov1alpha2.BackendDriverFlux,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled: true,
			},
			Parameters: map[string]string{"namespace": "flux-system"},
		},
	}
	gitRepository := &unstructured.Unstructured{}
	gitRepository.SetGroupVersionKind(schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"})
	gitRepository.SetNamespace("flux-system")
	gitRepository.SetName("checkout-git")
	gitRepository.SetLabels(map[string]string{"service": "checkout"})
	if err := unstructured.SetNestedField(gitRepository.Object, "v1.0.0", "spec", "ref", "tag"); err != nil {
		t.Fatal(err)
	}
	ociRepository := &unstructured.Unstructured{}
	ociRepository.SetGroupVersionKind(schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "OCIRepository"})
	ociRepository.SetNamespace("flux-system")
	ociRepository.SetName("checkout-oci")
	ociRepository.SetLabels(map[string]string{"service": "checkout"})
	if err := unstructured.SetNestedField(ociRepository.Object, "1.x", "spec", "ref", "semver"); err != nil {
		t.Fatal(err)
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
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, gitRepository, ociRepository, helmRelease, kustomization).Build(),
	}

	counts, reason, _ := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 4 {
		t.Fatalf("applications=%d", counts.applications)
	}
	if len(counts.selected) != 4 {
		t.Fatalf("selected=%d", len(counts.selected))
	}
	if !hasDiscoveryPattern(counts.selected, "gitrepository") {
		t.Fatalf("selected does not include GitRepository: %#v", counts.selected)
	}
	if !hasDiscoveryPattern(counts.selected, "ocirepository") {
		t.Fatalf("selected does not include OCIRepository: %#v", counts.selected)
	}
	if !hasDiscoveryVersionField(counts.selected, "spec.ref.semver") {
		t.Fatalf("selected does not include semver source field: %#v", counts.selected)
	}
}

func newArgoApplication(namespace, name string, labels map[string]string, owners []metav1.OwnerReference) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace(namespace)
	app.SetName(name)
	app.SetLabels(labels)
	app.SetOwnerReferences(owners)
	return app
}

func newApplicationSet(namespace, name string, labels map[string]string) *unstructured.Unstructured {
	appSet := &unstructured.Unstructured{}
	appSet.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"})
	appSet.SetNamespace(namespace)
	appSet.SetName(name)
	appSet.SetLabels(labels)
	return appSet
}

func hasDiscoveryPattern(objects []kaprov1alpha2.DiscoveredBackendObject, pattern string) bool {
	for _, obj := range objects {
		if obj.Pattern == pattern {
			return true
		}
	}
	return false
}

func hasDiscoveryKind(objects []kaprov1alpha2.DiscoveredBackendObject, kind string) bool {
	for _, obj := range objects {
		if obj.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiscoveryVersionField(objects []kaprov1alpha2.DiscoveredBackendObject, field string) bool {
	for _, obj := range objects {
		if obj.VersionField == field {
			return true
		}
	}
	return false
}

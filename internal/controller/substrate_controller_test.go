package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestSubstrateProfileReadinessBuiltIn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec:       substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush, nil, nil),
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build(),
	}

	ready, reason, _ := r.profileReadiness(context.Background(), profile)
	if !ready {
		t.Fatalf("ready=false reason=%s", reason)
	}
	if reason != "BuiltInSubstrateReady" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestSubstrateProfileReadinessExternalRequiresReadyPlugin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "custom"},
		Spec:       substrateTestSpec("external", kaprov1alpha1.ExecutionModeExternalPull, nil, nil),
	}
	profile.Spec.PluginRef = "custom-substrate"
	plugin := &kaprov1alpha1.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-substrate", Generation: 2},
		Spec: kaprov1alpha1.PluginSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "custom",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "dns:///custom-substrate:9090",
		},
		Status: kaprov1alpha1.PluginStatus{
			ObservedGeneration: 2,
			Ready:              true,
		},
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, plugin).Build(),
	}

	ready, reason, _ := r.profileReadiness(context.Background(), profile)
	if !ready {
		t.Fatalf("ready=false reason=%s", reason)
	}
	if reason != "ExternalSubstrateReady" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestSubstrateProfileReadinessSubstrateClassConfigRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := acceptedSubstrateClass("argo", "kapro.io/argo",
		[]kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		kaprov1alpha1.SubstrateObjectKindReference{
			APIVersion: "argocd.substrate.kapro.io/v1alpha1",
			Kind:       "ArgoCDSubstrateConfig",
		},
	)
	config := typedSubstrateConfig("argocd.substrate.kapro.io/v1alpha1", "ArgoCDSubstrateConfig", "", "prod-argo")
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-argo"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "argo"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "argocd.substrate.kapro.io/v1alpha1",
				Kind:       "ArgoCDSubstrateConfig",
				Name:       "prod-argo",
			},
		},
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, class, config).Build(),
	}

	ready, reason, message := r.profileReadiness(context.Background(), profile)
	if !ready || reason != "SubstrateClassSubstrateReady" {
		t.Fatalf("ready=%v reason=%s message=%s", ready, reason, message)
	}
}

func TestSubstrateProfileReadinessSubstrateClassRejectsMissingConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := acceptedSubstrateClass("argo", "kapro.io/argo",
		[]kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		kaprov1alpha1.SubstrateObjectKindReference{
			APIVersion: "argocd.substrate.kapro.io/v1alpha1",
			Kind:       "ArgoCDSubstrateConfig",
		},
	)
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-argo"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "argo"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "argocd.substrate.kapro.io/v1alpha1",
				Kind:       "ArgoCDSubstrateConfig",
				Name:       "missing",
			},
		},
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, class).Build(),
	}

	ready, reason, _ := r.profileReadiness(context.Background(), profile)
	if ready || reason != "ConfigNotFound" {
		t.Fatalf("ready=%v reason=%s, want ConfigNotFound", ready, reason)
	}
}

func TestSubstrateReconcilerWritesClassAndConfigConditions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	class := acceptedSubstrateClass("argo", "kapro.io/argo",
		[]kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		kaprov1alpha1.SubstrateObjectKindReference{
			APIVersion: "argocd.substrate.kapro.io/v1alpha1",
			Kind:       "ArgoCDSubstrateConfig",
		},
	)
	config := typedSubstrateConfig("argocd.substrate.kapro.io/v1alpha1", "ArgoCDSubstrateConfig", "", "prod-argo")
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-argo"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "argo"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "argocd.substrate.kapro.io/v1alpha1",
				Kind:       "ArgoCDSubstrateConfig",
				Name:       "prod-argo",
			},
		},
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(profile, class, config).
			WithStatusSubresource(&kaprov1alpha1.Substrate{}).
			Build(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "prod-argo"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got kaprov1alpha1.Substrate
	if err := r.Get(context.Background(), client.ObjectKey{Name: "prod-argo"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ClassName != "argo" || got.Status.ConfigRef == nil {
		t.Fatalf("status class/config not mirrored: %#v", got.Status)
	}
	for _, condType := range []string{"Ready", "ClassAccepted", "ConfigAccepted"} {
		cond := apimeta.FindStatusCondition(got.Status.Conditions, condType)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Fatalf("%s condition = %#v, want True", condType, cond)
		}
	}
}

func TestSubstrateProfilesForSubstrateObjectMatchesDiscoveryProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kapro.io/import": "true"},
				},
			},
			map[string]string{"namespace": "argocd"}),
	}
	fluxProfile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: substrateTestSpec("flux", kaprov1alpha1.ExecutionModeSpokePull,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
			},
			map[string]string{"namespace": "flux-system"}),
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, fluxProfile).Build(),
	}
	app := newArgoApplication("argocd", "checkout", map[string]string{"kapro.io/import": "true"}, nil)

	requests := r.substrateProfilesForSubstrateObject(context.Background(), app)
	if len(requests) != 1 || requests[0].Name != "argo" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestSubstrateProfilesForSubstrateObjectMatchesArgoClusterSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
			},
			map[string]string{"namespace": "argocd"}),
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build(),
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "cluster-a",
		Namespace: "argocd",
		Labels:    map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
	}}

	requests := r.substrateProfilesForSubstrateObject(context.Background(), secret)
	if len(requests) != 1 || requests[0].Name != "argo" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestSubstrateProfileArgoDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
			},
			map[string]string{"namespace": "argocd"}),
	}
	app := newArgoApplication("argocd", "checkout-prod", map[string]string{"kapro.io/import": "true", "service": "checkout"}, nil)

	r := &SubstrateReconciler{
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

func TestSubstrateProfileDiscoveryUsesTypedConfigNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	config := typedSubstrateConfigWithSpecNamespace("flux.substrate.kapro.io/v1alpha1", "FluxSubstrateConfig", "checkout", "flux-managed")
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "flux.substrate.kapro.io/v1alpha1",
				Kind:       "FluxSubstrateConfig",
				Name:       "checkout",
			},
			Discovery:  &kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true},
			Parameters: map[string]string{"namespace": "wrong-namespace"},
		},
	}
	gitRepository := &unstructured.Unstructured{}
	gitRepository.SetGroupVersionKind(schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"})
	gitRepository.SetNamespace("flux-managed")
	gitRepository.SetName("checkout-git")

	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, config, gitRepository).Build(),
	}

	counts, reason, _ := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 1 {
		t.Fatalf("applications=%d, want typed config namespace object to be discovered", counts.applications)
	}
}

func TestSubstrateProfileObjectWatchUsesTypedConfigNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	config := typedSubstrateConfigWithSpecNamespace("flux.substrate.kapro.io/v1alpha1", "FluxSubstrateConfig", "checkout", "flux-managed")
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "flux.substrate.kapro.io/v1alpha1",
				Kind:       "FluxSubstrateConfig",
				Name:       "checkout",
			},
			Discovery:  &kaprov1alpha1.SubstrateDiscoverySpec{Enabled: true},
			Parameters: map[string]string{"namespace": "wrong-namespace"},
		},
	}
	gitRepository := &unstructured.Unstructured{}
	gitRepository.SetGroupVersionKind(schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"})
	gitRepository.SetNamespace("flux-managed")
	gitRepository.SetName("checkout-git")

	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, config).Build(),
	}

	requests := r.substrateProfilesForSubstrateObject(context.Background(), gitRepository)
	if len(requests) != 1 || requests[0].Name != "flux" {
		t.Fatalf("requests=%#v, want flux profile from typed config namespace", requests)
	}
}

func TestSubstrateProfileArgoDiscoveryClassifiesExistingGitOpsPatterns(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
					"kapro.io/import": "true",
				}},
			},
			map[string]string{"namespace": "argocd"}),
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

	r := &SubstrateReconciler{
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

func TestSubstrateProfileDiscoveryStatusSamplesAreBounded(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
			},
			map[string]string{"namespace": "argocd"}),
	}
	objects := []client.Object{profile}
	for i := 0; i < 1000; i++ {
		objects = append(objects, newArgoApplication("argocd", fmt.Sprintf("app-%04d", i), map[string]string{"service": "checkout"}, nil))
	}
	r := &SubstrateReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
	}

	counts, reason, message := r.observeDiscovery(context.Background(), profile)
	if reason != "DiscoverySucceeded" {
		t.Fatalf("reason=%s", reason)
	}
	if counts.applications != 1000 {
		t.Fatalf("applications=%d", counts.applications)
	}
	if len(counts.selected) != maxSubstrateDiscoveryStatusObjects {
		t.Fatalf("selected sample=%d want %d", len(counts.selected), maxSubstrateDiscoveryStatusObjects)
	}
	if !strings.Contains(message, "sampled selected objects") {
		t.Fatalf("summary does not identify sampled counts: %q", message)
	}
}

func TestSubstrateProfileDiscoveryFailsClosedWhenMaxObjectsExceeded(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: substrateTestSpec("argo", kaprov1alpha1.ExecutionModeHubPush,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled:    true,
				MaxObjects: 1,
			},
			map[string]string{"namespace": "argocd"}),
	}
	r := &SubstrateReconciler{
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

func TestSubstrateProfileFluxDiscoveryCountsExistingResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	profile := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: substrateTestSpec("flux", kaprov1alpha1.ExecutionModeSpokePull,
			&kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled: true,
			},
			map[string]string{"namespace": "flux-system"}),
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

	r := &SubstrateReconciler{
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

func substrateTestSpec(kind string, mode kaprov1alpha1.ExecutionMode, discovery *kaprov1alpha1.SubstrateDiscoverySpec, parameters map[string]string) kaprov1alpha1.SubstrateSpec {
	return kaprov1alpha1.SubstrateSpec{
		Substrate:  &kaprov1alpha1.SubstrateImplementationSpec{Kind: kind, Actuator: kind},
		Execution:  &kaprov1alpha1.SubstrateExecutionSpec{Mode: mode},
		Discovery:  discovery,
		Parameters: parameters,
	}
}

func acceptedSubstrateClass(name, controllerName string, modes []kaprov1alpha1.ExecutionMode, configKind kaprov1alpha1.SubstrateObjectKindReference) *kaprov1alpha1.SubstrateClass {
	class := &kaprov1alpha1.SubstrateClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: kaprov1alpha1.SubstrateClassSpec{
			ControllerName: controllerName,
		},
		Status: kaprov1alpha1.SubstrateClassStatus{
			ObservedGeneration: 1,
			ExecutionModes:     &kaprov1alpha1.SubstrateClassExecutionModesStatus{Supported: modes},
			AcceptedConfigKinds: []kaprov1alpha1.SubstrateObjectKindReference{
				configKind,
			},
			Conditions: []metav1.Condition{{
				Type:               "Accepted",
				Status:             metav1.ConditionTrue,
				Reason:             "BuiltInClassAccepted",
				Message:            "accepted for test",
				ObservedGeneration: 1,
			}},
		},
	}
	return class
}

func typedSubstrateConfig(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		panic(err)
	}
	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(gv.WithKind(kind))
	config.SetNamespace(namespace)
	config.SetName(name)
	return config
}

func typedSubstrateConfigWithSpecNamespace(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	config := typedSubstrateConfig(apiVersion, kind, "", name)
	if err := unstructured.SetNestedField(config.Object, namespace, "spec", "namespace"); err != nil {
		panic(err)
	}
	return config
}

func hasDiscoveryPattern(objects []kaprov1alpha1.DiscoveredSubstrateObject, pattern string) bool {
	for _, obj := range objects {
		if obj.Pattern == pattern {
			return true
		}
	}
	return false
}

func hasDiscoveryKind(objects []kaprov1alpha1.DiscoveredSubstrateObject, kind string) bool {
	for _, obj := range objects {
		if obj.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiscoveryVersionField(objects []kaprov1alpha1.DiscoveredSubstrateObject, field string) bool {
	for _, obj := range objects {
		if obj.VersionField == field {
			return true
		}
	}
	return false
}

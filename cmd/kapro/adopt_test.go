package main

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	argocdsubstratev1alpha1 "kapro.io/kapro/api/substrate/argocd/v1alpha1"
	fluxsubstratev1alpha1 "kapro.io/kapro/api/substrate/flux/v1alpha1"
)

func TestCreateOrUpdateObjectDryRunUsesClientDryRun(t *testing.T) {
	ctx := context.Background()
	c := &recordingAdoptClient{Client: fakeAdoptClient(t)}
	substrate := &kaprov1alpha1.Substrate{ObjectMeta: metav1.ObjectMeta{Name: "flux"}}

	if err := createOrUpdateObject(ctx, c, substrate, true); err != nil {
		t.Fatalf("createOrUpdateObject dry-run: %v", err)
	}
	if !c.createDryRun {
		t.Fatal("expected create to receive client.DryRunAll")
	}
	var got kaprov1alpha1.Substrate
	if err := c.Get(ctx, client.ObjectKey{Name: "flux"}, &got); err == nil {
		t.Fatal("dry-run create persisted object")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get dry-run object: %v", err)
	}
}

func TestImportArgoLiveApplyFlags(t *testing.T) {
	cmd := newImportArgoCmd()
	for _, name := range []string{"adopt", "apply", "dry-run", "kubeconfig", "sync-interval", "take"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("import argo missing --%s flag", name)
		}
	}
	if !cmd.Flags().Lookup("take").Hidden {
		t.Fatal("deprecated --take alias should be hidden from help")
	}
}

func TestImportFluxLiveApplyFlags(t *testing.T) {
	cmd := newImportFluxCmd()
	for _, name := range []string{"adopt", "apply", "dry-run", "kubeconfig", "sync-interval", "take"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("import flux missing --%s flag", name)
		}
	}
	if !cmd.Flags().Lookup("take").Hidden {
		t.Fatal("deprecated --take alias should be hidden from help")
	}
}

func TestImportSubstrateObjectsUseClassRefAndTypedConfig(t *testing.T) {
	objects := importSubstrateObjects(importSubstrateOptions{
		SubstrateKind: "flux",
		SubstrateName: "checkout",
		Namespace:     "flux-system",
		SyncInterval:  "5m",
	}, "flux", "Adopt", map[string]string{"team": "checkout"})

	if got := len(objects); got != 4 {
		t.Fatalf("objects=%d, want class/config/substrate/policy", got)
	}
	class, ok := objects[0].(*kaprov1alpha1.SubstrateClass)
	if !ok || class.Name != "flux" || class.Spec.ControllerName != "kapro.io/flux" {
		t.Fatalf("class object=%#v", objects[0])
	}
	config, ok := objects[1].(*fluxsubstratev1alpha1.FluxSubstrateConfig)
	if !ok || config.Name != "checkout" || config.Spec.Namespace != "flux-system" {
		t.Fatalf("config object=%#v", objects[1])
	}
	substrate, ok := objects[2].(*kaprov1alpha1.Substrate)
	if !ok {
		t.Fatalf("substrate object=%#v", objects[2])
	}
	if substrate.Spec.ClassRef == nil || substrate.Spec.ClassRef.Name != "flux" {
		t.Fatalf("classRef=%#v, want flux", substrate.Spec.ClassRef)
	}
	if substrate.Spec.ConfigRef == nil ||
		substrate.Spec.ConfigRef.APIVersion != fluxSubstrateConfigAPIVersion ||
		substrate.Spec.ConfigRef.Kind != "FluxSubstrateConfig" ||
		substrate.Spec.ConfigRef.Name != "checkout" {
		t.Fatalf("configRef=%#v, want FluxSubstrateConfig checkout", substrate.Spec.ConfigRef)
	}
	if _, ok := substrate.Spec.Parameters["namespace"]; ok {
		t.Fatalf("live import should keep namespace only on typed config, got parameters=%#v", substrate.Spec.Parameters)
	}
	if substrate.Spec.Discovery == nil ||
		substrate.Spec.Discovery.ManagementPolicy != "Adopt" ||
		substrate.Spec.Discovery.Selector == nil ||
		substrate.Spec.Discovery.Selector.MatchLabels["team"] != "checkout" {
		t.Fatalf("discovery=%#v, want adopt selector", substrate.Spec.Discovery)
	}
	policy, ok := objects[3].(*kaprov1alpha1.SubstrateDiscoveryPolicy)
	if !ok || policy.Spec.SubstrateRef != "checkout" || policy.Spec.ExpectedKind != "flux" {
		t.Fatalf("policy object=%#v", objects[3])
	}
}

func TestDiscoverSubstrateFileSuffix(t *testing.T) {
	if got := discoverSubstrateFileSuffix(false); got != "-observe" {
		t.Fatalf("observe suffix=%q", got)
	}
	if got := discoverSubstrateFileSuffix(true); got != "-adopt" {
		t.Fatalf("adopt suffix=%q", got)
	}
}

func TestImportAdoptRendersAdoptModeSubstrates(t *testing.T) {
	labels := map[string]string{"kapro.io/import": "true"}
	argo := renderArgoDiscoverSubstrate(argoDiscoverOptions{Name: "checkout", Namespace: "argocd", Adopt: true}, labels)
	for _, want := range []string{"kind: SubstrateClass", "kind: ArgoCDSubstrateConfig", "classRef:", "configRef:", "managementPolicy: Adopt"} {
		if !strings.Contains(argo, want) {
			t.Fatalf("argo substrate missing %q:\n%s", want, argo)
		}
	}
	if strings.Contains(argo, "actuator:") {
		t.Fatalf("argo substrate should not emit legacy actuator field:\n%s", argo)
	}
	flux := renderFluxDiscoverSubstrate(fluxDiscoverOptions{Name: "checkout", Namespace: "flux-system", Adopt: false}, labels)
	for _, want := range []string{"kind: SubstrateClass", "kind: FluxSubstrateConfig", "classRef:", "configRef:", "managementPolicy: Observe"} {
		if !strings.Contains(flux, want) {
			t.Fatalf("flux substrate missing %q:\n%s", want, flux)
		}
	}
	if strings.Contains(flux, "actuator:") {
		t.Fatalf("flux substrate should not emit legacy actuator field:\n%s", flux)
	}
}

func TestImportAdoptReadmeReferencesAdoptSubstrate(t *testing.T) {
	argo := renderArgoDiscoverReadme(argoDiscoverOptions{Name: "checkout", Adopt: true}, argoDiscoveryResult{})
	for _, want := range []string{
		"After review, apply Adopt-mode resources:",
		"kubectl apply -f substrates/checkout-adopt.yaml",
		"deliveryunits/checkout.yaml",
		"before running the Adopt-mode apply command below",
	} {
		if !strings.Contains(argo, want) {
			t.Fatalf("argo README missing %q:\n%s", want, argo)
		}
	}
	if strings.Contains(argo, "checkout-observe.yaml") {
		t.Fatalf("argo README should not reference observe substrate in adopt mode:\n%s", argo)
	}
	if strings.Contains(argo, "sources/checkout.yaml") {
		t.Fatalf("argo README should reference DeliveryUnit source mapping in adopt mode:\n%s", argo)
	}

	flux := renderFluxDiscoverReadme(fluxDiscoverOptions{Name: "checkout", Adopt: true}, fluxDiscoveryResult{})
	for _, want := range []string{
		"After review, apply Adopt-mode resources:",
		"kubectl apply -f substrates/checkout-adopt.yaml",
		"deliveryunits/checkout.yaml",
		"before running the Adopt-mode apply command below",
	} {
		if !strings.Contains(flux, want) {
			t.Fatalf("flux README missing %q:\n%s", want, flux)
		}
	}
	if strings.Contains(flux, "checkout-observe.yaml") {
		t.Fatalf("flux README should not reference observe substrate in adopt mode:\n%s", flux)
	}
	if strings.Contains(flux, "sources/checkout.yaml") {
		t.Fatalf("flux README should reference DeliveryUnit source mapping in adopt mode:\n%s", flux)
	}
}

func TestCreateOrUpdateObjectPatchPreservesExistingMetadata(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "flux",
			Labels:            map[string]string{"user": "kept"},
			Annotations:       map[string]string{"note": "kept"},
			Finalizers:        []string{"kapro.io/finalizer"},
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "kapro.io/v1alpha1", Kind: "Source", Name: "owner", UID: types.UID("owner-uid")}},
			UID:               types.UID("substrate-uid"),
			CreationTimestamp: metav1.NewTime(time.Unix(1700000000, 0).UTC()),
			Generation:        7,
			ResourceVersion:   "1",
			ManagedFields: []metav1.ManagedFieldsEntry{{
				Manager:    "kubectl",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "kapro.io/v1alpha1",
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: []byte("{}")},
			}},
		},
		Spec: testSubstrateSpec("flux", kaprov1alpha1.ExecutionModeSpokePull),
	}
	c := fakeAdoptClient(t, existing)
	desired := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "flux",
			Labels:      map[string]string{"kapro.io/managed-by": "kapro"},
			Annotations: map[string]string{"kapro.io/source": "import"},
		},
		Spec: testSubstrateSpec("flux", kaprov1alpha1.ExecutionModeSpokePull),
	}

	if err := createOrUpdateObject(ctx, c, desired, false); err != nil {
		t.Fatalf("createOrUpdateObject patch: %v", err)
	}
	var got kaprov1alpha1.Substrate
	if err := c.Get(ctx, client.ObjectKey{Name: "flux"}, &got); err != nil {
		t.Fatalf("get patched substrate: %v", err)
	}
	for key, want := range map[string]string{"user": "kept", "kapro.io/managed-by": "kapro"} {
		if got.Labels[key] != want {
			t.Fatalf("label %s=%q, want %q; labels=%#v", key, got.Labels[key], want, got.Labels)
		}
	}
	for key, want := range map[string]string{"note": "kept", "kapro.io/source": "import"} {
		if got.Annotations[key] != want {
			t.Fatalf("annotation %s=%q, want %q; annotations=%#v", key, got.Annotations[key], want, got.Annotations)
		}
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != "kapro.io/finalizer" {
		t.Fatalf("finalizers=%#v, want preserved finalizer", got.Finalizers)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].Name != "owner" {
		t.Fatalf("ownerReferences=%#v, want preserved owner reference", got.OwnerReferences)
	}
	if got.UID != existing.UID {
		t.Fatalf("uid=%q, want %q", got.UID, existing.UID)
	}
	if !got.CreationTimestamp.Equal(&existing.CreationTimestamp) {
		t.Fatalf("creationTimestamp=%s, want %s", got.CreationTimestamp, existing.CreationTimestamp)
	}
	if got.Generation != existing.Generation {
		t.Fatalf("generation=%d, want %d", got.Generation, existing.Generation)
	}
	if got.Spec.ClassRef == nil ||
		got.Spec.ClassRef.Name != "flux" ||
		got.Spec.ConfigRef == nil ||
		got.Spec.ConfigRef.Kind != "FluxSubstrateConfig" ||
		got.Spec.Execution == nil ||
		got.Spec.Execution.Mode != kaprov1alpha1.ExecutionModeSpokePull {
		t.Fatalf("spec=%#v, want import spec patched", got.Spec)
	}
}

func TestPreserveObjectMetadataKeepsServerOwnedFields(t *testing.T) {
	current := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "flux",
			UID:               types.UID("substrate-uid"),
			CreationTimestamp: metav1.NewTime(time.Unix(1700000000, 0).UTC()),
			Generation:        7,
			ResourceVersion:   "11",
			ManagedFields: []metav1.ManagedFieldsEntry{{
				Manager:    "kubectl",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "kapro.io/v1alpha1",
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: []byte("{}")},
			}},
		},
	}
	desired := &kaprov1alpha1.Substrate{ObjectMeta: metav1.ObjectMeta{Name: "flux"}}

	preserveObjectMetadata(current, desired)

	if desired.ResourceVersion != current.ResourceVersion {
		t.Fatalf("resourceVersion=%q, want %q", desired.ResourceVersion, current.ResourceVersion)
	}
	if desired.UID != current.UID {
		t.Fatalf("uid=%q, want %q", desired.UID, current.UID)
	}
	if !desired.CreationTimestamp.Equal(&current.CreationTimestamp) {
		t.Fatalf("creationTimestamp=%s, want %s", desired.CreationTimestamp, current.CreationTimestamp)
	}
	if desired.Generation != current.Generation {
		t.Fatalf("generation=%d, want %d", desired.Generation, current.Generation)
	}
	if len(desired.ManagedFields) != 1 || desired.ManagedFields[0].Manager != "kubectl" {
		t.Fatalf("managedFields=%#v, want preserved managed fields", desired.ManagedFields)
	}
}

func TestCreateOrUpdateObjectPatchDryRunUsesClientDryRun(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
		},
	}
	c := &recordingAdoptClient{Client: fakeAdoptClient(t, existing)}
	desired := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec:       testSubstrateSpec("flux", kaprov1alpha1.ExecutionModeSpokePull),
	}

	if err := createOrUpdateObject(ctx, c, desired, true); err != nil {
		t.Fatalf("createOrUpdateObject dry-run patch: %v", err)
	}
	if !c.patchDryRun {
		t.Fatal("expected patch to receive client.DryRunAll")
	}
	var got kaprov1alpha1.Substrate
	if err := c.Get(ctx, client.ObjectKey{Name: "flux"}, &got); err != nil {
		t.Fatalf("get dry-run patch object: %v", err)
	}
	if got.Spec.Execution != nil {
		t.Fatalf("dry-run patch persisted spec=%#v", got.Spec)
	}
}

func testSubstrateSpec(kind string, mode kaprov1alpha1.ExecutionMode) kaprov1alpha1.SubstrateSpec {
	apiVersion, configKind := substrateConfigKind(kind)
	return kaprov1alpha1.SubstrateSpec{
		ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: kind},
		ConfigRef: &kaprov1alpha1.SubstrateObjectReference{APIVersion: apiVersion, Kind: configKind, Name: kind},
		Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: mode},
	}
}

type recordingAdoptClient struct {
	client.Client
	createDryRun bool
	patchDryRun  bool
}

func (c *recordingAdoptClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	createOpts := &client.CreateOptions{}
	createOpts.ApplyOptions(opts)
	c.createDryRun = len(createOpts.DryRun) > 0
	if c.createDryRun {
		current := obj.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), current); err == nil {
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "objects"}, obj.GetName())
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *recordingAdoptClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	patchOpts := &client.PatchOptions{}
	patchOpts.ApplyOptions(opts)
	c.patchDryRun = len(patchOpts.DryRun) > 0
	if c.patchDryRun {
		return nil
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func fakeAdoptClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := argocdsubstratev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add argo scheme: %v", err)
	}
	if err := fluxsubstratev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add flux scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha1.Substrate{}, &kaprov1alpha1.SubstrateDiscoveryPolicy{}, &argocdsubstratev1alpha1.ArgoCDSubstrateConfig{}, &fluxsubstratev1alpha1.FluxSubstrateConfig{}).
		Build()
}

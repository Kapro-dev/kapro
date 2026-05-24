package substrate_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	argocdv1alpha1 "kapro.io/kapro/api/substrate/argocd/v1alpha1"
	fluxv1alpha1 "kapro.io/kapro/api/substrate/flux/v1alpha1"
	kubernetesv1alpha1 "kapro.io/kapro/api/substrate/kubernetes/v1alpha1"
	ociv1alpha1 "kapro.io/kapro/api/substrate/oci/v1alpha1"
	webhookv1alpha1 "kapro.io/kapro/api/substrate/webhook/v1alpha1"
)

func TestReferenceSubstrateConfigSchemesRegister(t *testing.T) {
	scheme := runtime.NewScheme()
	adders := []func(*runtime.Scheme) error{
		argocdv1alpha1.AddToScheme,
		fluxv1alpha1.AddToScheme,
		kubernetesv1alpha1.AddToScheme,
		ociv1alpha1.AddToScheme,
		webhookv1alpha1.AddToScheme,
	}
	for _, add := range adders {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	want := []struct {
		groupVersion string
		kind         string
	}{
		{argocdv1alpha1.GroupVersion.String(), "ArgoCDSubstrateConfig"},
		{fluxv1alpha1.GroupVersion.String(), "FluxSubstrateConfig"},
		{kubernetesv1alpha1.GroupVersion.String(), "KubernetesApplyConfig"},
		{ociv1alpha1.GroupVersion.String(), "OCIBundleApplyConfig"},
		{webhookv1alpha1.GroupVersion.String(), "WebhookSubstrateConfig"},
	}
	for _, item := range want {
		gv, err := schema.ParseGroupVersion(item.groupVersion)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scheme.New(gv.WithKind(item.kind)); err != nil {
			t.Fatalf("%s/%s not registered: %v", item.groupVersion, item.kind, err)
		}
	}
}

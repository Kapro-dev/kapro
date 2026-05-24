package admission_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

func newSubstrateRefScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func substrateProfile(name string, driver kaprov1alpha1.SubstrateKind, ready bool) *kaprov1alpha1.Substrate {
	p := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.SubstrateSpec{
			Substrate: &kaprov1alpha1.SubstrateImplementationSpec{Kind: string(driver), Actuator: string(driver)},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeSpokePull},
		},
		Status: kaprov1alpha1.SubstrateStatus{Ready: ready},
	}
	if driver == kaprov1alpha1.SubstrateKindExternal {
		p.Spec.PluginRef = "external-plugin"
	}
	return p
}

func classRefSubstrateProfile(name, className string, ready bool) *kaprov1alpha1.Substrate {
	configRef := kaprov1alpha1.SubstrateObjectReference{
		APIVersion: "example.com/v1alpha1",
		Kind:       "ExampleConfig",
		Name:       name,
	}
	switch className {
	case "kubernetes-apply":
		configRef.APIVersion = "kubernetes.substrate.kapro.io/v1alpha1"
		configRef.Kind = "KubernetesApplyConfig"
	case "argo":
		configRef.APIVersion = "argocd.substrate.kapro.io/v1alpha1"
		configRef.Kind = "ArgoCDSubstrateConfig"
	case "flux":
		configRef.APIVersion = "flux.substrate.kapro.io/v1alpha1"
		configRef.Kind = "FluxSubstrateConfig"
	}
	return &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: className},
			ConfigRef: &configRef,
		},
		Status: kaprov1alpha1.SubstrateStatus{Ready: ready},
	}
}

func fleetClusterWithSubstrate(ref string) *kaprov1alpha1.Cluster {
	return &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.ClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{
				Mode:         kaprov1alpha1.DeliveryModePull,
				SubstrateRef: ref,
				Parameters:   map[string]string{"ociRepository": "cluster-a"},
			},
		},
	}
}

func TestValidateFleetClusterSubstrateRef_Missing(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	mc := fleetClusterWithSubstrate("ghost")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("missing Substrate should warn, not deny: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not found yet") {
		t.Fatalf("expected missing-substrate warning, got %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_ExistingSubstrateDoesNotRequireStatusReady(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(substrateProfile("flux", kaprov1alpha1.SubstrateKindFlux, false)).
		Build()
	mc := fleetClusterWithSubstrate("flux")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("unexpected error for built-in Substrate without status Ready: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_FluxParametersWarnAfterResolution(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(substrateProfile("team-flux", kaprov1alpha1.SubstrateKindFlux, true)).
		Build()
	mc := fleetClusterWithSubstrate("team-flux")
	mc.Spec.Delivery.Parameters = nil
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("missing flux parameters should warn, not deny: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ociRepository") {
		t.Fatalf("expected flux parameter warning, got %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_NotReadyClassRefAllowed(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	for _, tc := range []struct {
		name      string
		className string
	}{
		{name: "direct", className: "kubernetes-apply"},
		{name: "argo", className: "argo"},
		{name: "flux", className: "flux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(classRefSubstrateProfile(tc.name, tc.className, false)).
				Build()
			mc := fleetClusterWithSubstrate(tc.name)
			warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
			if err != nil {
				t.Fatalf("NotReady classRef Substrate should be admitted for controller reconciliation: %v", err)
			}
			if len(warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", warnings)
			}
		})
	}
}

func TestValidateFleetClusterSubstrateRef_ReadyClassRefAllowsCluster(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(classRefSubstrateProfile("direct", "kubernetes-apply", true)).
		Build()
	mc := fleetClusterWithSubstrate("direct")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("unexpected error for ready classRef Substrate: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_UnknownClassRefNotReadyAllowed(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(classRefSubstrateProfile("custom", "example-platform", false)).
		Build()
	mc := fleetClusterWithSubstrate("custom")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("NotReady custom classRef Substrate should be admitted for controller reconciliation: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_ExternalNotReadyAllowed(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(substrateProfile("external", kaprov1alpha1.SubstrateKindExternal, false)).
		Build()
	mc := fleetClusterWithSubstrate("external")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("NotReady external Substrate should be admitted for controller reconciliation: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_Ready(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(substrateProfile("flux", kaprov1alpha1.SubstrateKindFlux, true)).
		Build()
	mc := fleetClusterWithSubstrate("flux")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestValidateFleetClusterSubstrateRef_EmptyRefSkipped(t *testing.T) {
	scheme := newSubstrateRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	mc := fleetClusterWithSubstrate("")
	warnings, err := admission.ValidateFleetClusterSubstrateRef(context.Background(), reader, mc)
	if err != nil {
		t.Fatalf("empty ref should be deferred to syntactic validator: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

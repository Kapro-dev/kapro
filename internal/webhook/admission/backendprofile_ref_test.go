package admission_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/webhook/admission"
)

func newBackendRefScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func backendProfile(name string, driver kaprov1alpha2.BackendDriver, ready bool) *kaprov1alpha2.Backend {
	p := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.BackendSpec{
			Driver: driver,
		},
		Status: kaprov1alpha2.BackendStatus{Ready: ready},
	}
	if driver == kaprov1alpha2.BackendDriverExternal {
		p.Spec.PluginRef = "external-plugin"
	}
	return p
}

func fleetClusterWithBackend(ref string) *kaprov1alpha2.Cluster {
	return &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode:       kaprov1alpha2.DeliveryModePull,
				BackendRef: ref,
				Parameters: map[string]string{"ociRepository": "cluster-a"},
			},
		},
	}
}

func TestValidateFleetClusterBackendRef_Missing(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	mc := fleetClusterWithBackend("ghost")
	err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc)
	if err == nil {
		t.Fatal("expected error for missing Backend")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestValidateFleetClusterBackendRef_BuiltInBackendDoesNotRequireStatusReady(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(backendProfile("flux", kaprov1alpha2.BackendDriverFlux, false)).
		Build()
	mc := fleetClusterWithBackend("flux")
	if err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc); err != nil {
		t.Fatalf("unexpected error for built-in Backend without status Ready: %v", err)
	}
}

func TestValidateFleetClusterBackendRef_ExternalNotReady(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(backendProfile("external", kaprov1alpha2.BackendDriverExternal, false)).
		Build()
	mc := fleetClusterWithBackend("external")
	err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc)
	if err == nil {
		t.Fatal("expected error for NotReady external Backend")
	}
	if !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("expected not-Ready error, got %v", err)
	}
}

func TestValidateFleetClusterBackendRef_Ready(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(backendProfile("flux", kaprov1alpha2.BackendDriverFlux, true)).
		Build()
	mc := fleetClusterWithBackend("flux")
	if err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFleetClusterBackendRef_EmptyRefSkipped(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	mc := fleetClusterWithBackend("")
	if err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc); err != nil {
		t.Fatalf("empty ref should be deferred to syntactic validator: %v", err)
	}
}

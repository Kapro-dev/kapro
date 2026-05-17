package admission_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

func newBackendRefScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func backendProfile(name string, ready bool) *kaprov1alpha1.BackendProfile {
	p := &kaprov1alpha1.BackendProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.BackendProfileSpec{
			Driver: kaprov1alpha1.BackendDriverFlux,
		},
		Status: kaprov1alpha1.BackendProfileStatus{Ready: ready},
	}
	return p
}

func fleetClusterWithBackend(ref string) *kaprov1alpha1.FleetCluster {
	return &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{
				Mode:       kaprov1alpha1.DeliveryModePull,
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
		t.Fatal("expected error for missing BackendProfile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestValidateFleetClusterBackendRef_NotReady(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(backendProfile("flux", false)).
		Build()
	mc := fleetClusterWithBackend("flux")
	err := admission.ValidateFleetClusterBackendRef(context.Background(), reader, mc)
	if err == nil {
		t.Fatal("expected error for NotReady BackendProfile")
	}
	if !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("expected not-Ready error, got %v", err)
	}
}

func TestValidateFleetClusterBackendRef_Ready(t *testing.T) {
	scheme := newBackendRefScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(backendProfile("flux", true)).
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

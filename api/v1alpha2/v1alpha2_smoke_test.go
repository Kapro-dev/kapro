package v1alpha2

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// v1alpha2 is the post-rename API surface (ADR-0008 / PR 1).
// These tests are deliberately shallow — they only verify the new
// types round-trip through JSON / YAML and register with the scheme.
// Behavioural tests live alongside their controllers (added in
// Phase 2 of the migration).

func TestSchemeRegistersAllNewKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	// Every CRD's singular Kind we care about.
	wantKinds := []string{
		"Fleet", "Plan", "Source", "Trigger", "Target",
		"Backend", "Cluster", "ClusterTemplate", "Plugin", "Policy",
	}
	for _, kind := range wantKinds {
		gvk := GroupVersion.WithKind(kind)
		if _, err := scheme.New(gvk); err != nil {
			t.Errorf("Kind %q not registered in v1alpha2 scheme: %v", kind, err)
		}
	}
}

func TestFleetRoundTripsThroughYAML(t *testing.T) {
	in := &Fleet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha2", Kind: "Fleet"},
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: FleetSpec{
			SourceRef: "checkout-catalog",
			Delivery: DeliverySpec{
				Mode:       "pull",
				BackendRef: "flux",
			},
		},
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Fleet
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Name != "checkout" {
		t.Errorf("name lost across round-trip: %q", out.Name)
	}
	if out.Spec.Delivery.BackendRef != "flux" {
		t.Errorf("backendRef lost across round-trip: %q", out.Spec.Delivery.BackendRef)
	}
}

func TestPlanRoundTripsThroughYAML(t *testing.T) {
	in := &Plan{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha2", Kind: "Plan"},
		ObjectMeta: metav1.ObjectMeta{Name: "canary-then-prod"},
		Spec: PlanSpec{
			Stages: []Stage{
				{Name: "canary"},
				{Name: "prod"},
			},
		},
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Plan
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := len(out.Spec.Stages); got != 2 {
		t.Errorf("stages roundtrip: got %d, want 2", got)
	}
}

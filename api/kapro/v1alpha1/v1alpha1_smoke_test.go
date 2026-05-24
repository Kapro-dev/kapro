package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// v1alpha1 is the post-rename API surface (ADR-0008 / PR 1).
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
		"Approval", "Substrate", "Cluster", "ClusterTemplate",
		"Fleet", "Plan", "Plugin", "Policy",
		"Promotion", "Source", "SubstrateClass", "Trigger",
	}
	for _, kind := range wantKinds {
		gvk := GroupVersion.WithKind(kind)
		if _, err := scheme.New(gvk); err != nil {
			t.Errorf("Kind %q not registered in v1alpha1 scheme: %v", kind, err)
		}
	}
}

func TestFleetRoundTripsThroughYAML(t *testing.T) {
	in := &Fleet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Fleet"},
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: FleetSpec{
			SourceRef: "checkout-catalog",
			Delivery: DeliverySpec{
				Mode:         "pull",
				SubstrateRef: "flux",
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
	if out.Spec.Delivery.SubstrateRef != "flux" {
		t.Errorf("substrateRef lost across round-trip: %q", out.Spec.Delivery.SubstrateRef)
	}
}

func TestSubstrateClassConfigRefsRoundTripThroughYAML(t *testing.T) {
	in := &Substrate{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Substrate"},
		ObjectMeta: metav1.ObjectMeta{Name: "prod-argo"},
		Spec: SubstrateSpec{
			ClassRef: &SubstrateClassReference{Name: "argo"},
			ConfigRef: &SubstrateObjectReference{
				APIVersion: "argocd.substrate.kapro.io/v1alpha1",
				Kind:       "ArgoCDSubstrateConfig",
				Name:       "prod-argo",
			},
			Execution: &SubstrateExecutionSpec{Mode: ExecutionModeHubPush},
		},
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Substrate
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Spec.ClassRef == nil || out.Spec.ClassRef.Name != "argo" {
		t.Fatalf("classRef lost across round-trip: %#v", out.Spec.ClassRef)
	}
	if out.Spec.ConfigRef == nil || out.Spec.ConfigRef.Kind != "ArgoCDSubstrateConfig" {
		t.Fatalf("configRef lost across round-trip: %#v", out.Spec.ConfigRef)
	}
	if got := out.Spec.SubstrateKind(); got != "argo" {
		t.Fatalf("SubstrateKind() = %q, want argo", got)
	}
}

func TestDeliveryStagingRoundTripsThroughYAML(t *testing.T) {
	in := &Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec: ClusterSpec{
			Delivery: DeliverySpec{
				Mode:         DeliveryModePull,
				SubstrateRef: "oci",
				Staging: &DeliveryStagingSpec{
					Type:          DeliveryStagingTwoPhase,
					FailurePolicy: DeliveryStagingFailureAbort,
				},
			},
		},
		Status: ClusterStatus{
			Delivery: map[string]ClusterDeliveryStatus{
				"api": {
					Phase:          DeliveryPhaseFailed,
					DesiredVersion: "1.2.3",
					Staging: &DeliveryStagingStatus{
						Type:                 DeliveryStagingTwoPhase,
						FailurePolicy:        DeliveryStagingFailureAbort,
						StagedObjects:        4,
						StagingFailedObjects: 1,
						FailurePhase:         DeliveryPhaseStaging,
					},
				},
			},
		},
	}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Cluster
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Spec.Delivery.Staging == nil {
		t.Fatal("staging lost across round-trip")
	}
	if out.Spec.Delivery.Staging.Type != DeliveryStagingTwoPhase {
		t.Errorf("staging.type = %q, want %q", out.Spec.Delivery.Staging.Type, DeliveryStagingTwoPhase)
	}
	if out.Spec.Delivery.Staging.FailurePolicy != DeliveryStagingFailureAbort {
		t.Errorf("staging.failurePolicy = %q, want %q", out.Spec.Delivery.Staging.FailurePolicy, DeliveryStagingFailureAbort)
	}
	status := out.Status.Delivery["api"].Staging
	if status == nil {
		t.Fatal("status.delivery[api].staging lost across round-trip")
	}
	if status.StagedObjects != 4 || status.StagingFailedObjects != 1 {
		t.Fatalf("status staging counts = %+v, want staged=4 failed=1", status)
	}
	if status.FailurePhase != DeliveryPhaseStaging {
		t.Fatalf("status failurePhase = %q, want Staging", status.FailurePhase)
	}
}

func TestPlanRoundTripsThroughYAML(t *testing.T) {
	in := &Plan{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Plan"},
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

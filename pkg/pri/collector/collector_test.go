package collector

import (
	"context"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"
	"kapro.io/kapro/pkg/pri"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCollectPromotionRunEmitsValidPRIBundle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme kapro: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme runtime: %v", err)
	}

	run := &kaproruntimev1alpha1.PromotionRun{
		TypeMeta: metav1.TypeMeta{APIVersion: "runtime.kapro.io/v1alpha1", Kind: "PromotionRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-run",
			Labels: map[string]string{
				labelPromotion: "checkout-v1",
				labelUnit:      "checkout",
			},
		},
		Spec: kaprov1alpha1.PromotionRunSpec{
			DeliveryUnitRef: "checkout",
			Version:         "v1.2.3",
			Plans: []kaprov1alpha1.PlanRef{{
				Name: "global",
				Plan: "global",
			}},
		},
		Status: kaprov1alpha1.PromotionRunStatus{
			Phase:       kaprov1alpha1.PromotionRunPhaseComplete,
			StartedAt:   "2026-05-28T10:00:00Z",
			CompletedAt: "2026-05-28T10:05:00Z",
			AuditTrail: []kaprov1alpha1.AuditEntry{{
				Artifact:    "checkout:v1.2.3",
				CompletedAt: "2026-05-28T10:05:00Z",
			}},
		},
	}
	target := &kaproruntimev1alpha1.Target{
		TypeMeta: metav1.TypeMeta{APIVersion: "runtime.kapro.io/v1alpha1", Kind: "Target"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-run-prod",
		},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "checkout-run",
			Target:          "prod",
			Plan:            "global",
			Stage:           "prod",
			Version:         "v1.2.3",
		},
		Status: kaprov1alpha1.TargetStatus{
			TargetExecutionState: kaprov1alpha1.TargetExecutionState{
				PromotionRunRef: "checkout-run",
				Target:          "prod",
				Plan:            "global",
				Stage:           "prod",
				Phase:           kaprov1alpha1.TargetPhaseConverged,
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, target).Build()
	bundle, err := New(client).CollectPromotionRun(context.Background(), "checkout-run")
	if err != nil {
		t.Fatalf("CollectPromotionRun: %v", err)
	}
	if bundle.Promotion == nil {
		t.Fatal("Promotion is nil")
	}
	if bundle.PromotionRun == nil {
		t.Fatal("PromotionRun is nil")
	}
	if len(bundle.Evidence) != 1 {
		t.Fatalf("evidence len = %d, want 1", len(bundle.Evidence))
	}
	for _, doc := range bundle.Documents() {
		if err := pri.Validate(doc); err != nil {
			t.Fatalf("Validate(%T): %v", doc, err)
		}
	}
	if got := bundle.PromotionRun.Status.Phase; got != "Succeeded" {
		t.Fatalf("phase = %q, want Succeeded", got)
	}
	if got := bundle.PromotionRun.Status.TargetResults[0].Phase; got != "Succeeded" {
		t.Fatalf("target phase = %q, want Succeeded", got)
	}
	if got := bundle.PromotionRun.Status.TargetResults[0].ImplementationPhase; got != "Converged" {
		t.Fatalf("implementation phase = %q, want Converged", got)
	}
}

func TestReferenceProfileDocumentsValidate(t *testing.T) {
	for _, doc := range []any{ReferenceBinding(), ReferenceConformanceProfile()} {
		if err := pri.Validate(doc); err != nil {
			t.Fatalf("Validate(%T): %v", doc, err)
		}
	}
}

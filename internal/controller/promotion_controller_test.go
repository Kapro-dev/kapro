package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestPromotionReconcilerCreatesNewRunWhenImmutableSpecChanges(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	oldSpec := promotionRunSpecFixture("v1", false)
	newSpec := promotionRunSpecFixture("v2", false)
	existing := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: promotionRunName(promotion, oldSpec)}, Spec: oldSpec}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, existing).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var run kaprov1alpha1.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: promotionRunName(promotion, newSpec)}, &run); err != nil {
		t.Fatalf("expected new PromotionRun for changed immutable spec: %v", err)
	}
	if run.Spec.Version != "v2" {
		t.Fatalf("run version = %q", run.Spec.Version)
	}
}

func TestPromotionReconcilerPatchesSameRunForMutableSpecChanges(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(2, nil)
	promotion.Spec.Suspended = true
	oldSpec := promotionRunSpecFixture("v2", false)
	existing := &kaprov1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: promotionRunName(promotion, oldSpec)}, Spec: oldSpec}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion, existing).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	if !runs.Items[0].Spec.Suspended {
		t.Fatalf("same PromotionRun was not patched with suspended=true")
	}
}

func TestPromotionReconcilerFailsClosedForPolicies(t *testing.T) {
	ctx := context.Background()
	scheme := newPromotionTestScheme(t)
	promotion := promotionFixture(1, []corev1.LocalObjectReference{{Name: "signed-artifacts"}})
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{}).
		WithObjects(promotion).
		Build()
	r := &PromotionReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("promotionrun count = %d", len(runs.Items))
	}
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != kaprov1alpha1.PromotionPhaseFailed || cond == nil || cond.Reason != "PromotionPolicyUnsupported" {
		t.Fatalf("status phase=%q condition=%+v", got.Status.Phase, cond)
	}
}

func promotionFixture(generation int64, policies []corev1.LocalObjectReference) *kaprov1alpha1.Promotion {
	return &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Generation: generation},
		Spec: kaprov1alpha1.PromotionSpec{
			Version: "v2",
			PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{
				Name:          "staging",
				PromotionPlan: "default",
			}},
			Policies: policies,
		},
	}
}

func promotionRunSpecFixture(version string, suspended bool) kaprov1alpha1.PromotionRunSpec {
	return kaprov1alpha1.PromotionRunSpec{
		Version: version,
		PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{
			Name:          "staging",
			PromotionPlan: "default",
		}},
		Suspended: suspended,
	}
}

func newPromotionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

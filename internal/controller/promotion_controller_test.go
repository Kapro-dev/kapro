package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func newPromotionReconciler(t *testing.T, objects ...client.Object) (*PromotionReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(
			&kaprov1alpha1.Promotion{},
			&kaprov1alpha1.PromotionRun{},
		).
		Build()
	return &PromotionReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
	}, c
}

func newKapro(name string) *kaprov1alpha1.Kapro {
	return &kaprov1alpha1.Kapro{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.KaproSpec{
			SourceRef: "shared-catalog",
			Delivery:  kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
			Clusters: []kaprov1alpha1.KaproCluster{
				{Name: "c1", Labels: map[string]string{"stage": "prod"}},
			},
			PromotionPlan: kaprov1alpha1.KaproPromotionPlan{
				Stages: []kaprov1alpha1.KaproStage{
					{Name: "prod", Selector: map[string]string{"stage": "prod"}},
				},
			},
		},
	}
}

func newPromotion(name, kaproRef, version string) *kaprov1alpha1.Promotion {
	return &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: kaprov1alpha1.PromotionSpec{
			KaproRef: kaproRef,
			Version:  version,
		},
	}
}

func TestPromotionMissingKaproIsPending(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-v1", "checkout", "v1.2.3")
	r, c := newPromotionReconciler(t, p)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != kaprov1alpha1.PromotionPhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
	// No PromotionRun should be created.
	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("len(runs) = %d, want 0", len(runs.Items))
	}
}

func TestPromotionStampsFirstAttempt(t *testing.T) {
	ctx := context.Background()
	r, c := newPromotionReconciler(t, newKapro("checkout"), newPromotion("checkout-v1", "checkout", "v1.2.3"))
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout-v1"}}); err != nil {
		t.Fatal(err)
	}
	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs.Items))
	}
	if got := runs.Items[0].Labels[promotionOwnerLabel]; got != "checkout-v1" {
		t.Fatalf("owner label = %q, want checkout-v1", got)
	}
	if runs.Items[0].Spec.Version != "v1.2.3" {
		t.Fatalf("run version = %q, want v1.2.3", runs.Items[0].Spec.Version)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-v1"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ActiveAttemptRef == nil {
		t.Fatal("ActiveAttemptRef = nil, want non-nil")
	}
	if got.Status.ActiveAttemptRef.Name != runs.Items[0].Name {
		t.Fatalf("ActiveAttemptRef.Name = %q, want %q", got.Status.ActiveAttemptRef.Name, runs.Items[0].Name)
	}
	if len(got.Status.Attempts) != 1 {
		t.Fatalf("len(attempts) = %d, want 1", len(got.Status.Attempts))
	}
}

func TestPromotionSpecChangeStampsNewAttemptAndSupersedes(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-rolling", "checkout", "v1.2.3")
	r, c := newPromotionReconciler(t, newKapro("checkout"), p)

	// First reconcile: stamp v1.2.3.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	// Mutate Promotion spec to v1.2.4 and bump generation.
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Version = "v1.2.4"
	got.Generation = 2
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}

	// Second reconcile: should stamp a new attempt and supersede the first.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("len(runs) = %d, want 2 (one superseded, one active)", len(runs.Items))
	}

	var superseded, active *kaprov1alpha1.PromotionRun
	for i := range runs.Items {
		switch runs.Items[i].Spec.Version {
		case "v1.2.3":
			superseded = &runs.Items[i]
		case "v1.2.4":
			active = &runs.Items[i]
		}
	}
	if superseded == nil {
		t.Fatal("did not find v1.2.3 run")
	}
	if active == nil {
		t.Fatal("did not find v1.2.4 run")
	}
	if superseded.Status.Phase != kaprov1alpha1.PromotionRunPhaseSuperseded {
		t.Fatalf("v1.2.3 phase = %q, want Superseded", superseded.Status.Phase)
	}
	if active.Status.Phase == kaprov1alpha1.PromotionRunPhaseSuperseded {
		t.Fatal("v1.2.4 must not be Superseded")
	}

	// Promotion.status.attempts should record both, newest first.
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Attempts) != 2 {
		t.Fatalf("len(attempts) = %d, want 2", len(got.Status.Attempts))
	}
	if got.Status.Attempts[0].Version != "v1.2.4" {
		t.Fatalf("attempts[0].Version = %q, want v1.2.4 (newest first)", got.Status.Attempts[0].Version)
	}
}

func TestPromotionSuspendedSuspendsActiveRuns(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-v1", "checkout", "v1.2.3")
	r, c := newPromotionReconciler(t, newKapro("checkout"), p)

	// Stamp v1.2.3.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	// Suspend the promotion.
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Suspended = true
	got.Generation = 2
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs.Items))
	}
	if !runs.Items[0].Spec.Suspended {
		t.Fatal("expected owned PromotionRun.spec.suspended = true after Promotion suspend")
	}
}

func TestPromotionSpecHashStableAndDriftDetectable(t *testing.T) {
	a := kaprov1alpha1.PromotionSpec{KaproRef: "checkout", Version: "v1"}
	b := kaprov1alpha1.PromotionSpec{KaproRef: "checkout", Version: "v1"}
	c := kaprov1alpha1.PromotionSpec{KaproRef: "checkout", Version: "v2"}

	if promotionSpecHash(&a) != promotionSpecHash(&b) {
		t.Fatal("identical specs should hash equal")
	}
	if promotionSpecHash(&a) == promotionSpecHash(&c) {
		t.Fatal("version change must produce different hash")
	}
}

func TestPromotionAttemptsCappedAt20(t *testing.T) {
	var list []kaprov1alpha1.PromotionAttemptRef
	for i := 0; i < 30; i++ {
		entry := kaprov1alpha1.PromotionAttemptRef{
			Name:     "run-" + string(rune('A'+i)),
			SpecHash: "h" + string(rune('A'+i)),
		}
		list = upsertAttempt(list, entry)
	}
	if len(list) != kaprov1alpha1.MaxPromotionAttempts {
		t.Fatalf("len = %d, want %d", len(list), kaprov1alpha1.MaxPromotionAttempts)
	}
	// Newest first; the last entry inserted (i=29) must be at index 0.
	if list[0].Name != "run-"+string(rune('A'+29)) {
		t.Fatalf("list[0].Name = %q, want newest", list[0].Name)
	}
}

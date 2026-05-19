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

// TestPromotionSuspendedAtCreationStampsSuspendedRun covers Bug A: a
// Promotion created already-suspended must produce a PromotionRun that is
// suspended at t=0. The prior implementation only suspended runs on a later
// reconcile cycle, leaving a window where the runtime FSM could advance.
func TestPromotionSuspendedAtCreationStampsSuspendedRun(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-v1", "checkout", "v1.2.3")
	p.Spec.Suspended = true
	r, c := newPromotionReconciler(t, newKapro("checkout"), p)

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	// Promotion is suspended → the suspended branch never stamps a new
	// PromotionRun. That is correct: spec.suspended at creation pauses the
	// whole lifecycle. We assert phase=Paused and no run exists.
	if len(runs.Items) != 0 {
		t.Fatalf("expected no PromotionRun while spec.suspended=true at creation, got %d", len(runs.Items))
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != kaprov1alpha1.PromotionPhasePaused {
		t.Fatalf("phase = %q, want Paused", got.Status.Phase)
	}

	// Unsuspend and reconcile: the freshly-stamped run must carry
	// spec.suspended=false (i.e., the value propagated from the current
	// Promotion.spec.suspended, not the previous one).
	got.Spec.Suspended = false
	got.Generation = 2
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected one PromotionRun after unsuspend, got %d", len(runs.Items))
	}
	if runs.Items[0].Spec.Suspended {
		t.Fatal("freshly stamped PromotionRun must mirror current Promotion.spec.suspended=false")
	}

	// Re-suspend before reconciliation and confirm the suspend bit flows
	// through to the run (verifies Bug A propagation path, not just
	// post-hoc patching). This is the inverse case: a *new* attempt while
	// spec.suspended=true must produce a suspended run at t=0.
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Suspended = true
	got.Spec.Version = "v1.2.4"
	got.Generation = 3
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	if err := c.List(ctx, &runs); err != nil {
		t.Fatal(err)
	}
	for _, run := range runs.Items {
		if !run.Spec.Suspended {
			t.Fatalf("PromotionRun %s spec.suspended = false, want true while parent Promotion is suspended", run.Name)
		}
	}
}

// TestPromotionPhaseLifecycle walks the Docker-style lifecycle:
// Pending → Progressing → Succeeded → Restarting (on respec) → Progressing.
func TestPromotionPhaseLifecycle(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-lc", "checkout", "v1.2.3")
	r, c := newPromotionReconciler(t, newKapro("checkout"), p)

	// 1. First reconcile stamps attempt #1; run starts in Pending phase
	//    (no prior terminal attempts → Promotion.phase=Pending).
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhasePending)

	// 2. Run advances to Progressing → Promotion mirrors it.
	advanceRun(t, ctx, c, p.Name, kaprov1alpha1.PromotionRunPhaseProgressing, "")
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhaseProgressing)

	// 3. Run completes → Promotion → Succeeded; Ready=True.
	advanceRun(t, ctx, c, p.Name, kaprov1alpha1.PromotionRunPhaseComplete, "2024-01-01T00:00:00Z")
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhaseSucceeded)
	assertCondition(t, c, p.Name, "Ready", metav1.ConditionTrue)
	assertCondition(t, c, p.Name, "RollbackAvailable", metav1.ConditionTrue)

	// 4. New spec → controller stamps attempt #2 while a prior terminal
	//    exists → Promotion → Restarting (run created in Pending).
	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Version = "v1.2.4"
	got.Generation = 2
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhaseRestarting)

	// 5. New run progresses → Promotion → Progressing.
	advanceRun(t, ctx, c, p.Name, kaprov1alpha1.PromotionRunPhaseProgressing, "")
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhaseProgressing)

	// 6. New run fails → Promotion → Failed; Ready=False.
	advanceRun(t, ctx, c, p.Name, kaprov1alpha1.PromotionRunPhaseFailed, "2024-01-02T00:00:00Z")
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionPhase(t, c, p.Name, kaprov1alpha1.PromotionPhaseFailed)
	assertCondition(t, c, p.Name, "Ready", metav1.ConditionFalse)
}

// TestPromotionDeletionTransitionsToTerminating verifies the Docker
// "removing" analogue: a Promotion with deletionTimestamp set publishes
// phase=Terminating so observers see the transition before GC completes.
func TestPromotionDeletionTransitionsToTerminating(t *testing.T) {
	ctx := context.Background()
	p := newPromotion("checkout-del", "checkout", "v1.2.3")
	// Add a finalizer so the fake client persists the object after Delete
	// (without one, Delete removes immediately and we can't reconcile).
	p.Finalizers = []string{"kapro.io/test"}
	r, c := newPromotionReconciler(t, newKapro("checkout"), p)

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: p.Name}}); err != nil {
		t.Fatal(err)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != kaprov1alpha1.PromotionPhaseTerminating {
		t.Fatalf("phase = %q, want Terminating", got.Status.Phase)
	}
	if got.Status.ActiveAttemptRef != nil {
		t.Fatal("ActiveAttemptRef must be nil during Terminating")
	}
}

// advanceRun patches the (single) PromotionRun owned by the named Promotion
// to the given phase and optional CompletedAt timestamp.
func advanceRun(t *testing.T, ctx context.Context, c client.Client, promotionName string,
	phase kaprov1alpha1.PromotionRunPhase, completedAt string) {
	t.Helper()
	var runs kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &runs, client.MatchingLabels{promotionOwnerLabel: promotionName}); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) == 0 {
		t.Fatalf("no PromotionRun owned by %q", promotionName)
	}
	// Newest first.
	newest := &runs.Items[0]
	for i := range runs.Items {
		if runs.Items[i].CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = &runs.Items[i]
		}
	}
	patch := client.MergeFrom(newest.DeepCopy())
	newest.Status.Phase = phase
	if completedAt != "" {
		newest.Status.CompletedAt = completedAt
	}
	if err := c.Status().Patch(ctx, newest, patch); err != nil {
		t.Fatal(err)
	}
}

func assertPromotionPhase(t *testing.T, c client.Client, name string, want kaprov1alpha1.PromotionPhase) {
	t.Helper()
	var got kaprov1alpha1.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != want {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, want)
	}
}

func assertCondition(t *testing.T, c client.Client, name, condType string, want metav1.ConditionStatus) {
	t.Helper()
	var got kaprov1alpha1.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	for _, cond := range got.Status.Conditions {
		if cond.Type == condType {
			if cond.Status != want {
				t.Fatalf("condition %q status = %q, want %q", condType, cond.Status, want)
			}
			return
		}
	}
	t.Fatalf("condition %q not found on Promotion %s", condType, name)
}

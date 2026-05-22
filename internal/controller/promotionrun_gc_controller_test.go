package controller_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/controller"
)

// fixture helper — builds a PromotionRun child of a Promotion at a given age.
func mkRun(name, parent string, phase kaprov1alpha2.PromotionRunPhase, ageMinutes int) *kaprov1alpha2.PromotionRun {
	return &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Duration(ageMinutes) * time.Minute)),
			Labels: map[string]string{
				"kapro.io/promotion": parent,
			},
		},
		Status: kaprov1alpha2.PromotionRunStatus{Phase: phase},
	}
}

func gcTestClient(t *testing.T, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(), s
}

// TestGC_BelowCap_NothingDeleted asserts the controller is a no-op when the
// total retained count is at or under the cap.
func TestGC_BelowCap_NothingDeleted(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	runs := []client.Object{
		mkRun("rel-1", "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 60),
		mkRun("rel-2", "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 50),
		mkRun("rel-3", "checkout", kaprov1alpha2.PromotionRunPhaseFailed, 40),
	}
	c, s := gcTestClient(t, append(runs, promo)...)
	r := &controller.PromotionRunGCReconciler{
		Client:                  c,
		Recorder:                record.NewFakeRecorder(200),
		Scheme:                  s,
		MaxRetainedPerPromotion: 50,
		MinRetainedPerOutcome:   10,
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)
	if len(remaining.Items) != 3 {
		t.Fatalf("expected 3 PromotionRuns retained, got %d", len(remaining.Items))
	}
}

// TestGC_ActiveNeverDeleted asserts non-terminal PromotionRuns survive even
// when the total exceeds the cap.
func TestGC_ActiveNeverDeleted(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}

	// 5 cap, 1 floor → very tight to force pressure.
	// 3 active (always survive) + 5 terminal Complete = 8 total. Budget = 5-3=2 terminal.
	// Should delete 3 oldest Complete, keep 2 newest. Active untouched.
	var objs []client.Object
	objs = append(objs, promo)
	for i := 1; i <= 3; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("active-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseProgressing, 30-i))
	}
	for i := 1; i <= 5; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("done-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 100-i*5))
	}

	c, s := gcTestClient(t, objs...)
	r := &controller.PromotionRunGCReconciler{
		Client:                  c,
		Recorder:                record.NewFakeRecorder(200),
		Scheme:                  s,
		MaxRetainedPerPromotion: 5,
		MinRetainedPerOutcome:   1,
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)
	if len(remaining.Items) != 5 {
		t.Fatalf("expected 5 retained (3 active + 2 newest Complete), got %d", len(remaining.Items))
	}
	activeCount := 0
	for _, run := range remaining.Items {
		if !run.Status.Phase.IsTerminal() {
			activeCount++
		}
	}
	if activeCount != 3 {
		t.Fatalf("active count drifted: got %d, want 3", activeCount)
	}
}

// TestGC_PerOutcomeFloorRespected asserts each terminal outcome keeps at
// least MinRetainedPerOutcome — old Failures are not auto-pruned just
// because Successes filled the cap.
func TestGC_PerOutcomeFloorRespected(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	var objs []client.Object
	objs = append(objs, promo)
	// 20 Successes (recent), 5 Failed (much older), 0 active.
	// Cap=10, floor-per-outcome=3.
	// Expected: keep 3 Failed (floor) + 7 Successes (recent) = 10.
	for i := 1; i <= 20; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("succ-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 100-i))
	}
	for i := 1; i <= 5; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("fail-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseFailed, 500-i))
	}
	c, s := gcTestClient(t, objs...)
	r := &controller.PromotionRunGCReconciler{
		Client:                  c,
		Recorder:                record.NewFakeRecorder(200),
		Scheme:                  s,
		MaxRetainedPerPromotion: 10,
		MinRetainedPerOutcome:   3,
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)
	if len(remaining.Items) != 10 {
		t.Fatalf("expected 10 retained, got %d", len(remaining.Items))
	}
	var failed, succ int
	for _, run := range remaining.Items {
		switch run.Status.Phase {
		case kaprov1alpha2.PromotionRunPhaseFailed:
			failed++
		case kaprov1alpha2.PromotionRunPhaseComplete:
			succ++
		}
	}
	if failed < 3 {
		t.Fatalf("Failed floor violated: got %d, want >= 3", failed)
	}
	if succ != 10-failed {
		t.Fatalf("Complete count: got %d, want %d", succ, 10-failed)
	}
}

// TestGC_MissingPromotionNoOp asserts the controller is a no-op when the
// parent Promotion has already been deleted (K8s GC handles the cascade).
func TestGC_MissingPromotionNoOp(t *testing.T) {
	c, s := gcTestClient(t)
	r := &controller.PromotionRunGCReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(200),
		Scheme:   s,
	}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: "missing"}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for missing Promotion, got %+v", res)
	}
}

// TestGC_GlobalOldestFirst asserts above-floor victims across buckets are
// selected by global oldest-first ordering. Two terminal phases each have
// excess-above-floor candidates; the oldest entries — wherever they live —
// must be picked first regardless of map iteration order.
func TestGC_GlobalOldestFirst(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	var objs []client.Object
	objs = append(objs, promo)

	// 4 Complete (ages 10,20,30,40m) and 4 Failed (ages 5,15,25,35m).
	// Cap=4, floor-per-outcome=1 → must delete 4 (one excess per bucket
	// after floor for Complete: 3, for Failed: 3; budget=4-0=4; excess=4).
	// Globally oldest 4: complete-40, failed-35, complete-30, failed-25.
	for i, age := range []int{10, 20, 30, 40} {
		objs = append(objs, mkRun(fmt.Sprintf("complete-%d", age), "checkout", kaprov1alpha2.PromotionRunPhaseComplete, age))
		_ = i
	}
	for _, age := range []int{5, 15, 25, 35} {
		objs = append(objs, mkRun(fmt.Sprintf("failed-%d", age), "checkout", kaprov1alpha2.PromotionRunPhaseFailed, age))
	}

	c, s := gcTestClient(t, objs...)
	r := &controller.PromotionRunGCReconciler{
		Client:                  c,
		Recorder:                record.NewFakeRecorder(200),
		Scheme:                  s,
		MaxRetainedPerPromotion: 4,
		MinRetainedPerOutcome:   1,
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)

	survivors := map[string]bool{}
	for _, run := range remaining.Items {
		survivors[run.Name] = true
	}
	wantSurvive := []string{"complete-10", "complete-20", "failed-5", "failed-15"}
	wantDelete := []string{"complete-30", "complete-40", "failed-25", "failed-35"}
	for _, n := range wantSurvive {
		if !survivors[n] {
			t.Errorf("expected %s to survive, got deleted", n)
		}
	}
	for _, n := range wantDelete {
		if survivors[n] {
			t.Errorf("expected %s to be deleted, got retained", n)
		}
	}
}

// TestGC_BoundedDeletesPerReconcile asserts a large backlog drains over
// multiple reconciles instead of one mega-pass that could saturate the API
// server or event broadcaster.
func TestGC_BoundedDeletesPerReconcile(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	var objs []client.Object
	objs = append(objs, promo)
	// 30 terminal Completes, cap=5 → 25 to delete. With per-reconcile
	// cap=10 the first reconcile deletes 10 and requeues; we don't drain
	// the rest here, we just assert the cap held + RequeueAfter set.
	for i := 1; i <= 30; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("succ-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 100-i))
	}
	c, s := gcTestClient(t, objs...)
	r := &controller.PromotionRunGCReconciler{
		Client:                  c,
		Recorder:                record.NewFakeRecorder(200),
		Scheme:                  s,
		MaxRetainedPerPromotion: 5,
		MinRetainedPerOutcome:   1,
		MaxDeletesPerReconcile:  10,
		RequeueAfter:            time.Second,
	}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != time.Second {
		t.Fatalf("expected RequeueAfter=1s to drain remaining backlog, got %+v", res)
	}
	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)
	// Started at 30, deleted exactly 10 this pass → 20 remain.
	if len(remaining.Items) != 20 {
		t.Fatalf("expected per-reconcile cap to bound deletes to 10 (20 remaining); got %d remaining", len(remaining.Items))
	}
}

// TestGC_DefaultsApplied asserts zero overrides resolve to the
// kaprov1alpha2.Default* constants.
func TestGC_DefaultsApplied(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	var objs []client.Object
	objs = append(objs, promo)
	// At default cap=50 and floor=10, 30 Completes should all survive.
	for i := 1; i <= 30; i++ {
		objs = append(objs, mkRun(fmt.Sprintf("succ-%d", i), "checkout", kaprov1alpha2.PromotionRunPhaseComplete, 100-i))
	}
	c, s := gcTestClient(t, objs...)
	r := &controller.PromotionRunGCReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(200),
		Scheme:   s,
		// MaxRetainedPerPromotion and MinRetainedPerOutcome left zero — defaults apply.
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(promo)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var remaining kaprov1alpha2.PromotionRunList
	_ = c.List(context.Background(), &remaining)
	if len(remaining.Items) != 30 {
		t.Fatalf("expected 30 retained (under default cap of %d), got %d",
			kaprov1alpha2.DefaultMaxRetainedPerPromotion, len(remaining.Items))
	}
}

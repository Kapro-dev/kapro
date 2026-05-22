package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestGateExpressionReconcilerAllPassed(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhasePassed),
		gateExpressionWithStatus("b", gateExpressionPhasePassed),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "all"},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "ALL",
				Operands: []kaprov1alpha2.GateExpressionOperand{
					{ExpressionRef: "a"},
					{ExpressionRef: "b"},
				},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "all"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "all")
	if got.Status.Phase != gateExpressionPhasePassed {
		t.Fatalf("phase = %q, want Passed", got.Status.Phase)
	}
	if got.Status.Reason != "AllOperandsPassed" {
		t.Fatalf("reason = %q", got.Status.Reason)
	}
}

func TestGateExpressionReconcilerOneFailed(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhasePassed),
		gateExpressionWithStatus("b", gateExpressionPhaseFailed),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "all"},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "ALL",
				Operands: []kaprov1alpha2.GateExpressionOperand{
					{ExpressionRef: "a"},
					{ExpressionRef: "b"},
				},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "all"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "all")
	if got.Status.Phase != gateExpressionPhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
}

func TestGateExpressionReconcilerOnePending(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhasePassed),
		gateExpressionWithStatus("b", gateExpressionPhasePending),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "all"},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "ALL",
				Operands: []kaprov1alpha2.GateExpressionOperand{
					{ExpressionRef: "a"},
					{ExpressionRef: "b"},
				},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "all"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "all")
	if got.Status.Phase != gateExpressionPhasePending {
		t.Fatalf("phase = %q, want Pending", got.Status.Phase)
	}
}

func TestGateExpressionReconcilerIgnoresStaleChildStatus(t *testing.T) {
	ctx := context.Background()
	child := gateExpressionWithStatus("child", gateExpressionPhasePassed)
	child.Generation = 2
	child.Status.ObservedGeneration = 1
	parent := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "parent", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ALL",
			Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
		},
	}
	c := gateExpressionClient(t, child, parent)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "parent"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "parent")
	if got.Status.Phase != gateExpressionPhasePending {
		t.Fatalf("phase = %q, want Pending because child status is stale", got.Status.Phase)
	}
}

func TestGateExpressionReconcilerMissingReferenceFailsDomainStatus(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t, &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "all", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ALL",
			Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "missing"}},
		},
	})
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "all"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "all")
	if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "MissingReference" {
		t.Fatalf("status = %s/%s, want Failed/MissingReference", got.Status.Phase, got.Status.Reason)
	}
}

func TestGateExpressionReconcilerDetectsCycleWithChildStatus(t *testing.T) {
	ctx := context.Background()
	child := gateExpressionWithStatus("b", gateExpressionPhasePassed)
	child.Spec.Operands = []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "a"}}
	parent := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ALL",
			Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "b"}},
		},
	}
	c := gateExpressionClient(t, child, parent)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "a")
	if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "CycleDetected" {
		t.Fatalf("status = %s/%s, want Failed/CycleDetected", got.Status.Phase, got.Status.Reason)
	}
}

func TestGateExpressionReconcilerAnyPassed(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhasePending),
		gateExpressionWithStatus("b", gateExpressionPhasePassed),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "any", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "ANY",
				Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "a"}, {ExpressionRef: "b"}},
			},
		})
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "any"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "any")
	if got.Status.Phase != gateExpressionPhasePassed {
		t.Fatalf("phase = %q, want Passed", got.Status.Phase)
	}
}

func TestGateExpressionReconcilerNotInvertsFailed(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("child", gateExpressionPhaseFailed),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "not", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "NOT",
				Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "not"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "not")
	if got.Status.Phase != gateExpressionPhasePassed || got.Status.Reason != "NotOperandFailed" {
		t.Fatalf("status = %s/%s, want Passed/NotOperandFailed", got.Status.Phase, got.Status.Reason)
	}
}

func TestGateExpressionReconcilerThresholdEarlyFailed(t *testing.T) {
	threshold := int32(2)
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhaseFailed),
		gateExpressionWithStatus("b", gateExpressionPhaseFailed),
		gateExpressionWithStatus("c", gateExpressionPhasePending),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "threshold", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator:  "THRESHOLD",
				Threshold: &threshold,
				Operands:  []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "a"}, {ExpressionRef: "b"}, {ExpressionRef: "c"}},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "threshold"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "threshold")
	if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "ThresholdUnreachable" {
		t.Fatalf("status = %s/%s, want Failed/ThresholdUnreachable", got.Status.Phase, got.Status.Reason)
	}
}

func TestGateExpressionReconcilerWeightedSumEarlyFailed(t *testing.T) {
	threshold := int32(5)
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("a", gateExpressionPhaseFailed),
		gateExpressionWithStatus("b", gateExpressionPhasePending),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "weighted", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator:  "WEIGHTED_SUM",
				Weights:   []int32{10, 5},
				Threshold: &threshold,
				Operands:  []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "a"}, {ExpressionRef: "b"}},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "weighted"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "weighted")
	if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "WeightedSumUnreachable" {
		t.Fatalf("status = %s/%s, want Failed/WeightedSumUnreachable", got.Status.Phase, got.Status.Reason)
	}
}

func TestGateExpressionReconcilerDelayPersistsFirstObservation(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t,
		gateExpressionWithStatus("child", gateExpressionPhasePassed),
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "delay", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator:   "DELAY",
				Parameters: map[string]string{"duration": "1h"},
				Operands:   []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delay"}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("requeueAfter = %s, want positive delay requeue", res.RequeueAfter)
	}

	got := getGateExpression(t, c, "delay")
	if got.Status.Phase != gateExpressionPhasePending || got.Status.FirstObservedAt == nil {
		t.Fatalf("status = %s firstObservedAt=%v, want Pending with firstObservedAt", got.Status.Phase, got.Status.FirstObservedAt)
	}
}

func TestGateExpressionReconcilerDelayResetsOnNewGeneration(t *testing.T) {
	ctx := context.Background()
	oldFirstObserved := metav1.NewTime(time.Now().Add(-2 * time.Hour).Truncate(time.Second))
	delay := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "delay", Generation: 2},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator:   "DELAY",
			Parameters: map[string]string{"duration": "1h"},
			Operands:   []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
		},
		Status: kaprov1alpha2.GateExpressionStatus{
			ObservedGeneration: 1,
			Phase:              gateExpressionPhasePending,
			Reason:             "DelayPending",
			FirstObservedAt:    &oldFirstObserved,
		},
	}
	c := gateExpressionClient(t,
		gateExpressionWithStatus("child", gateExpressionPhasePassed),
		delay,
	)
	r := &GateExpressionReconciler{Client: c}

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delay"}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("requeueAfter = %s, want positive delay requeue", res.RequeueAfter)
	}

	got := getGateExpression(t, c, "delay")
	if got.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want 2", got.Status.ObservedGeneration)
	}
	if got.Status.Phase != gateExpressionPhasePending || got.Status.Reason != "DelayPending" {
		t.Fatalf("status = %s/%s, want Pending/DelayPending", got.Status.Phase, got.Status.Reason)
	}
	if got.Status.FirstObservedAt == nil || !got.Status.FirstObservedAt.After(oldFirstObserved.Time) {
		t.Fatalf("firstObservedAt = %v, want reset after %v", got.Status.FirstObservedAt, oldFirstObserved)
	}
}

func TestGateExpressionReconcilerDelayRestartUsesPersistedFirstObservation(t *testing.T) {
	ctx := context.Background()
	firstObserved := metav1.NewTime(time.Now().Add(-2 * time.Hour).Truncate(time.Second))
	delay := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "delay", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator:   "DELAY",
			Parameters: map[string]string{"duration": "1h"},
			Operands:   []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
		},
		Status: kaprov1alpha2.GateExpressionStatus{
			ObservedGeneration: 1,
			Phase:              gateExpressionPhasePending,
			Reason:             "DelayPending",
			FirstObservedAt:    &firstObserved,
		},
	}
	c := gateExpressionClient(t,
		gateExpressionWithStatus("child", gateExpressionPhasePassed),
		delay,
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delay"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "delay")
	if got.Status.Phase != gateExpressionPhasePassed {
		t.Fatalf("status = %s/%s, want Passed after persisted delay window", got.Status.Phase, got.Status.Reason)
	}
	if got.Status.FirstObservedAt == nil || !got.Status.FirstObservedAt.Time.Equal(firstObserved.Time) {
		t.Fatalf("firstObservedAt = %v, want preserved %v", got.Status.FirstObservedAt, firstObserved)
	}
}

func TestGateExpressionReconcilerDelayInvalidDurationFails(t *testing.T) {
	for _, duration := range []string{"0s", "-1s", "notaduration"} {
		t.Run(duration, func(t *testing.T) {
			ctx := context.Background()
			oldFirstObserved := metav1.NewTime(time.Now().Add(-1 * time.Hour).Truncate(time.Second))
			c := gateExpressionClient(t,
				gateExpressionWithStatus("child", gateExpressionPhasePassed),
				&kaprov1alpha2.GateExpression{
					ObjectMeta: metav1.ObjectMeta{Name: "delay", Generation: 1},
					Spec: kaprov1alpha2.GateExpressionSpec{
						Operator:   "DELAY",
						Parameters: map[string]string{"duration": duration},
						Operands:   []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
					},
					Status: kaprov1alpha2.GateExpressionStatus{
						ObservedGeneration: 1,
						Phase:              gateExpressionPhasePending,
						Reason:             "DelayPending",
						FirstObservedAt:    &oldFirstObserved,
					},
				},
			)
			r := &GateExpressionReconciler{Client: c}

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delay"}}); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			got := getGateExpression(t, c, "delay")
			if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "InvalidDuration" {
				t.Fatalf("status = %s/%s, want Failed/InvalidDuration", got.Status.Phase, got.Status.Reason)
			}
			if got.Status.FirstObservedAt != nil {
				t.Fatalf("firstObservedAt = %v, want nil for invalid duration", got.Status.FirstObservedAt)
			}
		})
	}
}

func TestGateExpressionReconcilerReferencedDelayUsesPersistedChildStatus(t *testing.T) {
	ctx := context.Background()
	child := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "delay-child", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator:   "DELAY",
			Parameters: map[string]string{"duration": "1h"},
			Operands: []kaprov1alpha2.GateExpressionOperand{
				{ExpressionRef: "passed"},
			},
		},
	}
	c := gateExpressionClient(t,
		gateExpressionWithStatus("passed", gateExpressionPhasePassed),
		child,
		&kaprov1alpha2.GateExpression{
			ObjectMeta: metav1.ObjectMeta{Name: "parent", Generation: 1},
			Spec: kaprov1alpha2.GateExpressionSpec{
				Operator: "ALL",
				Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "delay-child"}},
			},
		},
	)
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "parent"}}); err != nil {
		t.Fatalf("reconcile parent: %v", err)
	}

	parent := getGateExpression(t, c, "parent")
	if parent.Status.Phase != gateExpressionPhasePending || parent.Status.Reason != "OperandPending" {
		t.Fatalf("parent status = %s/%s, want Pending/OperandPending", parent.Status.Phase, parent.Status.Reason)
	}
	childAfter := getGateExpression(t, c, "delay-child")
	if childAfter.Status.FirstObservedAt != nil {
		t.Fatalf("parent reconcile mutated referenced DELAY firstObservedAt: %v", childAfter.Status.FirstObservedAt)
	}
}

func TestGateExpressionParentsForChild(t *testing.T) {
	child := gateExpressionWithStatus("child", gateExpressionPhasePassed)
	parent := &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "parent", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ALL",
			Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
		},
	}
	c := gateExpressionClient(t, child, parent)
	r := &GateExpressionReconciler{Client: c}

	got := r.parentsForGateExpression(context.Background(), child)
	if len(got) != 1 || got[0].Name != "parent" {
		t.Fatalf("parents = %#v, want parent", got)
	}
}

func gateExpressionClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha2.GateExpression{}).
		WithIndex(&kaprov1alpha2.GateExpression{}, gateExpressionRefIndex, func(obj client.Object) []string {
			expr, ok := obj.(*kaprov1alpha2.GateExpression)
			if !ok {
				return nil
			}
			return gateExpressionOperandRefs(expr.Spec.Operands)
		}).
		Build()
}

func gateExpressionWithStatus(name, phase string) *kaprov1alpha2.GateExpression {
	return &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ALL",
			Operands: []kaprov1alpha2.GateExpressionOperand{
				{InlineGate: &kaprov1alpha2.GatePolicySpec{Mode: kaprov1alpha2.GateModeAuto}},
			},
		},
		Status: kaprov1alpha2.GateExpressionStatus{ObservedGeneration: 1, Phase: phase, Reason: "Test"},
	}
}

func getGateExpression(t *testing.T, c client.Client, name string) *kaprov1alpha2.GateExpression {
	t.Helper()
	var expr kaprov1alpha2.GateExpression
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &expr); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return &expr
}

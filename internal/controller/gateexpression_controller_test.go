package controller

import (
	"context"
	"testing"

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

func TestGateExpressionReconcilerUnsupportedOperator(t *testing.T) {
	ctx := context.Background()
	c := gateExpressionClient(t, &kaprov1alpha2.GateExpression{
		ObjectMeta: metav1.ObjectMeta{Name: "any", Generation: 1},
		Spec: kaprov1alpha2.GateExpressionSpec{
			Operator: "ANY",
			Operands: []kaprov1alpha2.GateExpressionOperand{{ExpressionRef: "child"}},
		},
	})
	r := &GateExpressionReconciler{Client: c}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "any"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getGateExpression(t, c, "any")
	if got.Status.Phase != gateExpressionPhaseFailed || got.Status.Reason != "UnsupportedOperator" {
		t.Fatalf("status = %s/%s, want Failed/UnsupportedOperator", got.Status.Phase, got.Status.Reason)
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

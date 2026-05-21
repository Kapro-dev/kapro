package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	gateExpressionPhasePending = "Pending"
	gateExpressionPhasePassed  = "Passed"
	gateExpressionPhaseFailed  = "Failed"
	gateExpressionRefIndex     = ".spec.operands.expressionRef"
)

// GateExpressionReconciler records the preview composition outcome for
// GateExpression objects. v0.1.2 executes ALL over referenced child
// GateExpressions; inline gates remain Pending because target-specific runtime
// context belongs to the Target reconciler.
type GateExpressionReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=gateexpressions,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=gateexpressions/status,verbs=get;update;patch

func (r *GateExpressionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var expr kaprov1alpha2.GateExpression
	if err := r.Get(ctx, req.NamespacedName, &expr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	phase, reason, message, err := r.evaluate(ctx, &expr, map[string]bool{})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("evaluate GateExpression: %w", err)
	}

	before := expr.DeepCopy()
	desired := expr.DeepCopy()
	desired.Status.ObservedGeneration = desired.Generation
	desired.Status.Phase = phase
	desired.Status.Reason = reason
	status := metav1.ConditionUnknown
	switch phase {
	case gateExpressionPhasePassed:
		status = metav1.ConditionTrue
	case gateExpressionPhaseFailed:
		status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&desired.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha2.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: desired.Generation,
		LastTransitionTime: metav1.Now(),
	})
	if reflect.DeepEqual(before.Status, desired.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, desired, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch GateExpression status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *GateExpressionReconciler) evaluate(ctx context.Context, expr *kaprov1alpha2.GateExpression, seen map[string]bool) (string, string, string, error) {
	if expr.Spec.Operator != "ALL" {
		return gateExpressionPhaseFailed, "UnsupportedOperator",
			fmt.Sprintf("operator %s is reserved for v0.2.0; use ALL", expr.Spec.Operator), nil
	}
	if seen[expr.Name] {
		return gateExpressionPhaseFailed, "CycleDetected",
			fmt.Sprintf("cycle detected at %s", expr.Name), nil
	}
	seen[expr.Name] = true
	defer delete(seen, expr.Name)

	if len(expr.Spec.Operands) == 0 {
		return gateExpressionPhasePending, "NoOperands", "waiting for operands", nil
	}

	pending := false
	var pendingReasons []string
	for i, operand := range expr.Spec.Operands {
		phase, reason, message, err := r.evaluateOperand(ctx, operand, seen)
		if err != nil {
			return "", "", "", err
		}
		switch phase {
		case gateExpressionPhaseFailed:
			return gateExpressionPhaseFailed, reason, message, nil
		case gateExpressionPhasePassed:
			continue
		default:
			pending = true
			pendingReasons = append(pendingReasons, fmt.Sprintf("operand[%d]=%s", i, reason))
		}
	}
	if pending {
		return gateExpressionPhasePending, "OperandPending", strings.Join(pendingReasons, ", "), nil
	}
	return gateExpressionPhasePassed, "AllOperandsPassed", "all operands passed", nil
}

func (r *GateExpressionReconciler) evaluateOperand(ctx context.Context, operand kaprov1alpha2.GateExpressionOperand, seen map[string]bool) (string, string, string, error) {
	switch {
	case operand.ExpressionRef != "":
		var child kaprov1alpha2.GateExpression
		if err := r.Get(ctx, client.ObjectKey{Name: operand.ExpressionRef}, &child); err != nil {
			if apierrors.IsNotFound(err) {
				return gateExpressionPhaseFailed, "MissingReference",
					fmt.Sprintf("referenced GateExpression %s was not found", operand.ExpressionRef), nil
			}
			return "", "", "", fmt.Errorf("get referenced GateExpression %q: %w", operand.ExpressionRef, err)
		}
		if seen[child.Name] {
			return gateExpressionPhaseFailed, "CycleDetected",
				fmt.Sprintf("cycle detected at %s", child.Name), nil
		}
		if len(gateExpressionOperandRefs(child.Spec.Operands)) == 0 &&
			child.Status.Phase != "" &&
			child.Status.ObservedGeneration == child.Generation {
			reason := child.Status.Reason
			if reason == "" {
				reason = "ReferencedExpression"
			}
			return child.Status.Phase, reason, fmt.Sprintf("referenced GateExpression %s is %s", child.Name, child.Status.Phase), nil
		}
		return r.evaluate(ctx, &child, seen)
	case operand.InlineGate != nil:
		return gateExpressionPhasePending, "InlineGatePending", "inline gate evaluation requires target runtime context", nil
	default:
		return gateExpressionPhaseFailed, "InvalidOperand", "operand must set inlineGate or expressionRef", nil
	}
}

func (r *GateExpressionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kaprov1alpha2.GateExpression{},
		gateExpressionRefIndex,
		func(obj client.Object) []string {
			expr, ok := obj.(*kaprov1alpha2.GateExpression)
			if !ok {
				return nil
			}
			return gateExpressionOperandRefs(expr.Spec.Operands)
		},
	); err != nil {
		return fmt.Errorf("index GateExpression operand refs: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.GateExpression{}).
		Watches(
			&kaprov1alpha2.GateExpression{},
			handler.EnqueueRequestsFromMapFunc(r.parentsForGateExpression),
		).
		Complete(r)
}

func (r *GateExpressionReconciler) parentsForGateExpression(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj == nil || obj.GetName() == "" {
		return nil
	}
	var parents kaprov1alpha2.GateExpressionList
	if err := r.List(ctx, &parents, client.MatchingFields{gateExpressionRefIndex: obj.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "list parent GateExpressions", "gateExpression", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(parents.Items))
	for _, parent := range parents.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: parent.Name}})
	}
	return requests
}

func gateExpressionOperandRefs(operands []kaprov1alpha2.GateExpressionOperand) []string {
	var refs []string
	for _, operand := range operands {
		if operand.ExpressionRef != "" {
			refs = append(refs, operand.ExpressionRef)
		}
	}
	return refs
}

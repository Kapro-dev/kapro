package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

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

	before := expr.DeepCopy()
	desired := expr.DeepCopy()
	phase, reason, message, requeueAfter, err := r.evaluate(ctx, desired, map[string]bool{})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("evaluate GateExpression: %w", err)
	}

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
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

func (r *GateExpressionReconciler) evaluate(ctx context.Context, expr *kaprov1alpha2.GateExpression, seen map[string]bool) (string, string, string, time.Duration, error) {
	if seen[expr.Name] {
		return gateExpressionPhaseFailed, "CycleDetected",
			fmt.Sprintf("cycle detected at %s", expr.Name), 0, nil
	}
	seen[expr.Name] = true
	defer delete(seen, expr.Name)

	if len(expr.Spec.Operands) == 0 {
		return gateExpressionPhasePending, "NoOperands", "waiting for operands", 0, nil
	}

	phases := make([]string, 0, len(expr.Spec.Operands))
	reasons := make([]string, 0, len(expr.Spec.Operands))
	messages := make([]string, 0, len(expr.Spec.Operands))
	pending := false
	var pendingReasons []string
	for i, operand := range expr.Spec.Operands {
		phase, reason, message, err := r.evaluateOperand(ctx, operand, seen)
		if err != nil {
			return "", "", "", 0, err
		}
		phases = append(phases, phase)
		reasons = append(reasons, reason)
		messages = append(messages, message)
		if phase != gateExpressionPhasePassed && phase != gateExpressionPhaseFailed {
			pending = true
			pendingReasons = append(pendingReasons, fmt.Sprintf("operand[%d]=%s", i, reason))
		}
	}

	switch expr.Spec.Operator {
	case "ALL":
		for i, phase := range phases {
			if phase == gateExpressionPhaseFailed {
				return gateExpressionPhaseFailed, reasons[i], messages[i], 0, nil
			}
		}
		if pending {
			return gateExpressionPhasePending, "OperandPending", strings.Join(pendingReasons, ", "), 0, nil
		}
		return gateExpressionPhasePassed, "AllOperandsPassed", "all operands passed", 0, nil
	case "ANY":
		for i, phase := range phases {
			if phase == gateExpressionPhasePassed {
				return gateExpressionPhasePassed, reasons[i], messages[i], 0, nil
			}
		}
		if pending {
			return gateExpressionPhasePending, "OperandPending", strings.Join(pendingReasons, ", "), 0, nil
		}
		return gateExpressionPhaseFailed, "AllOperandsFailed", "all operands failed", 0, nil
	case "NOT":
		switch phases[0] {
		case gateExpressionPhasePassed:
			return gateExpressionPhaseFailed, "NotOperandPassed", "operand passed", 0, nil
		case gateExpressionPhaseFailed:
			return gateExpressionPhasePassed, "NotOperandFailed", "operand failed", 0, nil
		default:
			return gateExpressionPhasePending, "OperandPending", reasons[0], 0, nil
		}
	case "THRESHOLD":
		if expr.Spec.Threshold == nil {
			return gateExpressionPhaseFailed, "InvalidThreshold", "threshold is required", 0, nil
		}
		needed := int(*expr.Spec.Threshold)
		passed, failed := countGateExpressionPhases(phases)
		switch {
		case passed >= needed:
			return gateExpressionPhasePassed, "ThresholdReached", fmt.Sprintf("%d operands passed", passed), 0, nil
		case failed > len(phases)-needed:
			return gateExpressionPhaseFailed, "ThresholdUnreachable", fmt.Sprintf("%d failed operands makes threshold %d unreachable", failed, needed), 0, nil
		default:
			return gateExpressionPhasePending, "OperandPending", strings.Join(pendingReasons, ", "), 0, nil
		}
	case "WEIGHTED_SUM":
		threshold := int64(0)
		if expr.Spec.Threshold != nil {
			threshold = int64(*expr.Spec.Threshold)
		}
		passedSum := int64(0)
		possibleSum := int64(0)
		for i, phase := range phases {
			weight := int64(expr.Spec.Weights[i])
			if phase == gateExpressionPhasePassed {
				passedSum += weight
			}
			if phase != gateExpressionPhaseFailed {
				possibleSum += weight
			}
		}
		switch {
		case passedSum > threshold:
			return gateExpressionPhasePassed, "WeightedSumPassed", fmt.Sprintf("weighted sum %d > threshold %d", passedSum, threshold), 0, nil
		case possibleSum <= threshold:
			return gateExpressionPhaseFailed, "WeightedSumUnreachable", fmt.Sprintf("maximum possible weighted sum %d <= threshold %d", possibleSum, threshold), 0, nil
		default:
			return gateExpressionPhasePending, "OperandPending", strings.Join(pendingReasons, ", "), 0, nil
		}
	case "DELAY":
		duration, err := time.ParseDuration(expr.Spec.Parameters["duration"])
		if err != nil {
			return gateExpressionPhaseFailed, "InvalidDuration", err.Error(), 0, nil
		}
		now := metav1.Now()
		if expr.Status.FirstObservedAt == nil {
			expr.Status.FirstObservedAt = &now
			return gateExpressionPhasePending, "DelayPending", fmt.Sprintf("waiting %s before evaluating operand", duration), duration, nil
		}
		remaining := time.Until(expr.Status.FirstObservedAt.Time.Add(duration))
		if remaining > 0 {
			return gateExpressionPhasePending, "DelayPending", fmt.Sprintf("waiting %s before evaluating operand", remaining.Round(time.Second)), remaining, nil
		}
		return phases[0], reasons[0], messages[0], 0, nil
	default:
		return gateExpressionPhaseFailed, "UnsupportedOperator",
			fmt.Sprintf("unsupported operator %s", expr.Spec.Operator), 0, nil
	}
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
		for _, dep := range gateExpressionOperandRefs(child.Spec.Operands) {
			if seen[dep] {
				return gateExpressionPhaseFailed, "CycleDetected",
					fmt.Sprintf("cycle detected at %s", dep), nil
			}
		}
		if child.Status.Phase != "" && child.Status.ObservedGeneration == child.Generation {
			reason := child.Status.Reason
			if reason == "" {
				reason = "ReferencedExpression"
			}
			return child.Status.Phase, reason, fmt.Sprintf("referenced GateExpression %s is %s", child.Name, child.Status.Phase), nil
		}
		if child.Spec.Operator == "DELAY" {
			return gateExpressionPhasePending, "ReferencedDelayPending",
				fmt.Sprintf("referenced DELAY GateExpression %s has not persisted current status", child.Name), nil
		}
		phase, reason, message, _, err := r.evaluate(ctx, &child, seen)
		return phase, reason, message, err
	case operand.InlineGate != nil:
		return gateExpressionPhasePending, "InlineGatePending", "inline gate evaluation requires target runtime context", nil
	default:
		return gateExpressionPhaseFailed, "InvalidOperand", "operand must set inlineGate or expressionRef", nil
	}
}

func countGateExpressionPhases(phases []string) (passed, failed int) {
	for _, phase := range phases {
		switch phase {
		case gateExpressionPhasePassed:
			passed++
		case gateExpressionPhaseFailed:
			failed++
		}
	}
	return passed, failed
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

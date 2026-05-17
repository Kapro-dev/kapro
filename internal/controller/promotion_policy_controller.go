package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PromotionPolicyReconciler records readiness for reusable promotion guardrails.
type PromotionPolicyReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotionpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionpolicies/status,verbs=get;update;patch

func (r *PromotionPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy kaprov1alpha1.PromotionPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ready, reason, message := validatePromotionPolicy(&policy)
	patch := client.MergeFrom(policy.DeepCopy())
	now := metav1.Now()
	policy.Status.ObservedGeneration = policy.Generation
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})
	if ready {
		apimeta.RemoveStatusCondition(&policy.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	} else {
		apimeta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha1.ConditionTypeStalled,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: policy.Generation,
			LastTransitionTime: now,
		})
	}
	if err := r.Status().Patch(ctx, &policy, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PromotionPolicy status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *PromotionPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.PromotionPolicy{}).
		Complete(r)
}

func validatePromotionPolicy(policy *kaprov1alpha1.PromotionPolicy) (bool, string, string) {
	if policy.Spec.Selector != nil {
		if _, err := metav1.LabelSelectorAsSelector(policy.Spec.Selector); err != nil {
			return false, "InvalidSelector", err.Error()
		}
	}
	switch policy.Spec.OnFailure {
	case "", "halt", "continue":
		// supported
	case "rollback":
		return false, "UnsupportedOnFailureRollback",
			"PromotionPolicy.spec.onFailure=rollback is not implemented; in-flight runs will be suspended and flagged for operator review but no automated revert is performed. Use halt or continue, or omit the field."
	default:
		return false, "UnknownOnFailure",
			fmt.Sprintf("PromotionPolicy.spec.onFailure=%q is not a supported value (allowed: halt, rollback, continue)", policy.Spec.OnFailure)
	}
	for _, window := range policy.Spec.FreezeWindows {
		if _, err := validateFreezeWindow(window); err != nil {
			return false, "InvalidFreezeWindow", err.Error()
		}
	}
	for _, rule := range policy.Spec.CEL {
		if err := validatePromotionCEL(rule.Expression); err != nil {
			return false, "InvalidCEL", fmt.Sprintf("rule %q: %v", rule.Name, err)
		}
	}
	if policy.Spec.Verification != nil {
		return false, "UnsupportedVerification", "PromotionPolicy.spec.verification is preview-only; use PromotionTrigger signature verification"
	}
	return true, "PolicyReady", "PromotionPolicy is valid and enforceable"
}

func validateFreezeWindow(window kaprov1alpha1.AgentTimeWindow) (bool, error) {
	if window.Timezone != "" {
		if _, err := time.LoadLocation(window.Timezone); err != nil {
			return false, err
		}
	}
	start, err := parseWindowClock(window.StartTime)
	if err != nil {
		return false, fmt.Errorf("startTime: %w", err)
	}
	end, err := parseWindowClock(window.EndTime)
	if err != nil {
		return false, fmt.Errorf("endTime: %w", err)
	}
	if start == end {
		return false, fmt.Errorf("startTime and endTime must differ")
	}
	return false, nil
}

func validatePromotionCEL(expr string) error {
	if expr == "" {
		return fmt.Errorf("expression is empty")
	}
	env, err := promotionPolicyCELEnv()
	if err != nil {
		return err
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return issues.Err()
	}
	// CEL expressions that compile but return a non-bool type (e.g. just
	// `promotion.version`) would always fail evaluation at runtime because
	// evaluatePromotionCEL expects True/False. Reject them at Ready time so
	// the failure surfaces on the policy itself rather than terminally on
	// every matched Promotion.
	if ast.OutputType() != cel.BoolType {
		return fmt.Errorf("expression must return bool, got %s", ast.OutputType().String())
	}
	return nil
}

func promotionPolicyCELEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("promotion", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("versions", cel.MapType(cel.StringType, cel.StringType)),
	)
}

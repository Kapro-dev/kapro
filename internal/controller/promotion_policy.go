package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/common/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

type promotionPolicyDecision struct {
	Allowed      bool
	Terminal     bool
	Reason       string
	Message      string
	RequeueAfter time.Duration
}

func (r *PromotionReconciler) evaluatePromotionPolicies(ctx context.Context, promotion *kaprov1alpha1.Promotion, now time.Time) (promotionPolicyDecision, error) {
	if len(promotion.Spec.Policies) == 0 {
		return promotionPolicyDecision{Allowed: true}, nil
	}
	for _, ref := range promotion.Spec.Policies {
		if ref.Name == "" {
			return promotionPolicyDecision{
				Reason:   "InvalidPromotionPolicyRef",
				Message:  "Promotion.spec.policies contains an empty policy reference",
				Terminal: true,
			}, nil
		}
		var policy kaprov1alpha1.PromotionPolicy
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name}, &policy); err != nil {
			if apierrors.IsNotFound(err) {
				return promotionPolicyDecision{
					Reason:   "PromotionPolicyNotFound",
					Message:  fmt.Sprintf("PromotionPolicy %q was not found", ref.Name),
					Terminal: true,
				}, nil
			}
			return promotionPolicyDecision{}, fmt.Errorf("get PromotionPolicy %q: %w", ref.Name, err)
		}
		applies, err := promotionPolicyApplies(&policy, promotion)
		if err != nil {
			return promotionPolicyDecision{
				Reason:   "InvalidPromotionPolicySelector",
				Message:  fmt.Sprintf("PromotionPolicy %q selector is invalid: %v", policy.Name, err),
				Terminal: true,
			}, nil
		}
		if !applies {
			continue
		}
		decision := evaluatePromotionPolicy(&policy, promotion, now)
		if !decision.Allowed && promotionPolicyEnforces(&policy) {
			return decision, nil
		}
	}
	return promotionPolicyDecision{Allowed: true}, nil
}

func promotionPolicyApplies(policy *kaprov1alpha1.PromotionPolicy, promotion *kaprov1alpha1.Promotion) (bool, error) {
	if policy.Spec.Selector == nil {
		return true, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(policy.Spec.Selector)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(promotion.Labels)), nil
}

func evaluatePromotionPolicy(policy *kaprov1alpha1.PromotionPolicy, promotion *kaprov1alpha1.Promotion, now time.Time) promotionPolicyDecision {
	for _, window := range policy.Spec.FreezeWindows {
		active, err := freezeWindowActive(window, now)
		if err != nil {
			return promotionPolicyDecision{
				Reason:   "InvalidFreezeWindow",
				Message:  fmt.Sprintf("PromotionPolicy %q has an invalid freeze window: %v", policy.Name, err),
				Terminal: true,
			}
		}
		if active {
			return promotionPolicyDecision{
				Reason:       "FreezeWindowActive",
				Message:      fmt.Sprintf("PromotionPolicy %q blocks promotions during the active freeze window", policy.Name),
				RequeueAfter: time.Minute,
			}
		}
	}
	for _, rule := range policy.Spec.CEL {
		passed, err := evaluatePromotionCEL(rule.Expression, promotion)
		if err != nil {
			return promotionPolicyDecision{
				Reason:   "PromotionPolicyCELFailed",
				Message:  fmt.Sprintf("PromotionPolicy %q CEL rule %q failed to evaluate: %v", policy.Name, rule.Name, err),
				Terminal: true,
			}
		}
		if !passed {
			message := rule.Message
			if message == "" {
				message = fmt.Sprintf("PromotionPolicy %q CEL rule %q returned false", policy.Name, rule.Name)
			}
			return promotionPolicyDecision{
				Reason:   "PromotionPolicyDenied",
				Message:  message,
				Terminal: true,
			}
		}
	}
	if policy.Spec.Verification != nil {
		return promotionPolicyDecision{
			Reason:   "PromotionPolicyVerificationUnsupported",
			Message:  fmt.Sprintf("PromotionPolicy %q uses spec.verification, which is not enforced by the PromotionPolicy runtime yet; use PromotionTrigger signature verification for artifacts", policy.Name),
			Terminal: true,
		}
	}
	return promotionPolicyDecision{Allowed: true}
}

func promotionPolicyEnforces(policy *kaprov1alpha1.PromotionPolicy) bool {
	return policy.Spec.Mode == "" || policy.Spec.Mode == "enforce"
}

func evaluatePromotionCEL(expr string, promotion *kaprov1alpha1.Promotion) (bool, error) {
	if strings.TrimSpace(expr) == "" {
		return false, fmt.Errorf("expression is empty")
	}
	env, err := promotionPolicyCELEnv()
	if err != nil {
		return false, fmt.Errorf("create CEL env: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("compile: %w", issues.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("program: %w", err)
	}
	out, _, err := prg.Eval(map[string]any{
		"promotion": map[string]any{
			"name":      promotion.Name,
			"labels":    stringMapAny(promotion.Labels),
			"sourceRef": promotion.Spec.SourceRef,
			"version":   promotionVersion(promotion),
		},
		"versions": promotion.Spec.Versions,
	})
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}
	if out == types.True {
		return true, nil
	}
	if out == types.False {
		return false, nil
	}
	return false, fmt.Errorf("expression returned %T, expected bool", out.Value())
}

func stringMapAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func freezeWindowActive(window kaprov1alpha1.AgentTimeWindow, now time.Time) (bool, error) {
	loc := time.UTC
	if window.Timezone != "" {
		loaded, err := time.LoadLocation(window.Timezone)
		if err != nil {
			return false, err
		}
		loc = loaded
	}
	localNow := now.In(loc)
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
	return freezeWindowActiveForDay(window, localNow, localNow, start, end) ||
		freezeWindowActiveForDay(window, localNow.AddDate(0, 0, -1), localNow, start, end), nil
}

func freezeWindowActiveForDay(window kaprov1alpha1.AgentTimeWindow, windowDay time.Time, localNow time.Time, start, end time.Duration) bool {
	if !windowMatchesDay(window, windowDay.Weekday()) {
		return false
	}
	base := time.Date(windowDay.Year(), windowDay.Month(), windowDay.Day(), 0, 0, 0, 0, windowDay.Location())
	startAt := base.Add(start)
	endAt := base.Add(end)
	if !endAt.After(startAt) {
		endAt = endAt.Add(24 * time.Hour)
	}
	return !localNow.Before(startAt) && localNow.Before(endAt)
}

func parseWindowClock(value string) (time.Duration, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, err
	}
	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, nil
}

func windowMatchesDay(window kaprov1alpha1.AgentTimeWindow, day time.Weekday) bool {
	if len(window.DaysOfWeek) == 0 {
		return true
	}
	want := strings.ToLower(day.String())
	short := want[:3]
	for _, raw := range window.DaysOfWeek {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == want || value == short {
			return true
		}
	}
	return false
}

func (r *PromotionReconciler) patchPromotionPolicyDecision(ctx context.Context, promotion *kaprov1alpha1.Promotion, decision promotionPolicyDecision) error {
	patch := client.MergeFrom(promotion.DeepCopy())
	promotion.Status.ObservedGeneration = promotion.Generation
	promotion.Status.ActiveRun = ""
	promotion.Status.ResolvedVersion = ""
	if decision.Terminal {
		promotion.Status.Phase = kaprov1alpha1.PromotionPhaseFailed
	} else {
		promotion.Status.Phase = kaprov1alpha1.PromotionPhasePending
	}
	meta.SetStatusCondition(&promotion.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             decision.Reason,
		Message:            decision.Message,
		ObservedGeneration: promotion.Generation,
	})
	meta.SetStatusCondition(&promotion.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeStalled,
		Status:             metav1.ConditionTrue,
		Reason:             decision.Reason,
		Message:            decision.Message,
		ObservedGeneration: promotion.Generation,
	})
	if err := r.Status().Patch(ctx, promotion, patch); err != nil {
		return fmt.Errorf("patch Promotion policy status: %w", err)
	}
	return nil
}

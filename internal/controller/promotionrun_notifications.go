package controller

// promotionrun_notifications.go — notification engine wiring for the
// PromotionRun controller. Extracted from promotionrun_controller.go in
// D2-PR3 as part of the decomposition that the audit flagged: the 2000+
// LoC monolith was load-bearing-but-imperative, and notifications are a
// cleanly separable concern with one external dependency (the Notifier
// interface) and no FSM coupling.
//
// File-move only, no signature changes. Methods remain on
// PromotionRunReconciler so existing call sites (handlePending,
// handleProgressing, handleFailed, handleTimeout) compile unchanged.

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/notification"
)

func (r *PromotionRunReconciler) notifyPromotionRunEvent(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, eventType, message string) {
	if r.Notifier == nil {
		return
	}
	policy := r.notificationPolicyForPromotionRun(ctx, promotionrun)
	r.Notifier.Notify(ctx, notification.Event{
		Type:         eventType,
		Phase:        string(promotionrun.Status.Phase),
		Version:      promotionrun.Status.ResolvedVersion,
		PromotionRun: promotionrun.Name,
		Message:      message,
		IsFailure:    eventType == notification.EventPromotionRunFailed,
	}, policy)
}

func (r *PromotionRunReconciler) notifyStageEvent(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, promotionplanRef, stage, eventType, message string) {
	if r.Notifier == nil {
		return
	}
	policy := r.notificationPolicyForStage(ctx, promotionrun, promotionplanRef, stage)
	r.Notifier.Notify(ctx, notification.Event{
		Type:         eventType,
		Phase:        "Complete",
		Version:      promotionrun.Status.ResolvedVersion,
		PromotionRun: promotionrun.Name,
		Plan:         promotionplanRef,
		Stage:        stage,
		Message:      message,
	}, policy)
}

func (r *PromotionRunReconciler) notificationPolicyForPromotionRun(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun) notification.NotificationPolicy {
	policies := make([]notification.NotificationPolicy, 0)
	for _, promotionplanRef := range promotionrun.Spec.PromotionPlans {
		var promotionplan kaprov1alpha2.Plan
		if err := r.Get(ctx, client.ObjectKey{Name: promotionplanRef.Plan}, &promotionplan); err != nil {
			log.FromContext(ctx).Error(err, "failed to load promotionplan for promotionrun notification policy", "promotionplan", promotionplanRef.Plan)
			continue
		}
		for _, stage := range promotionplan.Spec.Stages {
			policies = append(policies, notificationPolicyFrom(stage.Gate))
		}
	}
	return mergeNotificationPolicies(policies...)
}

func (r *PromotionRunReconciler) notificationPolicyForStage(ctx context.Context, promotionrun *kaprov1alpha2.PromotionRun, promotionplanRefName, stageName string) notification.NotificationPolicy {
	for _, promotionplanRef := range promotionrun.Spec.PromotionPlans {
		if promotionplanRef.Name != promotionplanRefName {
			continue
		}
		var promotionplan kaprov1alpha2.Plan
		if err := r.Get(ctx, client.ObjectKey{Name: promotionplanRef.Plan}, &promotionplan); err != nil {
			log.FromContext(ctx).Error(err, "failed to load promotionplan for stage notification policy", "promotionplan", promotionplanRef.Plan)
			return notification.EmptyPolicy
		}
		for _, stage := range promotionplan.Spec.Stages {
			if stage.Name == stageName {
				return notificationPolicyFrom(stage.Gate)
			}
		}
	}
	return notification.EmptyPolicy
}

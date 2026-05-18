package controller

// target_fsm.go is named for historical reasons — the per-target rollout
// FSM itself now lives in:
//
//   - internal/promotion/fsm           : Machine[Phase, Env] primitive
//   - promotiontarget_controller.go    : buildFSM() phase table + handler bodies
//
// This file contains only the supporting cast that the FSM and the
// parent PromotionRunReconciler both reach for:
//
//   - eventTypeForPhase           : phase → notification event-type mapping
//                                   (kept in sync with the FSM graph by
//                                    TestEventTypeForPhase_CoversAllRegisteredPhases
//                                    in promotiontarget_fsm_graph_test.go)
//   - notificationPolicyFrom / mergeNotificationPolicies : notification plumbing
//   - targetToGateContext / targetAppKey / targetDesiredVersions / resolveSyncArgs
//                                 : per-target helpers consumed by handlers
//   - buildApprovalURLs / approvalIdentityHint : signed approval URLs
//   - triggerTargetRollback       : parent-side rollback driver, called from
//                                   PromotionRunReconciler when a stage fails
//                                   with onFailure=rollback
//   - approvalForPromotionRun     : watch mapper shared across reconcilers
//
// A future cleanup may rename this file to target_helpers.go; renaming was
// deferred to keep this commit reviewable against git blame.

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/token"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/notification"
)

// missingMCFailThreshold is the number of consecutive reconciles where a target's
// FleetCluster is not found before the target is transitioned to Failed.
const missingMCFailThreshold = 10

// --- Shared helpers ---

// eventTypeForPhase maps every TargetPhase to a stable semantic event type
// for the notification engine. This is the dual of the FSM phase table in
// promotiontarget_controller.go's buildFSM(); keeping the two in sync is
// asserted by TestEventTypeForPhase_CoversAllRegisteredPhases — every
// non-terminal phase the FSM registers must have a non-fallback mapping
// here so notification consumers see meaningful event types.
//
// This is a total switch with no fallback to ensure all event types are explicit.
func eventTypeForPhase(phase kaprov1alpha1.TargetPhase) string {
	switch phase {
	case kaprov1alpha1.TargetPhasePending:
		return notification.EventTargetPending
	case kaprov1alpha1.TargetPhaseVerification:
		return notification.EventTargetVerification
	case kaprov1alpha1.TargetPhaseHealthCheck:
		return notification.EventTargetHealthCheck
	case kaprov1alpha1.TargetPhaseSoaking:
		return notification.EventTargetSoaking
	case kaprov1alpha1.TargetPhaseMetricsCheck:
		return notification.EventTargetMetricsCheck
	case kaprov1alpha1.TargetPhaseWaitingApproval:
		return notification.EventApprovalRequired
	case kaprov1alpha1.TargetPhaseApplying:
		return notification.EventTargetApplying
	case kaprov1alpha1.TargetPhaseConverged:
		return notification.EventTargetConverged
	case kaprov1alpha1.TargetPhaseFailed:
		return notification.EventTargetFailed
	case kaprov1alpha1.TargetPhaseSkipped:
		return notification.EventTargetSkipped
	case "":
		return "" // initial empty phase, no notification
	default:
		return "kapro.promotionrun.target.unknown"
	}
}

func mergeNotificationPolicies(policies ...notification.NotificationPolicy) notification.NotificationPolicy {
	var channels []notification.Channel
	for _, policy := range policies {
		channels = append(channels, policy.Channels...)
	}
	if len(channels) == 0 {
		return notification.EmptyPolicy
	}
	return notification.NotificationPolicy{Channels: channels}
}

// notificationPolicyFrom converts a *GatePolicySpec into the value type expected
// by the notification package.
func notificationPolicyFrom(policy *kaprov1alpha1.GatePolicySpec) notification.NotificationPolicy {
	if policy == nil || len(policy.Notifications) == 0 {
		return notification.EmptyPolicy
	}
	channels := make([]notification.Channel, 0, len(policy.Notifications))
	for _, spec := range policy.Notifications {
		ch := notification.Channel{
			Type:   spec.Type,
			Events: spec.Events,
		}
		switch spec.Type {
		case "slack":
			if spec.Slack != nil {
				ch.Target = spec.Slack.Channel
			}
		case "webhook":
			if spec.Webhook != nil {
				ch.Target = spec.Webhook.URL
				ch.Format = spec.Webhook.Format
			}
		case "email":
			if spec.Email != nil {
				ch.Email = &notification.EmailConfig{
					To:            spec.Email.To,
					From:          spec.Email.From,
					SMTPSecretRef: spec.Email.SmtpSecretRef.Name,
				}
			}
		}
		channels = append(channels, ch)
	}
	return notification.NotificationPolicy{Channels: channels}
}

// targetToGateContext builds the gate evaluation context from a target entry.
// rt is the PromotionTarget owner — its UID and Name are carried into the gate context
// so that gates that create Kubernetes resources (e.g. Job gate) can set OwnerReferences.
func targetToGateContext(promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.PromotionTarget) *gate.Context {
	ctx := &gate.Context{
		Name:            syncName(promotionrun.Name, target.PromotionPlanRef, target.Stage, target.Target),
		Namespace:       promotionrun.Namespace,
		PromotionRunRef: promotionrun.Name,
		Target:          target.Target,
		PromotionPlan:   target.PromotionPlan,
		Stage:           target.Stage,
		Version:         target.Version,
		StartedAt:       target.StartedAt,
	}
	if rt != nil {
		ctx.OwnerUID = rt.UID
		ctx.OwnerName = rt.Name
	}
	return ctx
}

// targetAppKey returns the FleetCluster.status.currentVersions key for this target.
func targetAppKey(target *kaprov1alpha1.TargetStatus) string {
	if target.AppKey != "" {
		return target.AppKey
	}
	if len(target.DesiredVersions) == 1 {
		_, appKey := primaryDesiredVersion(target.DesiredVersions, "", "")
		if appKey != "" {
			return appKey
		}
	}
	return target.PromotionRunRef
}

func targetDesiredVersions(target *kaprov1alpha1.TargetStatus) map[string]string {
	if len(target.DesiredVersions) > 0 {
		return copyStringMap(target.DesiredVersions)
	}
	if target.Version == "" {
		return nil
	}
	return map[string]string{targetAppKey(target): target.Version}
}

// resolveSyncArgs builds the final args map for a GateTemplate.
func resolveSyncArgs(tmpl *kaprov1alpha1.GateTemplateSpec, ctx *gate.Context) map[string]string {
	args := make(map[string]string)
	for _, a := range tmpl.Args {
		if a.Value != "" {
			args[a.Name] = a.Value
		}
	}
	if ctx != nil {
		args["version"] = ctx.Version
		args["target"] = ctx.Target
		args["promotionrun"] = ctx.PromotionRunRef
		args["promotionplan"] = ctx.PromotionPlan
		args["stage"] = ctx.Stage
	}
	return args
}

// buildApprovalURLs returns signed approve and reject URLs for the given target.
func buildApprovalURLs(externalURL string, secret []byte, promotionrun *kaprov1alpha1.PromotionRun, target *kaprov1alpha1.TargetStatus) (approveURL, rejectURL string, err error) {
	exp := time.Now().Add(token.DefaultTTL).Unix()
	targetKey := syncName(promotionrun.Name, target.PromotionPlanRef, target.Stage, target.Target)

	baseClaims := token.Claims{
		SyncName:     targetKey,
		Namespace:    promotionrun.Namespace,
		PromotionRun: promotionrun.Name,
		Target:       target.Target,
		Version:      target.Version,
		UID:          string(promotionrun.UID) + "/" + targetKey,
		ApprovedBy:   approvalIdentityHint(target),
		Exp:          exp,
	}

	approveClaims := baseClaims
	approveClaims.Action = "approve"
	approveToken, err := token.Sign(approveClaims, secret)
	if err != nil {
		return "", "", fmt.Errorf("sign approve token: %w", err)
	}

	rejectClaims := baseClaims
	rejectClaims.Action = "reject"
	rejectToken, err := token.Sign(rejectClaims, secret)
	if err != nil {
		return "", "", fmt.Errorf("sign reject token: %w", err)
	}

	base := strings.TrimRight(externalURL, "/")
	approveURL = fmt.Sprintf("%s/approve/%s?token=%s", base, targetKey, approveToken)
	rejectURL = fmt.Sprintf("%s/reject/%s?token=%s", base, targetKey, rejectToken)
	return approveURL, rejectURL, nil
}

func approvalIdentityHint(target *kaprov1alpha1.TargetStatus) string {
	if target == nil || target.Gate == nil || target.Gate.Approval == nil {
		return ""
	}
	if len(target.Gate.Approval.Approvers) == 1 {
		return target.Gate.Approval.Approvers[0]
	}
	return ""
}

// --- Parent-side rollback logic ---

// triggerTargetRollback calls Actuator.Rollback() immediately, then appends a new
// rollback TargetStatus entry targeting the previous version.
// Called by PromotionRunReconciler when it detects a failed stage with onFailure=rollback.
func (r *PromotionRunReconciler) triggerTargetRollback(ctx context.Context, promotionrun *kaprov1alpha1.PromotionRun, i int) {
	log := log.FromContext(ctx)
	target := &promotionrun.Status.Targets[i]

	// 1. Call actuator.Rollback() immediately.
	var mc kaprov1alpha1.FleetCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err == nil {
		if r.ActuatorRegistry != nil {
			if act, actErr := r.ActuatorRegistry.Resolve(mc.Spec.Delivery.RegistryKey()); actErr == nil {
				if len(target.PreviousVersions) > 0 {
					if _, rbErr := act.ApplyDelta(ctx, actuator.DeltaApplyRequest{
						Cluster:         &mc,
						DesiredVersions: target.PreviousVersions,
					}); rbErr != nil {
						log.Error(rbErr, "actuator ApplyDelta() failed for rollback — rollback target will re-apply it")
					} else {
						log.Info("actuator ApplyDelta() rollback succeeded",
							"cluster", target.Target,
							"desiredVersions", target.PreviousVersions,
						)
					}
				} else if rbErr := act.Rollback(ctx, &mc, target.PreviousVersion, targetAppKey(target)); rbErr != nil {
					log.Error(rbErr, "actuator Rollback() failed — rollback target will re-apply it")
				} else {
					log.Info("actuator Rollback() succeeded",
						"cluster", target.Target,
						"previousVersion", target.PreviousVersion,
					)
				}
			}
		}
	}

	// 2. Append rollback entry (idempotent).
	for _, t := range promotionrun.Status.Targets {
		if t.Rollback && t.Target == target.Target &&
			t.PromotionPlanRef == target.PromotionPlanRef && t.Stage == target.Stage {
			log.Info("rollback target entry already exists", "target", target.Target)
			return
		}
	}

	rollbackTarget := kaprov1alpha1.TargetStatus{
		PromotionRunRef:  promotionrun.Name,
		Target:           target.Target,
		PromotionPlanRef: target.PromotionPlanRef,
		PromotionPlan:    target.PromotionPlan,
		Stage:            target.Stage,
		Version:          target.PreviousVersion,
		Gate:             target.Gate,
		AppKey:           target.AppKey,
		DesiredVersions:  copyStringMap(target.PreviousVersions),
		PreviousVersions: targetDesiredVersions(target),
		Phase:            kaprov1alpha1.TargetPhasePending,
		Rollback:         true,
	}
	if len(rollbackTarget.DesiredVersions) > 0 {
		rollbackTarget.Version, rollbackTarget.AppKey = primaryDesiredVersion(rollbackTarget.DesiredVersions, rollbackTarget.Version, rollbackTarget.AppKey)
	}
	promotionrun.Status.Targets = append(promotionrun.Status.Targets, rollbackTarget)

	log.Info("appended rollback target entry",
		"target", target.Target,
		"targetVersion", target.PreviousVersion,
	)
	r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "RollbackTriggered",
		"Auto-rollback to %s triggered for %s", target.PreviousVersion, target.Target)
	if r.Notifier != nil {
		r.Notifier.Notify(ctx, notification.Event{
			Type:          notification.EventRollbackStarted,
			Phase:         string(kaprov1alpha1.PromotionRunPhaseFailed),
			Version:       rollbackTarget.Version,
			Target:        rollbackTarget.Target,
			PromotionRun:  promotionrun.Name,
			PromotionPlan: rollbackTarget.PromotionPlanRef,
			Stage:         rollbackTarget.Stage,
			Message:       fmt.Sprintf("rollback to %s triggered for %s", rollbackTarget.Version, rollbackTarget.Target),
			IsFailure:     true,
		}, notificationPolicyFrom(target.Gate))
	}
}

// --- Watch mappers ---

// approvalForPromotionRun maps an Approval to the PromotionRun it should unblock.
func approvalForPromotionRun(_ context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.PromotionRun == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{
			Name:      approval.Spec.PromotionRun,
			Namespace: approval.Namespace,
		},
	}}
}

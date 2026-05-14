package controller

// target_fsm.go contains shared utilities for the per-target rollout FSM.
//
// The FSM is driven by ReleaseTargetReconciler (release_target_controller.go).
// ReleaseReconciler (release_controller.go) only creates ReleaseTarget children
// and aggregates their statuses — it never runs the FSM.
//
// This file contains:
//   - Shared helpers used by both controllers (name generation, gate context, etc.)
//   - Parent-side rollback logic (triggerTargetRollback, called when parent detects failure)
//   - Watch mappers shared across controllers

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
// MemberCluster is not found before the target is transitioned to Failed.
const missingMCFailThreshold = 10

// --- Shared helpers ---

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
// rt is the ReleaseTarget owner — its UID and Name are carried into the gate context
// so that gates that create Kubernetes resources (e.g. Job gate) can set OwnerReferences.
func targetToGateContext(release *kaprov1alpha1.Release, target *kaprov1alpha1.TargetStatus, rt *kaprov1alpha1.ReleaseTarget) *gate.Context {
	ctx := &gate.Context{
		Name:       syncName(release.Name, target.PipelineRef, target.Stage, target.Target),
		Namespace:  release.Namespace,
		ReleaseRef: release.Name,
		Target:     target.Target,
		Pipeline:   target.Pipeline,
		Stage:      target.Stage,
		Version:    target.Version,
		StartedAt:  target.StartedAt,
	}
	if rt != nil {
		ctx.OwnerUID = rt.UID
		ctx.OwnerName = rt.Name
	}
	return ctx
}

// targetAppKey returns the MemberCluster.status.currentVersions key for this target.
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
	return target.ReleaseRef
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
		args["release"] = ctx.ReleaseRef
		args["pipeline"] = ctx.Pipeline
		args["stage"] = ctx.Stage
	}
	return args
}

// buildApprovalURLs returns signed approve and reject URLs for the given target.
func buildApprovalURLs(externalURL string, secret []byte, release *kaprov1alpha1.Release, target *kaprov1alpha1.TargetStatus) (approveURL, rejectURL string, err error) {
	exp := time.Now().Add(token.DefaultTTL).Unix()
	targetKey := syncName(release.Name, target.PipelineRef, target.Stage, target.Target)

	baseClaims := token.Claims{
		SyncName:   targetKey,
		Namespace:  release.Namespace,
		Release:    release.Name,
		Target:     target.Target,
		Version:    target.Version,
		UID:        string(release.UID) + "/" + targetKey,
		ApprovedBy: approvalIdentityHint(target),
		Exp:        exp,
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
// Called by ReleaseReconciler when it detects a failed stage with onFailure=rollback.
func (r *ReleaseReconciler) triggerTargetRollback(ctx context.Context, release *kaprov1alpha1.Release, i int) {
	log := log.FromContext(ctx)
	target := &release.Status.Targets[i]

	// 1. Call actuator.Rollback() immediately.
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, client.ObjectKey{Name: target.Target}, &mc); err == nil {
		if r.ActuatorRegistry != nil {
			if act, actErr := r.ActuatorRegistry.Resolve(mc.Spec.Actuator.RegistryKey()); actErr == nil {
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
	for _, t := range release.Status.Targets {
		if t.Rollback && t.Target == target.Target &&
			t.PipelineRef == target.PipelineRef && t.Stage == target.Stage {
			log.Info("rollback target entry already exists", "target", target.Target)
			return
		}
	}

	rollbackTarget := kaprov1alpha1.TargetStatus{
		ReleaseRef:       release.Name,
		Target:           target.Target,
		PipelineRef:      target.PipelineRef,
		Pipeline:         target.Pipeline,
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
	release.Status.Targets = append(release.Status.Targets, rollbackTarget)

	log.Info("appended rollback target entry",
		"target", target.Target,
		"targetVersion", target.PreviousVersion,
	)
	r.Recorder.Eventf(release, corev1.EventTypeWarning, "RollbackTriggered",
		"Auto-rollback to %s triggered for %s", target.PreviousVersion, target.Target)
}

// --- Watch mappers ---

// approvalForRelease maps an Approval to the Release it should unblock.
func approvalForRelease(_ context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.Release == "" {
		return nil
	}
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{
			Name:      approval.Spec.Release,
			Namespace: approval.Namespace,
		},
	}}
}

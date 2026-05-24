package controller

import (
	"context"
	"fmt"
	"strconv"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
)

func (r *PromotionRunReconciler) emitDecisionTrace(ctx context.Context, spec kaproruntimev1alpha1.DecisionTraceSpec) {
	if err := r.DecisionTraceEmitter.Emit(ctx, spec); err != nil {
		log.FromContext(ctx).Error(err, "failed to emit DecisionTrace",
			"promotionrun", spec.PromotionRun, "eventType", spec.EventType, "source", spec.Source)
	}
}

func (r *TargetReconciler) emitDecisionTrace(ctx context.Context, spec kaproruntimev1alpha1.DecisionTraceSpec) {
	if err := r.DecisionTraceEmitter.Emit(ctx, spec); err != nil {
		log.FromContext(ctx).Error(err, "failed to emit DecisionTrace",
			"promotionrun", spec.PromotionRun, "eventType", spec.EventType, "source", spec.Source)
	}
}

func (r *TargetReconciler) emitGateDecisionTrace(
	ctx context.Context,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetExecutionState,
	gateName string,
	phase kaprov1alpha1.GatePhase,
	reason string,
	message string,
	evidence []gate.Evidence,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    kaproruntimev1alpha1.DecisionTraceEventGateEvaluate,
		Source:       gateName,
		Phase:        string(phase),
		Reason:       reason,
		Message:      message,
		Evidence:     decisionTraceEvidenceFromGateEvidence(evidence),
	})
}

func (r *TargetReconciler) emitDeliveryDecisionTraces(
	ctx context.Context,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetExecutionState,
	cluster *kaprov1alpha1.Cluster,
	desiredVersions map[string]string,
) {
	if promotionrun == nil || target == nil || cluster == nil {
		return
	}
	for appKey, desiredVersion := range desiredVersions {
		entry, ok := cluster.Status.Delivery[appKey]
		if !ok || entry.Phase == "" {
			continue
		}
		spec := kaproruntimev1alpha1.DecisionTraceSpec{
			PromotionRun: promotionrun.Name,
			Plan:         target.PlanRef,
			Stage:        target.Stage,
			Target:       target.Target,
			EventType:    kaproruntimev1alpha1.DecisionTraceEventDelivery,
			Source:       "cluster-delivery",
			Phase:        string(entry.Phase),
			Reason:       deliveryDecisionReason(entry),
			Message:      deliveryDecisionMessage(target.Target, appKey, entry),
			Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{{
				Type:   "cluster-delivery",
				Source: cluster.Name,
				Detail: deliveryDecisionEvidence(appKey, desiredVersion, entry),
			}},
		}
		if entry.LastAttemptedAt != nil {
			spec.Time = *entry.LastAttemptedAt
		}
		r.emitDecisionTrace(ctx, spec)
	}
}

func (r *TargetReconciler) emitTargetPhaseDecisionTrace(
	ctx context.Context,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetExecutionState,
	fromPhase kaprov1alpha1.TargetPhase,
	toPhase kaprov1alpha1.TargetPhase,
	reason string,
	message string,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
		Source:       "target-controller",
		Phase:        string(toPhase),
		Reason:       reason,
		Message:      message,
		Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{{
			Type:   "target-fsm",
			Source: "target-controller",
			Detail: targetPhaseDecisionEvidence(target, fromPhase, toPhase),
		}},
	})
}

func targetPhaseDecisionEvidence(target *kaprov1alpha1.TargetExecutionState, fromPhase, toPhase kaprov1alpha1.TargetPhase) map[string]string {
	detail := map[string]string{}
	addDetail(detail, "fromPhase", string(fromPhase))
	addDetail(detail, "toPhase", string(toPhase))
	addDetail(detail, "version", target.Version)
	addDetail(detail, "appKey", target.AppKey)
	addDetail(detail, "rollback", strconv.FormatBool(target.Rollback))
	addDetail(detail, "applyIssued", strconv.FormatBool(target.ApplyIssued))
	if target.Gate != nil {
		addDetail(detail, "onFailure", target.Gate.OnFailure)
	}
	return detail
}

func deliveryDecisionReason(entry kaprov1alpha1.ClusterDeliveryStatus) string {
	switch entry.Phase {
	case kaprov1alpha1.DeliveryPhaseConverged:
		return "DeliveryConverged"
	case kaprov1alpha1.DeliveryPhaseFailed:
		return "DeliveryFailed"
	case kaprov1alpha1.DeliveryPhaseSkipped:
		return "DeliverySkipped"
	default:
		return "DeliveryProgressing"
	}
}

func deliveryDecisionMessage(clusterName, appKey string, entry kaprov1alpha1.ClusterDeliveryStatus) string {
	msg := fmt.Sprintf("cluster %s app %s delivery %s", clusterName, appKey, entry.Phase)
	if entry.LastError != "" {
		return msg + ": " + entry.LastError
	}
	return msg
}

func deliveryDecisionEvidence(appKey, desiredVersion string, entry kaprov1alpha1.ClusterDeliveryStatus) map[string]string {
	detail := map[string]string{}
	addDetail(detail, "appKey", appKey)
	addDetail(detail, "desiredVersion", desiredVersion)
	addDetail(detail, "reportedDesiredVersion", entry.DesiredVersion)
	addDetail(detail, "observedDigest", entry.ObservedDigest)
	addDetail(detail, "format", entry.Format)
	addDetail(detail, "phase", string(entry.Phase))
	addDetail(detail, "appliedObjects", fmt.Sprint(entry.AppliedObjects))
	if entry.LastAttemptedAt != nil {
		addDetail(detail, "lastAttemptedAt", entry.LastAttemptedAt.Format(timeRFC3339Nano))
	}
	if entry.LastAppliedAt != nil {
		addDetail(detail, "lastAppliedAt", entry.LastAppliedAt.Format(timeRFC3339Nano))
	}
	addDetail(detail, "lastError", entry.LastError)
	if entry.Staging != nil {
		addDetail(detail, "stagingType", string(entry.Staging.Type))
		addDetail(detail, "stagingFailurePolicy", string(entry.Staging.FailurePolicy))
		addDetail(detail, "stagingFailurePhase", string(entry.Staging.FailurePhase))
		addDetail(detail, "stagedObjects", fmt.Sprint(entry.Staging.StagedObjects))
		addDetail(detail, "stagingFailedObjects", fmt.Sprint(entry.Staging.StagingFailedObjects))
		addDetail(detail, "committedObjects", fmt.Sprint(entry.Staging.CommittedObjects))
		addDetail(detail, "commitFailedObjects", fmt.Sprint(entry.Staging.CommitFailedObjects))
	}
	return detail
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func decisionTraceEvidenceFromPlanner(decision planner.Decision) kaproruntimev1alpha1.DecisionTraceEvidence {
	return kaproruntimev1alpha1.DecisionTraceEvidence{
		Type:   "planner",
		Source: decision.Plugin,
		Detail: map[string]string{
			"phase":   decision.Phase,
			"reason":  decision.Reason,
			"message": decision.Message,
		},
	}
}

func decisionTraceEvidenceFromGateEvidence(in []gate.Evidence) []kaproruntimev1alpha1.DecisionTraceEvidence {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaproruntimev1alpha1.DecisionTraceEvidence, 0, len(in))
	for _, e := range in {
		detail := map[string]string{}
		addDetail(detail, "analysisMode", e.AnalysisMode)
		addDetail(detail, "comparator", e.Comparator)
		addDetail(detail, "query", e.Query)
		addDetail(detail, "baselineQuery", e.BaselineQuery)
		addDetail(detail, "window", e.Window)
		addDetail(detail, "observedValue", e.ObservedValue)
		addDetail(detail, "threshold", e.Threshold)
		addDetail(detail, "baselineValue", e.BaselineValue)
		addDetail(detail, "sampleCount", fmt.Sprint(e.SampleCount))
		addDetail(detail, "decisionRule", e.DecisionRule)
		addDetail(detail, "reason", e.Reason)
		if e.BaselineHealthy != nil {
			addDetail(detail, "baselineHealthy", strconv.FormatBool(*e.BaselineHealthy))
		}
		if e.Confidence != nil {
			addDetail(detail, "confidence", fmt.Sprint(*e.Confidence))
		}
		if e.Score != nil {
			addDetail(detail, "score", fmt.Sprint(*e.Score))
		}
		out = append(out, kaproruntimev1alpha1.DecisionTraceEvidence{
			Type:   e.Type,
			Source: e.Provider,
			Detail: detail,
		})
	}
	return out
}

func addDetail(detail map[string]string, key, value string) {
	if value != "" {
		detail[key] = value
	}
}

// --- Capability-skip DecisionTrace helpers (v0.5.10, issue #317) ---
//
// When the controller resolves an actuator and discovers it does not implement
// a required capability (Apply/Observe/Rollback/Discover/DryRun), the
// controller MUST emit a DecisionTrace so the audit trail explains the skip.
// `kapro why <promotion>` surfaces these events alongside other decisions; an
// operator sees "RollbackUnsupported" instead of a silent log line.
//
// Reason constants are wire-stable strings; do not rename them lightly.

const (
	DecisionTraceReasonApplyUnsupported    = "ApplyUnsupported"
	DecisionTraceReasonObserveUnsupported  = "ObserveUnsupported"
	DecisionTraceReasonRollbackUnsupported = "RollbackUnsupported"
	DecisionTraceReasonDiscoverUnsupported = "DiscoverUnsupported"
	DecisionTraceReasonDryRunUnsupported   = "DryRunUnsupported"
)

// emitCapabilityUnsupportedTrace emits a DecisionTrace for an actuator
// capability gap during target FSM execution. Use the matching
// DecisionTraceReason* constant for reason; the message should name the
// actuator key and the operation that was skipped.
func (r *TargetReconciler) emitCapabilityUnsupportedTrace(
	ctx context.Context,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetExecutionState,
	eventType kaproruntimev1alpha1.DecisionTraceEventType,
	reason string,
	message string,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    eventType,
		Source:       "target-controller",
		Phase:        "Skipped",
		Reason:       reason,
		Message:      message,
		Time:         metav1.Now(),
	})
}

// emitCapabilityUnsupportedTracePR is the PromotionRunReconciler-side variant
// used from parent rollback flows in target_fsm.go.
func (r *PromotionRunReconciler) emitCapabilityUnsupportedTracePR(
	ctx context.Context,
	promotionrun *kaproruntimev1alpha1.PromotionRun,
	target *kaprov1alpha1.TargetExecutionState,
	eventType kaproruntimev1alpha1.DecisionTraceEventType,
	reason string,
	message string,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    eventType,
		Source:       "promotionrun-controller",
		Phase:        "Skipped",
		Reason:       reason,
		Message:      message,
		Time:         metav1.Now(),
	})
}

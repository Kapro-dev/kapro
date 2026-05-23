package controller

import (
	"context"
	"fmt"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
)

func (r *PromotionRunReconciler) emitDecisionTrace(ctx context.Context, spec kaprov1alpha2.DecisionTraceSpec) {
	if err := r.DecisionTraceEmitter.Emit(ctx, spec); err != nil {
		log.FromContext(ctx).Error(err, "failed to emit DecisionTrace",
			"promotionrun", spec.PromotionRun, "eventType", spec.EventType, "source", spec.Source)
	}
}

func (r *TargetReconciler) emitDecisionTrace(ctx context.Context, spec kaprov1alpha2.DecisionTraceSpec) {
	if err := r.DecisionTraceEmitter.Emit(ctx, spec); err != nil {
		log.FromContext(ctx).Error(err, "failed to emit DecisionTrace",
			"promotionrun", spec.PromotionRun, "eventType", spec.EventType, "source", spec.Source)
	}
}

func (r *TargetReconciler) emitGateDecisionTrace(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	target *kaprov1alpha2.TargetExecutionState,
	gateName string,
	phase kaprov1alpha2.GatePhase,
	reason string,
	message string,
	evidence []gate.Evidence,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    kaprov1alpha2.DecisionTraceEventGateEvaluate,
		Source:       gateName,
		Phase:        string(phase),
		Reason:       reason,
		Message:      message,
		Evidence:     decisionTraceEvidenceFromGateEvidence(evidence),
	})
}

func (r *TargetReconciler) emitDeliveryDecisionTraces(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	target *kaprov1alpha2.TargetExecutionState,
	cluster *kaprov1alpha2.Cluster,
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
		spec := kaprov1alpha2.DecisionTraceSpec{
			PromotionRun: promotionrun.Name,
			Plan:         target.PlanRef,
			Stage:        target.Stage,
			Target:       target.Target,
			EventType:    kaprov1alpha2.DecisionTraceEventDelivery,
			Source:       "cluster-delivery",
			Phase:        string(entry.Phase),
			Reason:       deliveryDecisionReason(entry),
			Message:      deliveryDecisionMessage(target.Target, appKey, entry),
			Evidence: []kaprov1alpha2.DecisionTraceEvidence{{
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
	promotionrun *kaprov1alpha2.PromotionRun,
	target *kaprov1alpha2.TargetExecutionState,
	fromPhase kaprov1alpha2.TargetPhase,
	toPhase kaprov1alpha2.TargetPhase,
	reason string,
	message string,
) {
	if promotionrun == nil || target == nil {
		return
	}
	r.emitDecisionTrace(ctx, kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: promotionrun.Name,
		Plan:         target.PlanRef,
		Stage:        target.Stage,
		Target:       target.Target,
		EventType:    kaprov1alpha2.DecisionTraceEventStage,
		Source:       "target-controller",
		Phase:        string(toPhase),
		Reason:       reason,
		Message:      message,
		Evidence: []kaprov1alpha2.DecisionTraceEvidence{{
			Type:   "target-fsm",
			Source: "target-controller",
			Detail: targetPhaseDecisionEvidence(target, fromPhase, toPhase),
		}},
	})
}

func targetPhaseDecisionEvidence(target *kaprov1alpha2.TargetExecutionState, fromPhase, toPhase kaprov1alpha2.TargetPhase) map[string]string {
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

func deliveryDecisionReason(entry kaprov1alpha2.ClusterDeliveryStatus) string {
	switch entry.Phase {
	case kaprov1alpha2.DeliveryPhaseConverged:
		return "DeliveryConverged"
	case kaprov1alpha2.DeliveryPhaseFailed:
		return "DeliveryFailed"
	case kaprov1alpha2.DeliveryPhaseSkipped:
		return "DeliverySkipped"
	default:
		return "DeliveryProgressing"
	}
}

func deliveryDecisionMessage(clusterName, appKey string, entry kaprov1alpha2.ClusterDeliveryStatus) string {
	msg := fmt.Sprintf("cluster %s app %s delivery %s", clusterName, appKey, entry.Phase)
	if entry.LastError != "" {
		return msg + ": " + entry.LastError
	}
	return msg
}

func deliveryDecisionEvidence(appKey, desiredVersion string, entry kaprov1alpha2.ClusterDeliveryStatus) map[string]string {
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

func decisionTraceEvidenceFromPlanner(decision planner.Decision) kaprov1alpha2.DecisionTraceEvidence {
	return kaprov1alpha2.DecisionTraceEvidence{
		Type:   "planner",
		Source: decision.Plugin,
		Detail: map[string]string{
			"phase":   decision.Phase,
			"reason":  decision.Reason,
			"message": decision.Message,
		},
	}
}

func decisionTraceEvidenceFromGateEvidence(in []gate.Evidence) []kaprov1alpha2.DecisionTraceEvidence {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha2.DecisionTraceEvidence, 0, len(in))
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
		out = append(out, kaprov1alpha2.DecisionTraceEvidence{
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

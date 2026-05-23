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

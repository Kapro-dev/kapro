package controller

import (
	"reflect"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/gate"
)

// findOrCreateGateStatus returns the existing GateRunStatus for the named gate,
// or a freshly initialised one with StartedAt = now if none exists yet.
func findOrCreateGateStatus(gates []kaprov1alpha2.GateRunStatus, name, now string) kaprov1alpha2.GateRunStatus {
	for _, g := range gates {
		if g.Name == name {
			return g
		}
	}
	return kaprov1alpha2.GateRunStatus{
		Name:      name,
		Phase:     kaprov1alpha2.GatePhasePending,
		StartedAt: now,
	}
}

// setGateStatus upserts a GateRunStatus entry (match by Name) in the slice.
func setGateStatus(gates *[]kaprov1alpha2.GateRunStatus, gs kaprov1alpha2.GateRunStatus) {
	for i, g := range *gates {
		if g.Name == gs.Name {
			(*gates)[i] = gs
			return
		}
	}
	*gates = append(*gates, gs)
}

// toAPIConditionResults converts gate-package ConditionResults to the API type.
func toAPIConditionResults(results []gate.ConditionResult) []kaprov1alpha2.GateConditionResult {
	out := make([]kaprov1alpha2.GateConditionResult, len(results))
	for i, r := range results {
		out[i] = kaprov1alpha2.GateConditionResult{
			Name:     r.Name,
			Phase:    r.Phase,
			Value:    r.Value,
			Message:  r.Message,
			Evidence: toAPIGateEvidence(r.Evidence),
		}
	}
	return out
}

func toAPIGateEvidence(evidence []gate.Evidence) []kaprov1alpha2.GateEvidence {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]kaprov1alpha2.GateEvidence, len(evidence))
	for i, e := range evidence {
		out[i] = kaprov1alpha2.GateEvidence{
			Type:                e.Type,
			Provider:            e.Provider,
			AnalysisMode:        e.AnalysisMode,
			Comparator:          e.Comparator,
			Query:               e.Query,
			BaselineQuery:       e.BaselineQuery,
			BaselineHealthQuery: e.BaselineHealthQuery,
			Window:              e.Window,
			Interval:            e.Interval,
			ObservedValue:       e.ObservedValue,
			Threshold:           e.Threshold,
			BaselineValue:       e.BaselineValue,
			BaselineHealthy:     e.BaselineHealthy,
			SampleCount:         e.SampleCount,
			Confidence:          e.Confidence,
			Alpha:               e.Alpha,
			PValue:              e.PValue,
			EffectSize:          e.EffectSize,
			Score:               e.Score,
			DecisionRule:        e.DecisionRule,
			Reason:              e.Reason,
		}
		if e.Projection != nil {
			out[i].Projection = &kaprov1alpha2.GateProjection{
				Horizon: e.Projection.Horizon,
				Value:   e.Projection.Value,
				Reason:  e.Projection.Reason,
			}
		}
	}
	return out
}

// promotionTargetSpecEqual returns true if two PromotionTargets have identical spec,
// labels, and owner references — meaning no API patch is needed.
// Used by persistPromotionTargets to skip no-op writes.
func promotionTargetSpecEqual(current, desired *kaprov1alpha2.Target) bool {
	return reflect.DeepEqual(current.Spec, desired.Spec) &&
		reflect.DeepEqual(current.Labels, desired.Labels) &&
		reflect.DeepEqual(current.OwnerReferences, desired.OwnerReferences)
}

func fleetClusterStatusEqualForRollouts(a, b kaprov1alpha2.ClusterStatus) bool {
	return a.Phase == b.Phase &&
		reflect.DeepEqual(a.CurrentVersions, b.CurrentVersions) &&
		a.DeliverySystem == b.DeliverySystem &&
		reflect.DeepEqual(a.Health, b.Health) &&
		a.ActivePromotionRun == b.ActivePromotionRun &&
		a.ControllerVersion == b.ControllerVersion &&
		reflect.DeepEqual(a.Capabilities, b.Capabilities) &&
		reflect.DeepEqual(a.Bootstrap, b.Bootstrap)
}

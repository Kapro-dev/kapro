package controller

import (
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/gate"
)

// isHeartbeatFresh returns true when the cluster last reported a heartbeat
// within the staleness window (2 × heartbeat interval = 2 min).
func isHeartbeatFresh(lastHeartbeat string) bool {
	if lastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < 2*time.Minute
}

// findOrCreateGateStatus returns the existing GateRunStatus for the named gate,
// or a freshly initialised one with StartedAt = now if none exists yet.
func findOrCreateGateStatus(gates []kaprov1alpha1.GateRunStatus, name, now string) kaprov1alpha1.GateRunStatus {
	for _, g := range gates {
		if g.Name == name {
			return g
		}
	}
	return kaprov1alpha1.GateRunStatus{
		Name:      name,
		Phase:     kaprov1alpha1.GatePhasePending,
		StartedAt: now,
	}
}

// setGateStatus upserts a GateRunStatus entry (match by Name) in the slice.
func setGateStatus(gates *[]kaprov1alpha1.GateRunStatus, gs kaprov1alpha1.GateRunStatus) {
	for i, g := range *gates {
		if g.Name == gs.Name {
			(*gates)[i] = gs
			return
		}
	}
	*gates = append(*gates, gs)
}

// toAPIConditionResults converts gate-package ConditionResults to the API type.
func toAPIConditionResults(results []gate.ConditionResult) []kaprov1alpha1.GateConditionResult {
	out := make([]kaprov1alpha1.GateConditionResult, len(results))
	for i, r := range results {
		out[i] = kaprov1alpha1.GateConditionResult{
			Name:    r.Name,
			Phase:   kaprov1alpha1.GatePhase(r.Phase),
			Value:   r.Value,
			Message: r.Message,
		}
	}
	return out
}

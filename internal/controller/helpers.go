package controller

import (
	"reflect"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/gate"
)

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
			Phase:   r.Phase,
			Value:   r.Value,
			Message: r.Message,
		}
	}
	return out
}

// releaseTargetSpecEqual returns true if two ReleaseTargets have identical spec,
// labels, and owner references — meaning no API patch is needed.
// Used by persistReleaseTargets to skip no-op writes.
func releaseTargetSpecEqual(current, desired *kaprov1alpha1.ReleaseTarget) bool {
	return reflect.DeepEqual(current.Spec, desired.Spec) &&
		reflect.DeepEqual(current.Labels, desired.Labels) &&
		reflect.DeepEqual(current.OwnerReferences, desired.OwnerReferences)
}

func memberClusterStatusEqualForRollouts(a, b kaprov1alpha1.MemberClusterStatus) bool {
	return a.Phase == b.Phase &&
		reflect.DeepEqual(a.CurrentVersions, b.CurrentVersions) &&
		a.DeliverySystem == b.DeliverySystem &&
		reflect.DeepEqual(a.Health, b.Health) &&
		a.ActiveRelease == b.ActiveRelease &&
		a.ControllerVersion == b.ControllerVersion &&
		reflect.DeepEqual(a.Capabilities, b.Capabilities) &&
		reflect.DeepEqual(a.Bootstrap, b.Bootstrap)
}

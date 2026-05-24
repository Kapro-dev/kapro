// Package runtimeview contains internal controller planning shapes.
//
// These types are deliberately not CRDs. They model the runtime ideas Kapro
// needs for fair dispatch, topology-aware placement, and sharded reconciliation
// without expanding the public API surface.
package runtimeview

import (
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"
)

// PromotionSnapshot is an immutable-ish input view for one reconciliation pass.
// Call NewPromotionSnapshot at the controller boundary so planner and dispatch
// code cannot mutate informer-cache objects by accident.
type PromotionSnapshot struct {
	Promotion        *kaprov1alpha1.Promotion
	Run              *kaproruntimev1alpha1.PromotionRun
	Targets          []kaproruntimev1alpha1.Target
	Clusters         []kaprov1alpha1.Cluster
	Substrates       []kaprov1alpha1.Substrate
	SubstrateClasses []kaprov1alpha1.SubstrateClass
	CapturedAt       time.Time
}

// NewPromotionSnapshot deep-copies controller inputs into a stable runtime view.
func NewPromotionSnapshot(
	promotion *kaprov1alpha1.Promotion,
	run *kaproruntimev1alpha1.PromotionRun,
	targets []kaproruntimev1alpha1.Target,
	clusters []kaprov1alpha1.Cluster,
	substrates []kaprov1alpha1.Substrate,
	substrateClasses []kaprov1alpha1.SubstrateClass,
	capturedAt time.Time,
) PromotionSnapshot {
	out := PromotionSnapshot{CapturedAt: capturedAt}
	if promotion != nil {
		out.Promotion = promotion.DeepCopy()
	}
	if run != nil {
		out.Run = run.DeepCopy()
	}
	out.Targets = copyTargets(targets)
	out.Clusters = copyClusters(clusters)
	out.Substrates = copySubstrates(substrates)
	out.SubstrateClasses = copySubstrateClasses(substrateClasses)
	return out
}

// WorkloadKey is the stable queue identity for a rollout unit.
type WorkloadKey struct {
	Promotion string
	Run       string
	PlanRef   string
	Stage     string
	Target    string
}

// DispatchOperation is the controller-internal command sent to a substrate
// actuator or spoke bridge.
type DispatchOperation struct {
	Key             WorkloadKey
	SubstrateRef    string
	ExecutionMode   kaprov1alpha1.ExecutionMode
	DesiredVersions map[string]string
	Topology        TopologyHint
	Shard           ShardAssignment
}

// DispatchResult is the normalized result returned by a substrate actuator or
// spoke bridge.
type DispatchResult struct {
	Key              WorkloadKey
	Phase            kaprov1alpha1.TargetPhase
	ObservedVersions map[string]string
	Objects          []kaprov1alpha1.SubstrateObjectStatus
	Reason           string
	Message          string
	RetryAfter       time.Duration
}

// QueueClass gives future fair-queue implementations a bounded scheduling lane
// without exposing PromotionQueue as a CRD.
type QueueClass string

const (
	QueueClassDefault QueueClass = "default"
	QueueClassCanary  QueueClass = "canary"
	QueueClassProd    QueueClass = "prod"
)

// TopologyHint captures placement-relevant cluster traits for internal planning.
type TopologyHint struct {
	Region      string
	Zone        string
	Tier        string
	Accelerator string
	GPUCount    int32
	GPUMemoryGB int32
	QueueClass  QueueClass
}

// ShardAssignment records which controller shard owns an operation.
type ShardAssignment struct {
	Name      string
	IsDefault bool
}

func copyTargets(in []kaproruntimev1alpha1.Target) []kaproruntimev1alpha1.Target {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaproruntimev1alpha1.Target, len(in))
	for i := range in {
		out[i] = *in[i].DeepCopy()
	}
	return out
}

func copyClusters(in []kaprov1alpha1.Cluster) []kaprov1alpha1.Cluster {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha1.Cluster, len(in))
	for i := range in {
		out[i] = *in[i].DeepCopy()
	}
	return out
}

func copySubstrates(in []kaprov1alpha1.Substrate) []kaprov1alpha1.Substrate {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha1.Substrate, len(in))
	for i := range in {
		out[i] = *in[i].DeepCopy()
	}
	return out
}

func copySubstrateClasses(in []kaprov1alpha1.SubstrateClass) []kaprov1alpha1.SubstrateClass {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha1.SubstrateClass, len(in))
	for i := range in {
		out[i] = *in[i].DeepCopy()
	}
	return out
}

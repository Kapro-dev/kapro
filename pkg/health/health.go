// Package health defines the contract for assessing the health of workloads
// on a target cluster.
//
// The release rollout FSM calls AssessHealth during the HealthCheck phase to
// determine whether a target cluster is in a state suitable for delivery.
//
// # Runtime model
//
// Built-in implementations live in internal/health/:
//   - gitops/ — argoproj/gitops-engine health assessment (Deployments, StatefulSets, DaemonSets)
//   - lws/    — LeaderWorkerSet health handler (AI/ML training jobs)
//
// An assessor may read a pre-computed health snapshot from the control plane
// (for example MemberCluster.status written by kapro-cluster-controller) or,
// when a *rest.Config is supplied, open a short-lived client against the
// target cluster directly. The choice is up to the assessor implementation;
// this package stays agnostic.
//
// External implementations can satisfy the Assessor interface and register
// at startup via pkg/registry.
//
// Health is not a pluggable CRD-level extension point today. The primary
// extension surfaces for Kapro are pkg/actuator and pkg/gate; see docs/SPEC.md.
package health

import (
	"context"

	"k8s.io/client-go/rest"
)

// Status mirrors argoproj/gitops-engine health statuses.
type Status string

const (
	// StatusHealthy means all assessed workloads are ready.
	StatusHealthy Status = "Healthy"
	// StatusProgressing means at least one workload is rolling out.
	StatusProgressing Status = "Progressing"
	// StatusDegraded means at least one workload has failed pods.
	StatusDegraded Status = "Degraded"
	// StatusSuspended means at least one workload is intentionally paused.
	StatusSuspended Status = "Suspended"
	// StatusMissing means the expected workloads were not found.
	StatusMissing Status = "Missing"
	// StatusUnknown is returned when health cannot be determined.
	StatusUnknown Status = "Unknown"
)

// ResourceHealth is the health of a single Kubernetes resource.
type ResourceHealth struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	Status    Status
	Message   string
}

// AssessRequest is the input to AssessHealth.
type AssessRequest struct {
	// Namespace to assess. Empty means all namespaces in the cluster.
	Namespace string
	// Kinds lists the resource kinds to assess.
	// Empty = [Deployment, StatefulSet, DaemonSet] (sensible defaults).
	Kinds []string
	// KubeConfig is an optional *rest.Config for the target cluster.
	// When nil, the assessor is expected to read a pre-computed health
	// snapshot from the control plane (for example MemberCluster.status).
	// When non-nil, the assessor may open a short-lived client against the
	// target cluster and compute health live.
	KubeConfig *rest.Config
}

// AssessResult is the overall health result for a cluster namespace.
type AssessResult struct {
	Overall   Status
	Resources []ResourceHealth
	Message   string
}

// Assessor is the health-assessment contract.
//
// AssessHealth must be idempotent and must not mutate cluster state.
// Implementations must be safe for concurrent use.
type Assessor interface {
	AssessHealth(ctx context.Context, req AssessRequest) (AssessResult, error)
}

// NopAssessor reports all workloads as Healthy without making any API calls.
// Use in tests and when the HealthCheck gate is intentionally disabled.
type NopAssessor struct{}

func (NopAssessor) AssessHealth(_ context.Context, _ AssessRequest) (AssessResult, error) {
	return AssessResult{
		Overall: StatusHealthy,
		Message: "nop: health check skipped",
	}, nil
}

// compile-time check: NopAssessor satisfies Assessor.
var _ Assessor = NopAssessor{}

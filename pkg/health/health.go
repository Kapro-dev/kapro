// Package health defines KHI — the Kapro Health Interface.
//
// KHI v1alpha1 is the contract for assessing the health of workloads on a
// target cluster. The SyncReconciler calls AssessHealth during the HealthCheck
// phase to determine whether the cluster is in a state suitable for delivery.
//
// # Two-path health assessment
//
// Path A (CRD provider — default): health is read from
// ManagedCluster.status.health written by kapro-cluster-controller.
// The AssessRequest.KubeConfig is nil on this path; the assessor reads the
// pre-computed health snapshot from the CRD.
//
// Path B (direct-connect — v0.3+): the controller provides a *rest.Config
// obtained from the KCI Connector. The assessor opens a short-lived client,
// lists workloads, and returns live health.
//
// Built-in implementations live in internal/health/:
//   - gitops/ — argoproj/gitops-engine health assessment (Deployments, StatefulSets, DaemonSets)
//   - lws/    — LeaderWorkerSet health handler (AI/ML training jobs)
//
// External implementations can satisfy the Assessor interface and register
// at startup.
//
// # Stability
//
// KHI v1alpha1 is stable. The Assessor interface has backwards-compatibility
// guarantees within a major version.
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
	// KubeConfig is the *rest.Config for the target cluster.
	// Nil on the CRD-provider path (health is read from ManagedCluster.status.health).
	// Non-nil on the direct-connect path (Connector.Connect() returns this).
	KubeConfig *rest.Config
}

// AssessResult is the overall health result for a cluster namespace.
type AssessResult struct {
	Overall   Status
	Resources []ResourceHealth
	Message   string
}

// Assessor is KHI v1alpha1: the Kapro Health Interface.
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

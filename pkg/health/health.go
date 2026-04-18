// Package health defines KHI — the Kapro Health Interface.
//
// KHI is the contract for assessing the health of workloads on a target cluster.
// Used by the Promotion controller during the HealthCheck phase.
//
// Built-in implementations live in internal/health/:
//   - gitops/ — uses argoproj/gitops-engine health assessment
//   - lws/    — LeaderWorkerSet health handler
//
// External implementations register via PluginRegistration CRD and communicate
// over proto/kapro/v1alpha1/health.proto (gRPC).
package health

import "context"

// Status mirrors gitops-engine health statuses.
type Status string

const (
	StatusHealthy     Status = "Healthy"
	StatusProgressing Status = "Progressing"
	StatusDegraded    Status = "Degraded"
	StatusSuspended   Status = "Suspended"
	StatusMissing     Status = "Missing"
	StatusUnknown     Status = "Unknown"
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
	// Namespace to assess. Empty = all namespaces.
	Namespace string
	// Kinds to assess. Empty = Deployment, StatefulSet, DaemonSet.
	Kinds []string
	// KubeConfig is the *rest.Config for the target cluster.
	// Use interface{} to avoid importing k8s.io/client-go here.
	// Implementations must type-assert to *rest.Config.
	KubeConfig interface{}
}

// AssessResult is the overall health result for a cluster namespace.
type AssessResult struct {
	Overall   Status
	Resources []ResourceHealth
	Message   string
}

// Assessor is KHI: the Kapro Health Interface.
//
// Implementations must be safe for concurrent use.
type Assessor interface {
	AssessHealth(ctx context.Context, req AssessRequest) (AssessResult, error)
}

// Package condition defines standard Kubernetes condition type constants
// used across Kapro CRD status fields.
//
// Condition types follow the Kubernetes API convention:
// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
package condition

const (
	// TypeReady indicates the resource is fully reconciled and operational.
	TypeReady = "Ready"

	// TypeProgressing indicates the resource is actively being reconciled.
	TypeProgressing = "Progressing"

	// TypeFailed indicates a non-recoverable failure.
	TypeFailed = "Failed"

	// TypeHealthy indicates the workload cluster is reachable and healthy.
	TypeHealthy = "Healthy"

	// TypeConverged indicates the cluster has converged to the target version.
	TypeConverged = "Converged"

	// TypeApprovalRequired indicates the resource is waiting for a human Approval object.
	TypeApprovalRequired = "ApprovalRequired"

	// TypeGatePassed indicates all gate checks (soak, metrics) have passed.
	TypeGatePassed = "GatePassed"
)

// Reason constants — used as condition.Reason field values.
const (
	ReasonReconciling        = "Reconciling"
	ReasonClusterUnreachable = "ClusterUnreachable"
	ReasonGateFailed         = "GateFailed"
	ReasonApprovalPending    = "ApprovalPending"
	ReasonConvergenceTimeout = "ConvergenceTimeout"
	ReasonSubResourceFailed  = "SubResourceFailed"
	ReasonScopeEmpty         = "ScopeEmpty"
	ReasonPipelineNotFound   = "PipelineNotFound"
)

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type PlanRef = kaprov1alpha1.PlanRef
type StageProgress = kaprov1alpha1.StageProgress
type PlannerResult = kaprov1alpha1.PlannerResult
type PlanProgress = kaprov1alpha1.PlanProgress
type PromotionRunScope = kaprov1alpha1.PromotionRunScope
type PromotionRunSpec = kaprov1alpha1.PromotionRunSpec
type PromotionRunPhase = kaprov1alpha1.PromotionRunPhase
type PromotionRunStatus = kaprov1alpha1.PromotionRunStatus
type TargetExecutionState = kaprov1alpha1.TargetExecutionState
type SubstrateObjectStatus = kaprov1alpha1.SubstrateObjectStatus
type TargetSpec = kaprov1alpha1.TargetSpec
type TargetStatus = kaprov1alpha1.TargetStatus
type PromotionRunReportSummary = kaprov1alpha1.PromotionRunReportSummary
type PromotionRunSummary = kaprov1alpha1.PromotionRunSummary
type AuditEntry = kaprov1alpha1.AuditEntry
type TargetPhase = kaprov1alpha1.TargetPhase

const (
	PromotionRunPhasePending     = kaprov1alpha1.PromotionRunPhasePending
	PromotionRunPhaseProgressing = kaprov1alpha1.PromotionRunPhaseProgressing
	PromotionRunPhaseComplete    = kaprov1alpha1.PromotionRunPhaseComplete
	PromotionRunPhaseFailed      = kaprov1alpha1.PromotionRunPhaseFailed
	PromotionRunPhaseSuperseded  = kaprov1alpha1.PromotionRunPhaseSuperseded

	TargetPhasePending         = kaprov1alpha1.TargetPhasePending
	TargetPhaseVerification    = kaprov1alpha1.TargetPhaseVerification
	TargetPhaseHealthCheck     = kaprov1alpha1.TargetPhaseHealthCheck
	TargetPhaseSoaking         = kaprov1alpha1.TargetPhaseSoaking
	TargetPhaseMetricsCheck    = kaprov1alpha1.TargetPhaseMetricsCheck
	TargetPhaseWaitingApproval = kaprov1alpha1.TargetPhaseWaitingApproval
	TargetPhaseApplying        = kaprov1alpha1.TargetPhaseApplying
	TargetPhaseConverged       = kaprov1alpha1.TargetPhaseConverged
	TargetPhaseFailed          = kaprov1alpha1.TargetPhaseFailed
	TargetPhaseSkipped         = kaprov1alpha1.TargetPhaseSkipped
)

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=prun,categories=kapro-runtime
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Targets",type=integer,JSONPath=`.status.summary.totalTargets`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.summary.syncedTargets`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.summary.failedTargets`
// +kubebuilder:printcolumn:name="Resolved",type=string,JSONPath=`.status.resolvedVersion`,priority=1
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startedAt`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionRun is one immutable execution attempt of a Promotion. Created and
// updated by the controller; users write Promotion and observe PromotionRun for
// execution detail.
type PromotionRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionRunSpec   `json:"spec,omitempty"`
	Status            PromotionRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionRun `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tgt,categories=kapro-runtime
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.runRef`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=`.spec.planRef`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Previous",type=string,JSONPath=`.status.previousVersion`,priority=1
// +kubebuilder:printcolumn:name="Rollback",type=boolean,JSONPath=`.spec.rollback`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Target is the child execution resource for one target rollout entry within a
// PromotionRun. It is the authoritative live state store for rollout execution.
type Target struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TargetSpec   `json:"spec,omitempty"`
	Status            TargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Target `json:"items"`
}

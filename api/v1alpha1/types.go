// Package v1alpha1 contains the Kapro API types.
// +groupName=kapro.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finalizer constants — prevents premature deletion of stateful resources.
const (
	// ReleaseFinalizer is added to Release objects to allow cleanup of Promotions and BatchRuns.
	ReleaseFinalizer = "kapro.io/release-finalizer"
	// BatchRunFinalizer is added to BatchRun objects to allow cleanup of in-progress cluster applies.
	BatchRunFinalizer = "kapro.io/batchrun-finalizer"
)

// ---- Artifact ---------------------------------------------------------------

// ArtifactSpec defines the desired state of Artifact.
type ArtifactSpec struct {
	Sources  []ArtifactSource `json:"sources"`
	Metadata ArtifactMeta     `json:"metadata,omitempty"`
}

type ArtifactSource struct {
	Type string    `json:"type"` // oci
	OCI  *OCIRef   `json:"oci,omitempty"`
}

type OCIRef struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

type ArtifactMeta struct {
	ReleasedBy  string `json:"releasedBy,omitempty"`
	Description string `json:"description,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Artifact is an immutable OCI bundle, digest-pinned, created by CI.
type Artifact struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArtifactSpec `json:"spec,omitempty"`
}

// ---- Environment ------------------------------------------------------------

// EnvironmentSpec defines the desired state of Environment.
type EnvironmentSpec struct {
	Actuator    ActuatorSpec    `json:"actuator"`
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`
}

type ActuatorSpec struct {
	Type string       `json:"type"` // flux | argocd
	Flux *FluxActuator `json:"flux,omitempty"`
}

type FluxActuator struct {
	Namespace         string `json:"namespace"`
	OCIRepository     string `json:"ociRepository"`
	KustomizationPath string `json:"kustomizationPath"`
}

type HealthCheckSpec struct {
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}

// EnvironmentStatus defines the observed state of Environment.
type EnvironmentStatus struct {
	ActiveRelease string `json:"activeRelease,omitempty"`
	Phase         string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Tier",type=string,JSONPath=`.metadata.labels.tier`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.metadata.labels.region`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Active Release",type=string,JSONPath=`.status.activeRelease`

// Environment represents one cluster managed by Kapro.
// Clusters can be GKE, EKS, AKS, OpenShift, on-prem, or any Kubernetes distribution.
// Labels on Environment are arbitrary — tier, region, cloud, wave, customer, etc.
type Environment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EnvironmentSpec   `json:"spec,omitempty"`
	Status            EnvironmentStatus `json:"status,omitempty"`
}

// ---- ClusterRegistration ----------------------------------------------------

// ClusterRegistrationSpec defines static info about the registered cluster.
type ClusterRegistrationSpec struct {
	EnvironmentRef    string `json:"environmentRef"`
	ControllerVersion string `json:"controllerVersion,omitempty"`
}

// ClusterRegistrationStatus is written by kapro-cluster-controller on the workload cluster.
type ClusterRegistrationStatus struct {
	LastHeartbeat  string             `json:"lastHeartbeat,omitempty"`
	Healthy        bool               `json:"healthy"`
	FluxVersion    string             `json:"fluxVersion,omitempty"`
	FluxReady      bool               `json:"fluxReady"`
	CurrentVersion string             `json:"currentVersion,omitempty"`
	Phase          ClusterPhase       `json:"phase,omitempty"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterPhase represents the convergence state of a workload cluster.
// +kubebuilder:validation:Enum=Pending;Applying;Converging;Converged;Failed
type ClusterPhase string

const (
	ClusterPhasePending    ClusterPhase = "Pending"
	ClusterPhaseApplying   ClusterPhase = "Applying"
	ClusterPhaseConverging ClusterPhase = "Converging"
	ClusterPhaseConverged  ClusterPhase = "Converged"
	ClusterPhaseFailed     ClusterPhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environmentRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.currentVersion`
// +kubebuilder:printcolumn:name="Healthy",type=boolean,JSONPath=`.status.healthy`
// +kubebuilder:printcolumn:name="Last Heartbeat",type=string,JSONPath=`.status.lastHeartbeat`

// ClusterRegistration is written by kapro-cluster-controller. The control plane reads it.
type ClusterRegistration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterRegistrationSpec   `json:"spec,omitempty"`
	Status            ClusterRegistrationStatus `json:"status,omitempty"`
}

// ---- PromotionPolicy --------------------------------------------------------

type PromotionMode string

const (
	PromotionModeAuto      PromotionMode = "auto"
	PromotionModeManual    PromotionMode = "manual"
	PromotionModeScheduled PromotionMode = "scheduled"
)

type PromotionPolicySpec struct {
	Mode     PromotionMode   `json:"mode"`
	Gate     GateSpec        `json:"gate,omitempty"`
	Approval *ApprovalConfig `json:"approval,omitempty"`
	OnFailure string         `json:"onFailure,omitempty"` // halt | rollback | continue
	Notifications []NotificationSpec `json:"notifications,omitempty"`
}

type GateSpec struct {
	SoakTime    string        `json:"soakTime,omitempty"`
	HealthCheck bool          `json:"healthCheck,omitempty"`
	Metrics     []MetricGate  `json:"metrics,omitempty"`
}

type MetricGate struct {
	Provider string `json:"provider"` // prometheus | datadog
	Query    string `json:"query"`
	Window   string `json:"window"`
}

type ApprovalConfig struct {
	Required  bool     `json:"required"`
	Approvers []string `json:"approvers,omitempty"`
}

type NotificationSpec struct {
	Type    string `json:"type"` // slack | webhook
	Channel string `json:"channel,omitempty"`
	URL     string `json:"url,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// PromotionPolicy defines reusable gate rules for promoting between environments.
type PromotionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionPolicySpec `json:"spec,omitempty"`
}

// ---- Pipeline ---------------------------------------------------------------

type PipelineSpec struct {
	Promotion   PipelinePromotion   `json:"promotion"`
	Progression PipelineProgression `json:"progression"`
}

type PipelinePromotion struct {
	Steps []PromotionStep `json:"steps"`
}

type PromotionStep struct {
	Name      string               `json:"name"`
	Selector  metav1.LabelSelector `json:"selector"`
	Policy    string               `json:"policy"`
	DependsOn []string             `json:"dependsOn,omitempty"`
}

type PipelineProgression struct {
	Batches []Batch `json:"batches"`
}

type Batch struct {
	Name      string               `json:"name"`
	DependsOn []string             `json:"dependsOn,omitempty"`
	Selectors []metav1.LabelSelector `json:"selectors"`
}

// PipelineStatus defines the observed state of Pipeline.
type PipelineStatus struct {
	Phase      string `json:"phase,omitempty"`
	ActiveStep string `json:"activeStep,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Pipeline defines the promotion DAG and batch progression owned by a Release.
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PipelineSpec   `json:"spec,omitempty"`
	Status            PipelineStatus `json:"status,omitempty"`
}

// ---- Release ----------------------------------------------------------------

type ReleaseSpec struct {
	Artifact          string              `json:"artifact"`
	Scope             ReleaseScope        `json:"scope"`
	PipelineRef       string              `json:"pipelineRef"`
	PipelineOverrides *PipelineOverrides  `json:"pipelineOverrides,omitempty"`
}

type ReleaseScope struct {
	Selector metav1.LabelSelector `json:"selector"`
}

type PipelineOverrides struct {
	GatePolicy *GatePolicyOverride `json:"gatePolicy,omitempty"`
}

type GatePolicyOverride struct {
	BakePeriod string `json:"bakePeriod,omitempty"`
}

type ReleasePhase string

const (
	ReleasePhasePending     ReleasePhase = "Pending"
	ReleasePhasePromoting   ReleasePhase = "Promoting"
	ReleasePhaseProgressing ReleasePhase = "Progressing"
	ReleasePhaseComplete    ReleasePhase = "Complete"
	ReleasePhaseFailed      ReleasePhase = "Failed"
)

// ReleaseStatus defines the observed state of Release.
type ReleaseStatus struct {
	Phase       ReleasePhase       `json:"phase,omitempty"`
	PipelineRef string             `json:"pipelineRef,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Artifact",type=string,JSONPath=`.spec.artifact`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// Release is the developer trigger for a progressive delivery rollout.
type Release struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseSpec   `json:"spec,omitempty"`
	Status            ReleaseStatus `json:"status,omitempty"`
}

// ---- Approval ---------------------------------------------------------------

type ApprovalKind string

const (
	ApprovalKindPromotion ApprovalKind = "Promotion"
	ApprovalKindBatch     ApprovalKind = "Batch"
)

type ApprovalSpec struct {
	Kind           ApprovalKind `json:"kind"`
	Ref            string       `json:"ref"`
	Release        string       `json:"release"`
	EnvironmentRef string       `json:"environmentRef,omitempty"`
	ApprovedBy     string       `json:"approvedBy"`
	Bypass         bool         `json:"bypass,omitempty"`
	Comment        string       `json:"comment,omitempty"`
}

// +kubebuilder:object:root=true

// Approval is a human gate signal to unblock a Promotion or Batch.
type Approval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ApprovalSpec `json:"spec,omitempty"`
}


// ---- Promotion --------------------------------------------------------------

type PromotionPhase string

const (
	PromotionPhasePending         PromotionPhase = "Pending"
	PromotionPhaseHealthCheck     PromotionPhase = "HealthCheck"
	PromotionPhaseSoaking         PromotionPhase = "Soaking"
	PromotionPhaseMetricsCheck    PromotionPhase = "MetricsCheck"
	PromotionPhaseWaitingApproval PromotionPhase = "WaitingApproval"
	PromotionPhaseApplying        PromotionPhase = "Applying"
	PromotionPhaseConverged       PromotionPhase = "Converged"
	PromotionPhaseFailed          PromotionPhase = "Failed"
)

type PromotionSpec struct {
	ReleaseRef     string `json:"releaseRef"`
	EnvironmentRef string `json:"environmentRef"`
	Version        string `json:"version"`
	PolicyRef      string `json:"policyRef,omitempty"`
}

type PromotionStatus struct {
	Phase      PromotionPhase     `json:"phase,omitempty"`
	StartedAt  string             `json:"startedAt,omitempty"`
	FinishedAt string             `json:"finishedAt,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	Message    string             `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environmentRef`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// Promotion drives a single cluster through the gate -> apply -> converge cycle.
type Promotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionSpec   `json:"spec,omitempty"`
	Status            PromotionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promotion `json:"items"`
}

// ---- BatchRun ---------------------------------------------------------------

type BatchPhase string

const (
	BatchPhasePending            BatchPhase = "Pending"
	BatchPhaseResolving          BatchPhase = "Resolving"
	BatchPhaseApplying           BatchPhase = "Applying"
	BatchPhaseWaitingConvergence BatchPhase = "WaitingConvergence"
	BatchPhaseGateCheck          BatchPhase = "GateCheck"
	BatchPhaseWaitingApproval    BatchPhase = "WaitingApproval"
	BatchPhaseComplete           BatchPhase = "Complete"
	BatchPhaseFailed             BatchPhase = "Failed"
)

type BatchRunSpec struct {
	ReleaseRef string                 `json:"releaseRef"`
	BatchName  string                 `json:"batchName"`
	Selectors  []metav1.LabelSelector `json:"selectors"`
	PolicyRef  string                 `json:"policyRef,omitempty"`
	DependsOn  []string               `json:"dependsOn,omitempty"`
}

type ClusterStatus struct {
	EnvironmentRef string       `json:"environmentRef"`
	Phase          ClusterPhase `json:"phase"`
	Version        string       `json:"version,omitempty"`
	Message        string       `json:"message,omitempty"`
}

type BatchRunStatus struct {
	Phase      BatchPhase         `json:"phase,omitempty"`
	Clusters   []ClusterStatus    `json:"clusters,omitempty"`
	StartedAt  string             `json:"startedAt,omitempty"`
	FinishedAt string             `json:"finishedAt,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.spec.releaseRef`
// +kubebuilder:printcolumn:name="Batch",type=string,JSONPath=`.spec.batchName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// BatchRun executes one batch of clusters from a Pipeline progression step.
type BatchRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BatchRunSpec   `json:"spec,omitempty"`
	Status            BatchRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BatchRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BatchRun `json:"items"`
}

// ---- List types (required for controller-runtime scheme registration) --------

// +kubebuilder:object:root=true
type ArtifactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Artifact `json:"items"`
}

// +kubebuilder:object:root=true
type EnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Environment `json:"items"`
}

// +kubebuilder:object:root=true
type ClusterRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRegistration `json:"items"`
}

// +kubebuilder:object:root=true
type PromotionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionPolicy `json:"items"`
}

// +kubebuilder:object:root=true
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

// +kubebuilder:object:root=true
type ReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Release `json:"items"`
}

// +kubebuilder:object:root=true
type ApprovalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Approval `json:"items"`
}

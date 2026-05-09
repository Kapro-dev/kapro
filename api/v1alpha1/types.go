// Package v1alpha1 contains the Kapro API types.
// +groupName=kapro.io
package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finalizer constants — prevents premature deletion of stateful resources.
const (
	// ReleaseFinalizer is added to Release objects to allow cleanup of owned rollout state.
	ReleaseFinalizer = "kapro.io/release-finalizer"
	// BootstrapTokenFinalizer is added to BootstrapToken objects to allow RBAC cleanup on deletion.
	BootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer" //nolint:gosec // not a credential
	// MemberClusterFinalizer is added to MemberCluster objects to allow bootstrap RBAC cleanup on deletion.
	MemberClusterFinalizer = "kapro.io/member-cluster-finalizer" //nolint:gosec // not a credential
)

// Condition type constants — Flux three-condition framework for operator status reporting.
const (
	// ConditionTypeReconciling indicates the controller is actively working on the object.
	// True while progressing, False when the object is terminal or suspended.
	ConditionTypeReconciling = "Reconciling"
	// ConditionTypeStalled indicates the object cannot progress without external intervention.
	// True when stuck (e.g. missing artifact, gate failure), False when healthy or recovering.
	ConditionTypeStalled = "Stalled"
)

// ---- Shared cluster types ---------------------------------------------------

// ActuatorSpec selects and configures the delivery backend for this cluster.
type TargetTopology struct {
	// Accelerator is the GPU/accelerator type in this cluster.
	// Well-known values: nvidia-h100, nvidia-a100, nvidia-l40s, amd-mi300x, tpu-v5e.
	// +optional
	Accelerator string `json:"accelerator,omitempty"`
	// GPUCount is the total number of GPU devices across all nodes in this cluster.
	// +optional
	// +kubebuilder:validation:Minimum=0
	GPUCount int32 `json:"gpuCount,omitempty"`
	// GPUMemoryGB is the memory per GPU device in gigabytes (e.g. 80 for H100 SXM).
	// +optional
	// +kubebuilder:validation:Minimum=0
	GPUMemoryGB int32 `json:"gpuMemoryGb,omitempty"`
	// NodeCount is the number of GPU nodes (not total devices).
	// +optional
	// +kubebuilder:validation:Minimum=0
	NodeCount int32 `json:"nodeCount,omitempty"`
	// Tier classifies the cluster's role in the delivery wave.
	// Well-known values: canary, shadow, prod.
	// +optional
	Tier string `json:"tier,omitempty"`
}

// ActuatorSpec selects and configures the delivery backend for this cluster.
type ActuatorSpec struct {
	// +kubebuilder:validation:Enum=flux;flux-operator
	Type         string              `json:"type"`
	Flux         *FluxActuator       `json:"flux,omitempty"`
	FluxOperator *FluxOperatorConfig `json:"fluxOperator,omitempty"`
}

// FluxOperatorConfig configures the Flux Operator actuator.
// Kapro patches ResourceSet inputs instead of individual Flux resources.
type FluxOperatorConfig struct {
	// ResourceSet is the name of the Flux Operator ResourceSet to patch.
	ResourceSet string `json:"resourceSet"`
	// Namespace is the namespace of the ResourceSet.
	// +kubebuilder:default="flux-system"
	Namespace string `json:"namespace,omitempty"`
	// InputField is the ResourceSet input field that holds the version/tag.
	// +kubebuilder:default="tag"
	InputField string `json:"inputField,omitempty"`
	// TenantField is the ResourceSet input field that identifies the cluster.
	// +kubebuilder:default="tenant"
	TenantField string `json:"tenantField,omitempty"`
}

type FluxActuator struct {
	// Namespace is the Flux namespace on the target cluster.
	// +kubebuilder:default="flux-system"
	Namespace string `json:"namespace,omitempty"`
	// OCIRepository is the Flux OCIRepository name that pulls the artifact.
	// Deprecated: use OCIRepositories for multi-artifact delivery.
	OCIRepository string `json:"ociRepository"`
	// OCIRepositories maps appKey -> Flux OCIRepository name for multi-artifact delivery.
	// When multiple artifacts are delivered to a cluster, every appKey in the rollout
	// must resolve to a repository name here.
	// +optional
	OCIRepositories map[string]string `json:"ociRepositories,omitempty"`
	// KustomizationPath is the path within the OCI artifact to the kustomization root.
	// +kubebuilder:default="."
	KustomizationPath string `json:"kustomizationPath,omitempty"`
}

type HealthCheckSpec struct {
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}

// ---- MemberCluster shared types --------------------------------------------
// registered workload cluster. Written by kapro-cluster-controller at bootstrap
// time and refreshed on each heartbeat.
//
// Platform engineers and pipeline authors can reference these fields in stage
// selectors for cloud-aware, GPU-aware, and compliance-aware delivery waves.
//
// Example stage selector:
//
//	stageSelector:
//	  matchLabels:
//	    kapro.io/cloud: gcp
//	    kapro.io/region: europe-west1
type ClusterCapabilities struct {
	// ---- Software versions ----

	// K8sVersion is the Kubernetes server version (e.g. "1.30.2").
	// +optional
	K8sVersion string `json:"k8sVersion,omitempty"`
	// FluxVersion is the Flux version installed on this cluster (e.g. "2.3.0").
	// Empty when Flux is not installed.
	// +optional
	FluxVersion string `json:"fluxVersion,omitempty"`
	// ArgoCDVersion is the ArgoCD version installed on this cluster (e.g. "2.11.0").
	// Empty when ArgoCD is not installed.
	// +optional
	ArgoCDVersion string `json:"argoCDVersion,omitempty"`
	// SveltosVersion is the Sveltos version installed on this cluster.
	// Empty when Sveltos is not installed.
	// +optional
	SveltosVersion string `json:"sveltosVersion,omitempty"`

	// ---- Infrastructure metadata ----

	// NodeCount is the total number of nodes in the cluster at registration time.
	// +optional
	NodeCount int `json:"nodeCount,omitempty"`

	// Cloud identifies the cloud provider hosting this cluster.
	// Well-known values: gcp, aws, azure, digitalocean, stackit, on-prem.
	// Written by kapro-cluster-controller based on IMDS detection.
	// +optional
	Cloud string `json:"cloud,omitempty"`

	// Region is the cloud region of this cluster (e.g. europe-west1, us-east-1, westeurope).
	// +optional
	Region string `json:"region,omitempty"`

	// Zone is the cloud availability zone of the primary node pool
	// (e.g. europe-west1-b, us-east-1a, 1). Empty for regional clusters.
	// +optional
	Zone string `json:"zone,omitempty"`

	// AccountID is the cloud account or project identifier.
	// GCP: project ID. AWS: account ID. Azure: subscription UUID.
	// DigitalOcean: team ID. StackIT: project UUID.
	// Used for cost attribution, audit, and cross-account routing.
	// +optional
	AccountID string `json:"accountID,omitempty"`

	// ClusterID is the cloud-provider-assigned cluster identifier.
	// GCP: cluster resource name. AWS: cluster ARN. Azure: resource ID.
	// DigitalOcean: cluster UUID. StackIT: cluster UUID.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`
}

// ClusterHealth aggregates workload health from the local delivery system.
type ClusterHealth struct {
	AllWorkloadsReady bool   `json:"allWorkloadsReady,omitempty"`
	ReadyWorkloads    int    `json:"readyWorkloads,omitempty"`
	FailedWorkloads   int    `json:"failedWorkloads,omitempty"`
	TotalWorkloads    int    `json:"totalWorkloads,omitempty"`
	Message           string `json:"message,omitempty"`
}

// ClusterPhase represents the convergence state of a workload cluster.
// +kubebuilder:validation:Enum=Pending;Applying;Converging;Converged;Failed;Unreachable
type ClusterPhase string

const (
	ClusterPhasePending     ClusterPhase = "Pending"
	ClusterPhaseApplying    ClusterPhase = "Applying"
	ClusterPhaseConverging  ClusterPhase = "Converging"
	ClusterPhaseConverged   ClusterPhase = "Converged"
	ClusterPhaseFailed      ClusterPhase = "Failed"
	ClusterPhaseUnreachable ClusterPhase = "Unreachable"
)

// ---- GatePolicy -------------------------------------------------------------

type GateMode string

const (
	GateModeAuto      GateMode = "auto"
	GateModeManual    GateMode = "manual"
	GateModeScheduled GateMode = "scheduled"
)

type GatePolicySpec struct {
	// +kubebuilder:validation:Enum=auto;manual;scheduled
	Mode     GateMode        `json:"mode"`
	Gate     GateSpec        `json:"gate,omitempty"`
	Approval *ApprovalConfig `json:"approval,omitempty"`
	// OnFailure controls what Kapro does when a gate fails or times out.
	//   halt (default): stop the rollout for this target and wait for human intervention.
	//     Use for checkout systems where automated rollback is too risky.
	//   rollback: automatically revert to the previous version.
	//     Only effective when a previous successful apply exists (PreviousVersion is set).
	//   continue: mark the gate as skipped and advance to the next phase.
	// +kubebuilder:validation:Enum=halt;rollback;continue
	// +kubebuilder:default=halt
	OnFailure     string             `json:"onFailure,omitempty"`
	Notifications []NotificationSpec `json:"notifications,omitempty"`
}

// ---- GateSpec (embedded in Stage.gate) --------------------------------------

type GateSpec struct {
	SoakTime string `json:"soakTime,omitempty"`
	// GateTimeout is the maximum duration the metrics gate may remain un-passed
	// before the target is failed. Only applies to MetricsCheck; infrastructure
	// errors (e.g. Prometheus unreachable) do not consume this budget.
	// Uses Go duration format, e.g. "30m", "1h". Empty means retry indefinitely.
	GateTimeout  string                `json:"gateTimeout,omitempty"`
	HealthCheck  bool                  `json:"healthCheck,omitempty"`
	Metrics      []MetricGate          `json:"metrics,omitempty"`
	Templates    []GateTemplateSpec    `json:"templates,omitempty"`
	Verification *VerificationGateSpec `json:"verification,omitempty"`
}

// VerificationGateSpec configures per-policy artifact signature verification.
type VerificationGateSpec struct {
	CosignPolicy *CosignPolicySpec `json:"cosignPolicy,omitempty"`
}

// CosignPolicySpec specifies how cosign should verify the artifact signature.
type CosignPolicySpec struct {
	Keyless *KeylessVerificationSpec `json:"keyless,omitempty"`
	Key     *KeyVerificationSpec     `json:"key,omitempty"`
}

// KeylessVerificationSpec configures OIDC keyless cosign verification.
type KeylessVerificationSpec struct {
	Issuer   string `json:"issuer,omitempty"`
	Subject  string `json:"subject,omitempty"`
	RekorURL string `json:"rekorURL,omitempty"`
}

// KeyVerificationSpec configures static public key cosign verification.
type KeyVerificationSpec struct {
	SecretRef CosignKeySecretRef `json:"secretRef"`
}

// CosignKeySecretRef identifies a cosign public key stored in a Kubernetes Secret.
type CosignKeySecretRef struct {
	Name string `json:"name"`
	// +kubebuilder:default=kapro-system
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:default=cosign.pub
	Key string `json:"key,omitempty"`
}

type MetricGate struct {
	Provider string `json:"provider"`
	// Query is a PromQL expression. The gate passes when the query returns a non-zero value.
	// Use range functions directly in the query for window-based checks, e.g.:
	//   min_over_time(error_rate[30m]) < 0.01
	// Or reference the Window field as a template: {{.Window}} is substituted at evaluation time.
	Query string `json:"query"`
	// Window is the lookback duration injected into the query template as {{.Window}}.
	// When Query already contains a hardcoded range (e.g. [5m]), this field is ignored.
	// Defaults to "5m".
	// +kubebuilder:default="5m"
	// +optional
	Window string `json:"window,omitempty"`
	// Interval controls how often the metric is re-evaluated while the gate is blocking.
	// Equivalent to Grafana's "Evaluate every" setting.
	// Defaults to "30s". Minimum "10s".
	// +kubebuilder:default="30s"
	// +optional
	Interval  string  `json:"interval,omitempty"`
	Endpoint  string  `json:"endpoint,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Config []byte `json:"config,omitempty"`
}

type ApprovalConfig struct {
	Required  bool     `json:"required"`
	Approvers []string `json:"approvers,omitempty"`
}

type NotificationSpec struct {
	Type    string             `json:"type"`
	Channel string             `json:"channel,omitempty"`
	URL     string             `json:"url,omitempty"`
	Email   *EmailNotifierSpec `json:"email,omitempty"`
}

// EmailNotifierSpec configures SMTP email delivery for gate notifications.
type EmailNotifierSpec struct {
	// +kubebuilder:validation:MinItems=1
	To   []string `json:"to"`
	From string   `json:"from,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	SmtpSecretRef corev1.LocalObjectReference `json:"smtpSecretRef"`
}

// ---- GateTemplate -----------------------------------------------------------

// GatePhase is the normalized execution state of a gate evaluation.
type GatePhase string

const (
	GatePhasePending      GatePhase = "Pending"
	GatePhaseRunning      GatePhase = "Running"
	GatePhasePassed       GatePhase = "Passed"
	GatePhaseFailed       GatePhase = "Failed"
	GatePhaseInconclusive GatePhase = "Inconclusive"
)

// GateTemplateSpec defines an inline, parameterised gate evaluation config.
// Embedded directly in GateSpec.Templates — no separate CRD needed.
type GateTemplateSpec struct {
	// Name uniquely identifies this template within the gate for status tracking
	// and Job naming. Required when type == "job" (used to generate Job name).
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Enum=cel;job;webhook
	Type string    `json:"type"`
	Args []GateArg `json:"args,omitempty"`
	// +kubebuilder:validation:Enum=halt;retry;skip
	// +kubebuilder:default=halt
	FailurePolicy string `json:"failurePolicy,omitempty"`
	// +kubebuilder:validation:Enum=retry;skip;halt
	// +kubebuilder:default=retry
	InconclusivePolicy string `json:"inconclusivePolicy,omitempty"`
	Timeout            string `json:"timeout,omitempty"`
	// +kubebuilder:default=3
	MaxAttempts int              `json:"maxAttempts,omitempty"`
	CEL         *CELGateSpec     `json:"cel,omitempty"`
	Job         *JobGateSpec     `json:"job,omitempty"`
	Webhook     *WebhookGateSpec `json:"webhook,omitempty"`
}

// GateArg declares a named parameter with an optional default value.
type GateArg struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// CELGateSpec configures the built-in CEL expression gate.
type CELGateSpec struct {
	Expression string `json:"expression"`
}

// JobGateSpec configures the Kubernetes Job gate.
type JobGateSpec struct {
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// WebhookGateSpec configures the HTTP webhook gate.
type WebhookGateSpec struct {
	URL          string `json:"url"`
	PollInterval string `json:"pollInterval,omitempty"`
}

// GateRunStatus is Kapro's authoritative snapshot of one gate evaluation.
type GateRunStatus struct {
	Name       string    `json:"name"`
	Phase      GatePhase `json:"phase"`
	Message    string    `json:"message,omitempty"`
	StartedAt  string    `json:"startedAt,omitempty"`
	FinishedAt string    `json:"finishedAt,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	VendorRef *corev1.ObjectReference `json:"vendorRef,omitempty"`
	Results   []GateConditionResult   `json:"results,omitempty"`
}

// GateConditionResult is the per-metric/condition result within a GateRunStatus.
type GateConditionResult struct {
	Name    string    `json:"name"`
	Phase   GatePhase `json:"phase"`
	Value   string    `json:"value,omitempty"`
	Message string    `json:"message,omitempty"`
}

// ---- Pipeline ---------------------------------------------------------------

// StageFailurePolicy controls what Kapro does when a stage fails.
// +kubebuilder:validation:Enum=halt;skip;rollback
type StageFailurePolicy string

const (
	StageFailurePolicyHalt     StageFailurePolicy = "halt"
	StageFailurePolicySkip     StageFailurePolicy = "skip"
	StageFailurePolicyRollback StageFailurePolicy = "rollback"
)

// StageDependency declares that a stage depends on an upstream stage,
// with optional soak time and availability strategy.
// This replaces bare stage name strings — enabling canary-unlock and
// soak-time patterns without heavyweight GateTemplate configuration.
type StageDependency struct {
	// Stage is the upstream stage name that must be satisfied.
	Stage string `json:"stage"`
	// RequiredSoakTime is how long ALL (or ANY, per Strategy) targets in the
	// upstream stage must have been continuously healthy before this stage
	// becomes eligible. Replaces GateTemplate for the most common gate pattern.
	// Zero or nil means no soak — advance as soon as the upstream stage completes.
	// +optional
	RequiredSoakTime *metav1.Duration `json:"requiredSoakTime,omitempty"`
	// Strategy controls when this dependency is considered satisfied.
	//   "all" (default): every target in the upstream stage must be verified.
	//   "any": at least one target in the upstream stage must be verified
	//          (canary-unlock pattern).
	// +kubebuilder:validation:Enum=all;any
	// +kubebuilder:default=all
	// +optional
	Strategy StageDependencyStrategy `json:"strategy,omitempty"`
}

// StageDependencyStrategy controls when an upstream dependency is satisfied.
// +kubebuilder:validation:Enum=all;any
type StageDependencyStrategy string

const (
	// StageDependencyAll requires every target in the upstream stage to be verified.
	StageDependencyAll StageDependencyStrategy = "all"
	// StageDependencyAny requires at least one target in the upstream stage to be verified (canary pattern).
	StageDependencyAny StageDependencyStrategy = "any"
)

// Stage is one node in a Pipeline's delivery DAG.
// It selects a set of target clusters by label selector, optionally gates them
// with a GatePolicy, and declares ordering via DependsOn.
//
// A single stage can target one or many clusters — the selector determines the
// fleet subset. Add a cluster to a wave by labeling its MemberCluster object;
// no Pipeline changes required.
type Stage struct {
	// Name uniquely identifies this stage within the pipeline.
	Name string `json:"name"`
	// Selector matches the target clusters that belong to this stage.
	Selector metav1.LabelSelector `json:"selector"`
	// DependsOn declares upstream stage dependencies with optional soak time
	// and availability strategy. Each entry names an upstream stage and
	// optionally specifies how long it must be healthy (RequiredSoakTime)
	// and whether all or any upstream targets must pass (Strategy).
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []StageDependency `json:"dependsOn,omitempty"`
	// Gate is the inline gate policy evaluated after all targets in this
	// stage converge. If nil, the stage advances immediately on convergence.
	// Use for complex gates (webhook, job, approval). For simple soak time,
	// prefer StageDependency.RequiredSoakTime instead.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// OnFailure controls what Kapro does when this stage fails.
	// halt (default): stop the pipeline, mark Release Failed.
	// skip: continue to downstream stages.
	// rollback: stop AND revert all targets promoted by earlier stages.
	// +kubebuilder:default=halt
	// +optional
	OnFailure StageFailurePolicy `json:"onFailure,omitempty"`
}

// PipelineSpec defines a reusable progressive delivery path as a flat DAG of stages.
// A Pipeline is a template — referenced by Release.spec.pipelines[].
// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type PipelineSpec struct {
	// Stages is the flat DAG of delivery stages.
	// Order is declared via DependsOn, not list position.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Stages []Stage `json:"stages"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pl,categories=kapro-all
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Pipeline defines a reusable progressive delivery path as a DAG of stages.
// Each stage selects a fleet subset via label selectors and optionally gates
// advancement with a GatePolicy. Referenced by Release.spec.pipelines[].
// Pipeline is a pure template — it has no controller, no status, no reconciler.
// Validation is enforced by the admission webhook. Execution state lives in Release.
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PipelineSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

// ---- Release ----------------------------------------------------------------

// ReleasePipelineRef is one node in the Release's pipeline DAG.
// Multiple pipelines can run in parallel; DependsOn declares ordering between them.
type ReleasePipelineRef struct {
	// Name uniquely identifies this pipeline node within the Release.
	Name string `json:"name"`
	// Pipeline is the name of the Pipeline CRD to execute.
	Pipeline string `json:"pipeline"`
	// DependsOn lists pipeline node names that must reach Complete before this one starts.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []string `json:"dependsOn,omitempty"`
}

// StageProgress tracks the execution state of one Stage within a pipeline.
type StageProgress struct {
	// Name is the stage name from Pipeline.spec.stages[].name.
	Name string `json:"name"`
	// Phase is the current state of this stage.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase string `json:"phase,omitempty"`
	// Total is the number of targets selected by this stage.
	Total int `json:"total,omitempty"`
	// Synced is the number of targets that have reached Converged.
	Synced int `json:"synced,omitempty"`
	// Failed is the number of targets that have reached Failed.
	Failed int `json:"failed,omitempty"`
}

// PipelineProgress tracks the execution state of one pipeline node in a Release.
type PipelineProgress struct {
	// Name matches ReleasePipelineRef.name.
	Name string `json:"name"`
	// Pipeline is the Pipeline CRD name.
	Pipeline string `json:"pipeline"`
	// Phase is the current execution state of this pipeline node.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase string `json:"phase,omitempty"`
	// StageProgress summarises the state of each stage in this pipeline.
	StageProgress []StageProgress `json:"stageProgress,omitempty"`
}

// ReleaseScope restricts a Release to an explicit subset of clusters.
// Only clusters listed in Targets will receive rollout entries.
type ReleaseScope struct {
	// Targets is the allowlist of target cluster names.
	// Must be non-empty when Scope is set — an empty list is ignored.
	Targets []string `json:"targets,omitempty"`
}

// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type ReleaseSpec struct {
	// Version is the OCI digest or tag to deliver across the fleet.
	Version string `json:"version"`
	// Pipelines is the DAG of pipeline nodes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Pipelines []ReleasePipelineRef `json:"pipelines"`
	// Suspended pauses all advancement when true.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts this Release to a subset of clusters.
	// +optional
	Scope *ReleaseScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for the entire Release.
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

type ReleasePhase string

const (
	ReleasePhasePending     ReleasePhase = "Pending"
	ReleasePhaseProgressing ReleasePhase = "Progressing"
	ReleasePhaseComplete    ReleasePhase = "Complete"
	ReleasePhaseFailed      ReleasePhase = "Failed"
)

// ReleaseStatus defines the observed state of Release.
type ReleaseStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	Phase              ReleasePhase `json:"phase,omitempty"`
	// ResolvedVersion is the OCI digest or tag resolved from spec.version.
	// Set once in Pending and never changed.
	ResolvedVersion string `json:"resolvedVersion,omitempty"`
	StartedAt         string             `json:"startedAt,omitempty"`
	CompletedAt       string             `json:"completedAt,omitempty"`
	// PipelineProgress tracks execution state of each pipeline node in the DAG.
	PipelineProgress []PipelineProgress `json:"pipelineProgress,omitempty"`
	// Targets is deprecated compatibility state. The authoritative per-target
	// rollout state lives in child ReleaseTarget objects.
	Targets []TargetStatus `json:"targets,omitempty"`
	// Report is the inline delivery summary.
	Report ReleaseReportSummary `json:"report,omitempty"`
	// AuditTrail records immutable delivery provenance. Capped at 50 entries.
	AuditTrail []AuditEntry       `json:"auditTrail,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=rel,categories=kapro-all
// +kubebuilder:printcolumn:name="Artifacts",type=integer,JSONPath=`.status.report.totalArtifacts`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.report.syncedTargets`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.report.pendingTargets`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Release is the trigger for a progressive delivery rollout across the cluster fleet.
// It references an Artifact and a DAG of Pipelines that define the delivery path.
// The Release controller drives the pipeline DAG, advancing each target cluster
// through the delivery FSM (MetricsCheck → WaitingApproval → Applying → Applied).
// Per-target execution state lives in child ReleaseTarget objects; Release.status
// stores only rollout summary, pipeline progress, and audit metadata.
type Release struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseSpec   `json:"spec,omitempty"`
	Status            ReleaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Release `json:"items"`
}

// ---- Per-target execution ---------------------------------------------------

// TargetStatus records the rollout state of one target cluster rollout. It is
// used as the status payload of ReleaseTarget and retained here as the
// controller's in-memory execution shape.
type TargetStatus struct {
	// ReleaseRef is the owning Release name.
	ReleaseRef string `json:"releaseRef,omitempty"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PipelineRef is the logical pipeline reference name from Release.spec.pipelines[i].name.
	// Used to disambiguate when the same Pipeline CRD is referenced multiple times.
	PipelineRef string `json:"pipelineRef,omitempty"`
	// Pipeline is the Pipeline CRD name this entry belongs to.
	Pipeline string `json:"pipeline"`
	// Stage is the stage name within the Pipeline.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in MemberCluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// When set, the actuator must converge all of these versions before the target completes.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// Phase is the FSM state of this target rollout.
	Phase      TargetPhase `json:"phase,omitempty"`
	StartedAt  string      `json:"startedAt,omitempty"`
	FinishedAt string      `json:"finishedAt,omitempty"`
	// PhaseEnteredAt records when the current phase was entered (used by gate timeouts).
	PhaseEnteredAt string `json:"phaseEnteredAt,omitempty"`
	Message        string `json:"message,omitempty"`
	// PreviousVersion holds the version before this rollout, used for rollback.
	PreviousVersion string `json:"previousVersion,omitempty"`
	// PreviousVersions holds the pre-rollout appKey -> version snapshot used for rollback.
	// +optional
	PreviousVersions map[string]string `json:"previousVersions,omitempty"`
	// ApprovalSentAt records when the approval notification was last dispatched.
	ApprovalSentAt string `json:"approvalSentAt,omitempty"`
	// Gates is the authoritative snapshot of GateTemplate evaluation state.
	Gates []GateRunStatus `json:"gates,omitempty"`
	// Rollback is true when this entry was created by a rollback trigger.
	Rollback bool `json:"rollback,omitempty"`
	// Rejected is set when a user rejects the approval via the webhook.
	Rejected bool `json:"rejected,omitempty"`
	// RejectedBy is the identity of the user who rejected the approval.
	RejectedBy string `json:"rejectedBy,omitempty"`
	// ApplyIssued is set once Actuator.Apply() has been called for this delivery
	// cycle. Guards against duplicate Apply() calls on subsequent reconciles while
	// the cluster is converging. Reset automatically on each transition into Applying.
	ApplyIssued bool `json:"applyIssued,omitempty"`
	// MissingMCCount tracks consecutive reconciles where the MemberCluster was not found.
	// When it reaches missingMCFailThreshold the target is transitioned to Failed.
	MissingMCCount int `json:"missingMCCount,omitempty"`
	// HeartbeatStaleSince records when the target's MemberCluster heartbeat first
	// became stale. Used to implement a configurable timeout — if the heartbeat
	// remains stale for longer than the threshold, the target is failed.
	// Reset when the heartbeat becomes fresh again.
	// +optional
	HeartbeatStaleSince string `json:"heartbeatStaleSince,omitempty"`
}

// ReleaseTargetSpec defines the immutable identity and desired intent for one
// target rollout entry within a Release.
type ReleaseTargetSpec struct {
	// ReleaseRef is the owning Release name.
	ReleaseRef string `json:"releaseRef"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PipelineRef is the logical pipeline reference name from Release.spec.pipelines[i].name.
	PipelineRef string `json:"pipelineRef,omitempty"`
	// Pipeline is the Pipeline CRD name this entry belongs to.
	Pipeline string `json:"pipeline"`
	// Stage is the stage name within the Pipeline.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in MemberCluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// Rollback is true when this entry was created by a rollback trigger.
	Rollback bool `json:"rollback,omitempty"`
	// Cancelled is set by the parent ReleaseReconciler to signal that this
	// target should stop progressing (e.g., stage halted due to peer failure).
	// The child ReleaseTargetReconciler observes this and transitions to Failed.
	// This avoids cross-controller status writes — parent owns spec, child owns status.
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`
	// CancelledReason explains why the target was cancelled.
	// +optional
	CancelledReason string `json:"cancelledReason,omitempty"`
}

// ReleaseTargetStatus is the live execution state for one target rollout.
type ReleaseTargetStatus struct {
	TargetStatus `json:",inline"`
	// ObservedGeneration records the ReleaseTarget generation last processed by
	// the child reconciler.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions provide the Kubernetes-native readiness/reconciling/stalled contract
	// for this execution object.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DecisionTrace stores the audit trail of AI agent and human decisions
	// for this target's approval gates. Written by the Decision API (webhook
	// server), never by the ReleaseTargetReconciler.
	// +optional
	DecisionTrace *DecisionTrace `json:"decisionTrace,omitempty"`
}

// DecisionTrace is the full audit trail of deployment decisions for one target.
// It stores the current decision, historical decisions, and human overrides.
type DecisionTrace struct {
	// Current is the active decision for this target's gate.
	// +optional
	Current *DecisionEntry `json:"current,omitempty"`
	// History is the list of previous decisions (Defer, superseded).
	// Capped at 10 entries; oldest are evicted.
	// +optional
	History []DecisionEntry `json:"history,omitempty"`
	// HumanOverrides records human overrides of AI decisions.
	// +optional
	HumanOverrides []HumanOverride `json:"humanOverrides,omitempty"`
}

// DecisionEntry records one AI agent decision with full reasoning.
type DecisionEntry struct {
	// DecisionID is a unique identifier for this decision.
	DecisionID string `json:"decisionId"`
	// Decision is the agent's verdict: Approve, Reject, or Defer.
	// +kubebuilder:validation:Enum=Approve;Reject;Defer
	Decision string `json:"decision"`
	// EffectiveDecision is the outcome after trust level evaluation.
	// May differ from Decision (e.g. Approve → PendingHumanConfirm).
	EffectiveDecision string `json:"effectiveDecision,omitempty"`
	// Identity identifies the agent that made this decision.
	Identity DecisionIdentity `json:"identity"`
	// Confidence is the agent's self-reported confidence score (0.0-1.0).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Confidence float64 `json:"confidence"`
	// Reasoning is the agent's human-readable explanation of the decision.
	Reasoning string `json:"reasoning"`
	// Factors are the weighted inputs the agent considered.
	// +optional
	Factors []DecisionFactor `json:"factors,omitempty"`
	// Conditions are post-decision checks that must hold for the approval
	// to remain valid (e.g. "error rate stays below 1% for 30m").
	// +optional
	Conditions []DecisionCondition `json:"conditions,omitempty"`
	// DecidedAt is the RFC3339 timestamp of the decision.
	DecidedAt string `json:"decidedAt"`
	// ExpiresAt is the RFC3339 timestamp after which the decision is void.
	// +optional
	ExpiresAt string `json:"expiresAt,omitempty"`
	// SupersededBy is the DecisionID that replaced this entry.
	// +optional
	SupersededBy string `json:"supersededBy,omitempty"`
	// SupersededReason explains why this entry was superseded.
	// +optional
	SupersededReason string `json:"supersededReason,omitempty"`
	// HumanConfirmation records a human's confirmation of this AI decision.
	// Only populated when the trust level is human-confirm.
	// +optional
	HumanConfirmation *HumanConfirmation `json:"humanConfirmation,omitempty"`
}

// DecisionIdentity identifies who made a decision.
type DecisionIdentity struct {
	// Name is the ServiceAccount name or human username.
	Name string `json:"name"`
	// Type is "ServiceAccount" for agents or "User" for humans.
	Type string `json:"type"`
	// Namespace is the ServiceAccount namespace (empty for users).
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// TrustLevel is the resolved trust level from the AgentPolicy.
	// +optional
	TrustLevel string `json:"trustLevel,omitempty"`
	// JWTFingerprint is the SHA-256 fingerprint of the JWT used for authentication.
	// +optional
	JWTFingerprint string `json:"jwtFingerprint,omitempty"`
}

// DecisionFactor is one weighted input the agent considered.
type DecisionFactor struct {
	// Name identifies the factor (e.g. "canary_error_rate").
	Name string `json:"name"`
	// Value is the observed value.
	Value float64 `json:"value"`
	// Weight is the relative importance (0.0-1.0).
	Weight float64 `json:"weight"`
	// Assessment is the agent's evaluation: pass, fail, or inconclusive.
	// +kubebuilder:validation:Enum=pass;fail;inconclusive
	Assessment string `json:"assessment"`
}

// DecisionCondition is a post-decision check that must hold.
type DecisionCondition struct {
	// Type identifies the condition (e.g. "MetricHold").
	Type string `json:"type"`
	// Metric is the metric to watch.
	// +optional
	Metric string `json:"metric,omitempty"`
	// Operator is the comparison operator (lt, gt, eq).
	// +optional
	Operator string `json:"operator,omitempty"`
	// Threshold is the metric threshold.
	// +optional
	Threshold float64 `json:"threshold,omitempty"`
	// Duration is how long the condition must hold.
	// +optional
	Duration string `json:"duration,omitempty"`
	// FailAction is what happens if the condition is violated.
	// +kubebuilder:validation:Enum=Rollback;Reject;Hold
	// +optional
	FailAction string `json:"failAction,omitempty"`
}

// HumanConfirmation records a human's sign-off on an AI decision.
type HumanConfirmation struct {
	// ConfirmedBy is the username of the confirming human.
	ConfirmedBy string `json:"confirmedBy"`
	// ConfirmedAt is the RFC3339 timestamp.
	ConfirmedAt string `json:"confirmedAt"`
	// Action is Confirmed or Rejected.
	// +kubebuilder:validation:Enum=Confirmed;Rejected
	Action string `json:"action"`
	// Comment is an optional human comment.
	// +optional
	Comment string `json:"comment,omitempty"`
}

// HumanOverride records a human overriding an AI decision.
type HumanOverride struct {
	// OverrideID is a unique identifier.
	OverrideID string `json:"overrideId"`
	// OverriddenDecisionID is the DecisionID being overridden.
	OverriddenDecisionID string `json:"overriddenDecisionId"`
	// Action is Approve or Reject.
	// +kubebuilder:validation:Enum=Approve;Reject
	Action string `json:"action"`
	// Identity is the human who issued the override.
	Identity string `json:"identity"`
	// Reason explains the override.
	Reason string `json:"reason"`
	// OverriddenAt is the RFC3339 timestamp.
	OverriddenAt string `json:"overriddenAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=relt,categories=kapro-all
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.spec.releaseRef`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Rollback",type=boolean,JSONPath=`.spec.rollback`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ReleaseTarget is the child execution resource for one target rollout entry
// within a Release. It is the authoritative live state store for rollout
// execution and replaces Release.status.targets as the persistence layer.
type ReleaseTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseTargetSpec   `json:"spec,omitempty"`
	Status            ReleaseTargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ReleaseTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReleaseTarget `json:"items"`
}

// ReleaseReportSummary is the inline delivery summary stored in
// Release.status.report. Counters + PendingApprovals only — per-target and
// per-gate detail live authoritatively in child ReleaseTarget objects (not
// duplicated here).
type ReleaseReportSummary struct {
	Phase             ReleasePhase `json:"phase,omitempty"`
	Artifact          string       `json:"artifact,omitempty"`
	ResolvedVersion   string       `json:"resolvedVersion,omitempty"`
	StartedAt         string       `json:"startedAt,omitempty"`
	CompletedAt       string       `json:"completedAt,omitempty"`
	Duration          string       `json:"duration,omitempty"`
	TotalTargets      int          `json:"totalTargets,omitempty"`
	SyncedTargets     int          `json:"syncedTargets,omitempty"`
	FailedTargets     int          `json:"failedTargets,omitempty"`
	PendingTargets    int          `json:"pendingTargets,omitempty"`
	RolledBackTargets int          `json:"rolledBackTargets,omitempty"`
	// TotalArtifacts is the number of artifacts in the resolved (merged) artifact list.
	TotalArtifacts int `json:"totalArtifacts,omitempty"`
	// DeltaArtifacts is the number of artifacts explicitly changed by this Release.
	// For derivedFrom releases, inherited artifacts are excluded.
	DeltaArtifacts int `json:"deltaArtifacts,omitempty"`
	// PendingApprovals lists "<release>-<ref>" Approval names that are
	// awaiting human signal. Derived from ReleaseTarget objects.
	PendingApprovals []string `json:"pendingApprovals,omitempty"`
}

// AuditEntry records the immutable delivery provenance of a completed Release.
// Stored in Release.status.auditTrail.
type AuditEntry struct {
	// Artifact is the OCI artifact that was delivered.
	Artifact string `json:"artifact"`
	// Release is the Release name.
	Release string `json:"release"`
	// DerivedFrom is the parent Artifact name.
	// +optional
	DerivedFrom string `json:"derivedFrom,omitempty"`
	// ReleaseDerivedFrom is the parent Release name.
	// +optional
	ReleaseDerivedFrom string `json:"releaseDerivedFrom,omitempty"`
	// ChangedComponents lists the components that changed relative to the parent artifact.
	// +optional
	ChangedComponents []string `json:"changedComponents,omitempty"`
	// Scope lists the target cluster names that were targeted. Nil = full-fleet rollout.
	// +optional
	Scope []string `json:"scope,omitempty"`
	// CompletedAt is when the Release completed.
	CompletedAt string `json:"completedAt,omitempty"`
}

// ---- Rollout execution ------------------------------------------------------

// TargetPhase is the execution state of one target cluster rollout within a Release.
// +kubebuilder:validation:Enum=Pending;Verification;HealthCheck;Soaking;MetricsCheck;WaitingApproval;Applying;Converged;Failed;Skipped
type TargetPhase string

const (
	TargetPhasePending         TargetPhase = "Pending"
	TargetPhaseVerification    TargetPhase = "Verification"
	TargetPhaseHealthCheck     TargetPhase = "HealthCheck"
	TargetPhaseSoaking         TargetPhase = "Soaking"
	TargetPhaseMetricsCheck    TargetPhase = "MetricsCheck"
	TargetPhaseWaitingApproval TargetPhase = "WaitingApproval"
	TargetPhaseApplying        TargetPhase = "Applying"
	TargetPhaseConverged       TargetPhase = "Converged"
	TargetPhaseFailed          TargetPhase = "Failed"
	// TargetPhaseSkipped means the target was bypassed because onFailure=continue was set
	// on a gate policy. A skipped target does not block subsequent targets in the pipeline.
	TargetPhaseSkipped TargetPhase = "Skipped"
)

// ---- Approval ---------------------------------------------------------------

// ApprovalSpec is the human signal that unblocks a waiting target.
//
// Identity is deterministic: one cluster-scoped Approval per (release, ref)
// pair. The object name is "<release>-<ref>". For target FSM approvals, ref is
// the stable sync key "<release>-<pipelineRef>-<stage>-<target>", so each
// waiting-approval step requires its own approval object.
type ApprovalSpec struct {
	// Release is the name of the Release this approval unblocks.
	// +kubebuilder:validation:Required
	Release string `json:"release"`
	// Target is the MemberCluster name this approval is for.
	// +kubebuilder:validation:Required
	Target string `json:"target"`
	// Ref identifies the exact approval scope within the Release. For target FSM
	// approvals this is the stable sync key "<release>-<pipelineRef>-<stage>-<target>".
	// External integrators may use another deterministic ref as long as
	// metadata.name is "<release>-<ref>".
	Ref string `json:"ref"`
	// ApprovedBy identifies the human approver. Populated by the admission
	// webhook from the request UserInfo when empty.
	// +kubebuilder:validation:Required
	ApprovedBy string `json:"approvedBy"`
	// Bypass skips subsequent gate conditions for the target. Reserved for
	// P0 hotfix escalations; audited via the ApprovalRecorded Event.
	// +optional
	Bypass bool `json:"bypass,omitempty"`
	// Comment is optional free-form justification.
	// +optional
	Comment string `json:"comment,omitempty"`
}

type ApprovalPhase string

const (
	ApprovalPhasePending  ApprovalPhase = "Pending"
	ApprovalPhaseRecorded ApprovalPhase = "Recorded"
)

type ApprovalStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ApprovalPhase      `json:"phase,omitempty"`
	ProcessedAt        string             `json:"processedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ap,categories=kapro-all
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.spec.release`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Recorded",type=string,JSONPath=`.status.conditions[?(@.type=="Recorded")].status`
// +kubebuilder:printcolumn:name="Approved By",type=string,JSONPath=`.spec.approvedBy`
// +kubebuilder:printcolumn:name="Bypass",type=boolean,JSONPath=`.spec.bypass`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Approval is the human gate signal that unblocks a waiting target rollout.
// Object name convention: "<release>-<ref>" as a cluster-scoped object.
type Approval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ApprovalSpec   `json:"spec,omitempty"`
	Status            ApprovalStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ApprovalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Approval `json:"items"`
}

// ---- MemberCluster ----------------------------------------------------------
//
// MemberCluster is the cluster-inventory CRD for Kapro. One object per physical
// cluster in the fleet.
//
// Ownership split:
//   - spec (except desiredVersion/desiredAppKey): written by the platform team
//   - spec.desiredVersion, spec.desiredAppKey: written by the kapro-operator (ReleaseReconciler)
//   - status: written by the cluster-controller (kapro-cluster-controller on the spoke)
//   - status.bootstrap: written by the hub csrapproval controller during registration

// MemberClusterSpec defines the desired state of a cluster in the Kapro fleet.
type MemberClusterSpec struct {
	// Actuator configures the delivery backend for this cluster.
	Actuator ActuatorSpec `json:"actuator"`

	// HealthCheck configures active health polling for this cluster.
	// +optional
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`

	// Topology holds hardware and scheduling metadata used by Pipeline stage selectors.
	// +optional
	Topology *TargetTopology `json:"topology,omitempty"`

	// DesiredVersion is written by the kapro-operator (ReleaseReconciler).
	// The cluster-controller polls this field and patches the local delivery system.
	// Deprecated: use DesiredVersions map for multi-artifact releases.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// DesiredAppKey is the key the cluster-controller uses when writing
	// status.currentVersions. Defaults to "default".
	// Deprecated: use DesiredVersions map for multi-artifact releases.
	// +optional
	DesiredAppKey string `json:"desiredAppKey,omitempty"`

	// DesiredVersions is a map of appKey → version written by the kapro-operator.
	// The cluster-controller iterates this map and patches local delivery for each changed entry.
	// This replaces the single DesiredVersion/DesiredAppKey pair for multi-artifact releases.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`

	// Suspend pauses all reconciliation for this cluster.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Bootstrap configures one-time cluster self-registration.
	// Platform engineers set tokenHash + expiresAt (or ttl); the cluster-controller
	// presents the pre-image token in a CSR to prove identity.
	// One bootstrap slot per cluster. To re-bootstrap, update tokenHash + expiresAt
	// and the hub resets the slot automatically.
	// +optional
	Bootstrap *MemberClusterBootstrapSpec `json:"bootstrap,omitempty"`
}

// MemberClusterBootstrapSpec holds the one-time registration credential.
type MemberClusterBootstrapSpec struct {
	// TokenHash is the SHA-256 hex hash of the pre-image bootstrap token (exactly 64 lowercase hex chars).
	// Platform team hashes the raw token and stores only the hash here; the cluster-controller
	// presents the plaintext pre-image. This ensures tokenHash cannot be used directly.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +optional
	TokenHash string `json:"tokenHash,omitempty"`

	// ExpiresAt is the absolute UTC time after which this bootstrap slot is invalid.
	// Set explicitly by the platform team for auditability.
	// If empty and TTL is set, the MemberCluster controller computes it on first reconcile.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// TTL is a convenience duration (e.g. "24h") used when ExpiresAt is not set explicitly.
	// The MemberCluster controller writes spec.bootstrap.expiresAt from
	// metadata.creationTimestamp + TTL at creation time and leaves it immutable.
	// +optional
	TTL string `json:"ttl,omitempty"`

	// Labels are applied to bootstrap resources created during registration
	// (ServiceAccount, kubeconfig Secret). Not used for stage selection — use
	// MemberCluster.metadata.labels for that.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// MemberClusterStatus is the observed state — written by cluster-controller and hub.
type MemberClusterStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ClusterPhase       `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`

	// CurrentVersions maps app keys to deployed versions. Written by cluster-controller.
	// +optional
	CurrentVersions map[string]string `json:"currentVersions,omitempty"`

	// DeliverySystem is the delivery system detected by the cluster-controller (e.g. "flux").
	// +optional
	DeliverySystem string `json:"deliverySystem,omitempty"`

	// Health aggregates workload health. Written by cluster-controller.
	// +optional
	Health ClusterHealth `json:"health,omitempty"`

	// ActiveRelease is the Release currently being processed for this cluster.
	// +optional
	ActiveRelease string `json:"activeRelease,omitempty"`

	// LastHeartbeat is the RFC3339 timestamp of the last cluster-controller heartbeat.
	// Deprecated: the authoritative heartbeat source is now the coordination.k8s.io/v1
	// Lease "kapro-heartbeat-<cluster>" in kapro-system. This field is still written
	// for backward compatibility but should not be relied upon for freshness checks.
	// +optional
	LastHeartbeat string `json:"lastHeartbeat,omitempty"`

	// ControllerVersion is the kapro-cluster-controller version running on this cluster.
	// +optional
	ControllerVersion string `json:"controllerVersion,omitempty"`

	// Capabilities is the self-reported capability profile written at registration time.
	// +optional
	Capabilities ClusterCapabilities `json:"capabilities,omitempty"`

	// Bootstrap tracks the one-time registration state. Written by the hub.
	// +optional
	Bootstrap *MemberClusterBootstrapStatus `json:"bootstrap,omitempty"`
}

// MemberClusterBootstrapStatus tracks the one-time bootstrap registration state.
type MemberClusterBootstrapStatus struct {
	// Used is true once the bootstrap token has been consumed by a successful CSR.
	Used bool `json:"used,omitempty"`

	// UsedAt is when the bootstrap token was consumed.
	// +optional
	UsedAt *metav1.Time `json:"usedAt,omitempty"`

	// IssuedCredentialFor is the cluster name the bootstrap credential was issued for.
	// +optional
	IssuedCredentialFor string `json:"issuedCredentialFor,omitempty"`

	// IssuedBootstrapKubeconfig is the name of the Secret in kapro-system that
	// contains the bootstrap kubeconfig. Operators copy this to the spoke cluster.
	// +optional
	IssuedBootstrapKubeconfig string `json:"issuedBootstrapKubeconfig,omitempty"`

	// BoundCSRName is the CSR that consumed this bootstrap slot.
	// Enables idempotent retry: if CSR approval fails transiently, the next reconcile
	// recognises the same CSR and re-approves rather than denying as replay.
	// +optional
	BoundCSRName string `json:"boundCSRName,omitempty"`
}

// IsHeartbeatFresh returns true when the cluster last reported a heartbeat
// within the given timeout window.
func (s *MemberClusterStatus) IsHeartbeatFresh(timeout time.Duration) bool {
	if s.LastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.LastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < timeout
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mc,categories=kapro-all
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="BootstrapReady",type=string,JSONPath=`.status.conditions[?(@.type=="BootstrapReady")].status`
// +kubebuilder:printcolumn:name="Delivery",type=string,JSONPath=`.status.deliverySystem`
// +kubebuilder:printcolumn:name="Healthy",type=boolean,JSONPath=`.status.health.allWorkloadsReady`
// +kubebuilder:printcolumn:name="Active Release",type=string,JSONPath=`.status.activeRelease`
// +kubebuilder:printcolumn:name="Heartbeat",type=string,JSONPath=`.status.lastHeartbeat`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MemberCluster represents one physical cluster in the Kapro fleet.
// It merges delivery config, fleet registration state,
// and BootstrapToken (one-time registration credential) into a single resource.
//
// Labels on MemberCluster drive Pipeline stage selection (tier, region, wave, cloud, etc.).
type MemberCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MemberClusterSpec   `json:"spec,omitempty"`
	Status            MemberClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MemberClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MemberCluster `json:"items"`
}

// ---- AgentPolicy ---------------------------------------------------------------

// AgentPolicyMode controls the agent's authority level.
// +kubebuilder:validation:Enum=auto;recommend;disabled
type AgentPolicyMode string

const (
	// AgentPolicyModeAuto allows the agent to create Approval objects autonomously
	// when confidence meets the threshold.
	AgentPolicyModeAuto AgentPolicyMode = "auto"
	// AgentPolicyModeRecommend allows the agent to post a recommendation
	// but a human must still create the Approval object.
	AgentPolicyModeRecommend AgentPolicyMode = "recommend"
	// AgentPolicyModeDisabled suspends the agent entirely.
	AgentPolicyModeDisabled AgentPolicyMode = "disabled"
)

// EscalationAction controls behavior when confidence is below threshold.
// +kubebuilder:validation:Enum=reject;hold;escalate
type EscalationAction string

const (
	EscalationReject   EscalationAction = "reject"
	EscalationHold     EscalationAction = "hold"
	EscalationEscalate EscalationAction = "escalate"
)

// AgentPolicySpec defines the trust boundary for one AI agent identity.
type AgentPolicySpec struct {
	// Identity binds this policy to a specific agent ServiceAccount.
	Identity AgentPolicyIdentity `json:"identity"`
	// Mode controls the agent's authority level.
	// +kubebuilder:default=recommend
	Mode AgentPolicyMode `json:"mode"`
	// Scope restricts which stages and clusters this agent may act on.
	Scope AgentScope `json:"scope"`
	// Confidence defines minimum confidence thresholds.
	Confidence AgentConfidencePolicy `json:"confidence"`
	// Escalation controls behavior when confidence is insufficient.
	Escalation AgentEscalationPolicy `json:"escalation"`
	// RateLimits caps the agent's decision throughput.
	// +optional
	RateLimits *AgentRateLimits `json:"rateLimits,omitempty"`
	// BlastRadius caps the maximum fleet impact per Release.
	// +optional
	BlastRadius *AgentBlastRadius `json:"blastRadius,omitempty"`
	// Audit defines what the agent must provide with each decision.
	Audit AgentAuditRequirements `json:"audit"`
	// TimeWindows restricts when the agent may issue decisions.
	// +optional
	TimeWindows []AgentTimeWindow `json:"timeWindows,omitempty"`
	// Priority determines precedence when multiple policies overlap.
	// Lower number = higher priority.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=100
	Priority int32 `json:"priority,omitempty"`
	// Suspended pauses this agent policy.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// AgentPolicyIdentity binds a policy to a ServiceAccount.
type AgentPolicyIdentity struct {
	// ServiceAccountName is the Kubernetes ServiceAccount the agent authenticates as.
	ServiceAccountName string `json:"serviceAccountName"`
	// ServiceAccountNamespace is the namespace of the ServiceAccount.
	// +kubebuilder:default=kapro-system
	ServiceAccountNamespace string `json:"serviceAccountNamespace,omitempty"`
}

// AgentScope defines what the agent can see and act on.
type AgentScope struct {
	// Stages lists stage names the agent may approve. Empty means all stages.
	// +optional
	Stages []string `json:"stages,omitempty"`
	// ExcludeStages lists stage names explicitly denied. Takes precedence over Stages.
	// +optional
	ExcludeStages []string `json:"excludeStages,omitempty"`
	// ClusterSelector restricts to targets matching these labels.
	// +optional
	ClusterSelector *metav1.LabelSelector `json:"clusterSelector,omitempty"`
	// ExcludeClusters is an explicit denylist of MemberCluster names.
	// +optional
	ExcludeClusters []string `json:"excludeClusters,omitempty"`
	// CountryProfiles assigns risk tiers and confidence overrides per geography.
	// +optional
	CountryProfiles []CountryRiskProfile `json:"countryProfiles,omitempty"`
}

// CountryRiskProfile assigns a risk tier to a set of countries.
type CountryRiskProfile struct {
	// Countries is a list of ISO 3166-1 alpha-2 country codes.
	Countries []string `json:"countries"`
	// RiskTier classifies the regulatory/operational risk.
	// +kubebuilder:validation:Enum=low;medium;high;critical
	RiskTier string `json:"riskTier"`
	// MinConfidence overrides the base threshold for these countries.
	// Effective threshold is max(base, this).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	MinConfidence float64 `json:"minConfidence"`
	// Mode overrides the agent mode for these countries.
	// +optional
	Mode *AgentPolicyMode `json:"mode,omitempty"`
	// RequireHumanCosign requires human Approval in addition to agent decision.
	// +optional
	RequireHumanCosign bool `json:"requireHumanCosign,omitempty"`
}

// AgentConfidencePolicy defines confidence thresholds per scope tier.
type AgentConfidencePolicy struct {
	// Default is the baseline confidence threshold.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Default float64 `json:"default"`
	// TierOverrides sets thresholds keyed by kapro.io/tier label value.
	// +optional
	TierOverrides map[string]float64 `json:"tierOverrides,omitempty"`
	// StageOverrides sets thresholds keyed by stage name.
	// +optional
	StageOverrides map[string]float64 `json:"stageOverrides,omitempty"`
}

// AgentEscalationPolicy controls behavior when confidence is insufficient.
type AgentEscalationPolicy struct {
	// Action is the default escalation behavior.
	// +kubebuilder:default=hold
	Action EscalationAction `json:"action"`
	// HoldDuration is how long to hold before auto-rejecting.
	// Only used when Action is "hold". Empty means hold indefinitely.
	// +optional
	HoldDuration string `json:"holdDuration,omitempty"`
}

// AgentRateLimits caps the agent's throughput.
type AgentRateLimits struct {
	// MaxApprovalsPerHour is the maximum approve decisions per hour.
	// +optional
	MaxApprovalsPerHour int32 `json:"maxApprovalsPerHour,omitempty"`
	// MaxApprovalsPerDay is the maximum approve decisions per day.
	// +optional
	MaxApprovalsPerDay int32 `json:"maxApprovalsPerDay,omitempty"`
	// MaxConcurrent is the maximum in-flight approvals at any time.
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`
	// Cooldown is the minimum duration between consecutive approvals.
	// +optional
	Cooldown string `json:"cooldown,omitempty"`
}

// AgentBlastRadius caps the fleet impact of agent decisions.
type AgentBlastRadius struct {
	// MaxPercentOfFleet is the maximum percentage of total clusters
	// the agent may approve in a single Release.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	MaxPercentOfFleet int32 `json:"maxPercentOfFleet,omitempty"`
	// MaxPercentPerTier caps per-tier, keyed by kapro.io/tier label.
	// +optional
	MaxPercentPerTier map[string]int32 `json:"maxPercentPerTier,omitempty"`
	// MaxAbsoluteClusters is the hard cap regardless of percentages.
	// +optional
	MaxAbsoluteClusters int32 `json:"maxAbsoluteClusters,omitempty"`
}

// AgentAuditRequirements defines what the agent must provide with each decision.
type AgentAuditRequirements struct {
	// RequireReasoning mandates human-readable reasoning.
	// +kubebuilder:default=true
	RequireReasoning bool `json:"requireReasoning"`
	// RequireMetricReferences mandates the reasoning reference specific metrics.
	// +optional
	RequireMetricReferences bool `json:"requireMetricReferences,omitempty"`
	// RequireConfidenceScore mandates a numeric confidence score.
	// +kubebuilder:default=true
	RequireConfidenceScore bool `json:"requireConfidenceScore"`
	// MinReasoningLength is the minimum character count for reasoning.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=50
	// +optional
	MinReasoningLength int32 `json:"minReasoningLength,omitempty"`
}

// AgentTimeWindow restricts when the agent may issue decisions.
type AgentTimeWindow struct {
	// Name identifies this window for audit purposes.
	Name string `json:"name"`
	// Timezone is an IANA timezone string.
	// +kubebuilder:default="UTC"
	Timezone string `json:"timezone,omitempty"`
	// DaysOfWeek restricts to specific days. Empty means all days.
	// +optional
	DaysOfWeek []string `json:"daysOfWeek,omitempty"`
	// StartTime is the daily start time in HH:MM format (24h).
	StartTime string `json:"startTime"`
	// EndTime is the daily end time in HH:MM format (24h).
	EndTime string `json:"endTime"`
	// Deny inverts the window: the agent is BLOCKED during this window.
	// +optional
	Deny bool `json:"deny,omitempty"`
}

// AgentPolicyStatus is the observed state of the AgentPolicy.
type AgentPolicyStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	// ActiveDecisions is the count of in-flight decisions by this agent.
	ActiveDecisions int32 `json:"activeDecisions,omitempty"`
	// DecisionsToday is the count of decisions made in the current UTC day.
	DecisionsToday int32 `json:"decisionsToday,omitempty"`
	// LastDecisionAt is the timestamp of the last decision.
	// +optional
	LastDecisionAt string `json:"lastDecisionAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=agp,categories=kapro-all
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="SA",type=string,JSONPath=`.spec.identity.serviceAccountName`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeDecisions`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentPolicy defines the trust boundary, scope, and guardrails for one AI
// agent identity within the Kapro progressive delivery system.
type AgentPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AgentPolicySpec   `json:"spec,omitempty"`
	Status            AgentPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AgentPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentPolicy `json:"items"`
}

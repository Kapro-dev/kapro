// Package v1alpha1 contains the Kapro API types.
// +groupName=kapro.io
package v1alpha1

import (
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

// ---- Artifact ---------------------------------------------------------------

// ArtifactSpec defines the desired state of Artifact.
type ArtifactSpec struct {
	Sources  []ArtifactSource `json:"sources"`
	Metadata ArtifactMeta     `json:"metadata,omitempty"`
}

type ArtifactSource struct {
	Type string  `json:"type"` // oci
	OCI  *OCIRef `json:"oci,omitempty"`
}

type OCIRef struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

type ArtifactMeta struct {
	ReleasedBy  string `json:"releasedBy,omitempty"`
	Description string `json:"description,omitempty"`
	// DerivedFrom is the name of the parent Artifact this was derived from.
	// Set by CI for hotfix bundles that replace only a subset of components.
	// +optional
	DerivedFrom string `json:"derivedFrom,omitempty"`
	// ChangedComponents lists the app components that changed relative to the
	// parent artifact (when DerivedFrom is set).
	// +optional
	ChangedComponents []string `json:"changedComponents,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=art,categories=kapro-all
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Artifact is an immutable OCI bundle, digest-pinned, created by CI.
type Artifact struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArtifactSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type ArtifactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Artifact `json:"items"`
}

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

// ProviderSpec selects the cluster connectivity backend for this Target.
//
// # Two-path model (see ADR-006, ADR-007)
//
// Path A (default): set Type to "" or "crd". The kapro-cluster-controller agent
// on the spoke cluster writes MemberCluster heartbeats outbound to the hub.
// No hub→spoke network required. Works on all clouds and air-gapped fleets.
//
// Path B (v0.3+): set Type to the cloud identifier and populate the matching
// provider spec block. The hub connects directly to the spoke API server using
// cloud IAM (Workload Identity, IRSA, Managed Identity). No cluster-controller
// agent needed on the spoke.
//
// Security invariant: credentials NEVER appear in CRD fields. Cloud-specific
// specs reference Secrets by name only. Keyless IAM is strongly preferred.
//
// +kubebuilder:validation:Optional
// +kubebuilder:validation:XValidation:rule="self.type != 'gke' || has(self.gke)",message="provider.gke must be set when type is gke"
type ProviderSpec struct {
	// Type selects the connectivity backend.
	//
	// ""  or "crd"    → CRD outbound path (default, all clouds, air-gap)
	// "gke"           → GKE Workload Identity + Connect Gateway (v0.3)
	// "aks"           → AKS Managed Identity + AAD OIDC federation (v0.4)
	// "digitalocean"  → DigitalOcean API token in Secret (v0.4)
	// "stackit"       → StackIT Service Account key in Secret (v0.4)
	//
	// +kubebuilder:validation:Enum="";crd;gke;aks;digitalocean;stackit
	// +optional
	Type string `json:"type,omitempty"`

	// GKE configures GKE Workload Identity direct-connect (type: gke, v0.3).
	// +optional
	GKE *GKEProviderSpec `json:"gke,omitempty"`

	// AKS configures AKS Managed Identity direct-connect (type: aks, v0.4).
	// +optional
	AKS *AKSProviderSpec `json:"aks,omitempty"`

	// DigitalOcean configures DOKS direct-connect (type: digitalocean, v0.4).
	// +optional
	DigitalOcean *DigitalOceanProviderSpec `json:"digitalOcean,omitempty"`

	// StackIT configures SKE direct-connect (type: stackit, v0.4).
	// +optional
	StackIT *StackITProviderSpec `json:"stackit,omitempty"`
}

// GKEProviderSpec configures KCI direct-connect to a GKE cluster via
// Workload Identity Federation.
//
// The hub Kubernetes ServiceAccount must be annotated with
// iam.gke.io/gcp-service-account pointing to a GCP SA bound to
// roles/container.clusterViewer on the cluster. No static credentials.
type GKEProviderSpec struct {
	// Project is the GCP project ID (not project number).
	// +kubebuilder:validation:Required
	Project string `json:"project"`
	// Location is the cluster region (e.g. europe-west1) or zone (e.g. europe-west1-b).
	// +kubebuilder:validation:Required
	Location string `json:"location"`
	// ClusterName is the GKE cluster name as shown in the GCP console.
	// +kubebuilder:validation:Required
	ClusterName string `json:"clusterName"`
	// WorkloadIdentityPool is the GCP Workload Identity pool for this hub.
	// Format: PROJECT_ID.svc.id.goog
	// +optional
	WorkloadIdentityPool string `json:"workloadIdentityPool,omitempty"`
	// ServiceAccountRef names the hub K8s SA annotated with
	// iam.gke.io/gcp-service-account. Defaults to the kapro-operator SA.
	// +optional
	ServiceAccountRef string `json:"serviceAccountRef,omitempty"`
}

// AKSProviderSpec configures KCI direct-connect to an AKS cluster via
// Azure Managed Identity and AAD OIDC federation.
//
// The hub pod uses its Managed Identity to call the Azure Resource Manager API
// and obtain a short-lived AAD token kubeconfig. No client secrets stored.
type AKSProviderSpec struct {
	// SubscriptionID is the Azure subscription UUID.
	// +kubebuilder:validation:Required
	SubscriptionID string `json:"subscriptionID"`
	// ResourceGroup is the Azure resource group containing the AKS cluster.
	// +kubebuilder:validation:Required
	ResourceGroup string `json:"resourceGroup"`
	// ClusterName is the AKS cluster name.
	// +kubebuilder:validation:Required
	ClusterName string `json:"clusterName"`
	// ClientID is the Managed Identity client ID. Uses system-assigned identity when empty.
	// +optional
	ClientID string `json:"clientID,omitempty"`
	// TenantID is the Azure AD tenant ID.
	// +optional
	TenantID string `json:"tenantID,omitempty"`
}

// DigitalOceanProviderSpec configures KCI direct-connect to a DOKS cluster
// via the DigitalOcean API v2.
//
// The connector reads a DigitalOcean API token from the referenced Secret
// and calls GET /v2/kubernetes/clusters/{id}/kubeconfig to obtain a
// time-limited kubeconfig. Plan a token rotation strategy externally.
type DigitalOceanProviderSpec struct {
	// ClusterID is the DigitalOcean Kubernetes cluster UUID.
	// Find it at cloud.digitalocean.com/kubernetes or `doctl kubernetes cluster list`.
	// +kubebuilder:validation:Required
	ClusterID string `json:"clusterID"`
	// TokenSecretRef names a Secret in kapro-system containing the DigitalOcean
	// API token under key "token".
	// +kubebuilder:validation:Required
	TokenSecretRef string `json:"tokenSecretRef"`
	// Region is the DigitalOcean region slug (e.g. nyc1, fra1, ams3).
	// Used for topology metadata only.
	// +optional
	Region string `json:"region,omitempty"`
}

// StackITProviderSpec configures KCI direct-connect to a SKE (STACKIT
// Kubernetes Engine) cluster via the STACKIT API.
//
// STACKIT is a German GDPR-compliant EU-sovereign cloud. When STACKIT adds
// Workload Identity support, ServiceAccountKeySecretRef will become optional.
type StackITProviderSpec struct {
	// ProjectID is the STACKIT project UUID.
	// +kubebuilder:validation:Required
	ProjectID string `json:"projectID"`
	// ClusterName is the SKE cluster name.
	// +kubebuilder:validation:Required
	ClusterName string `json:"clusterName"`
	// Region is the STACKIT region slug (e.g. eu01).
	// +kubebuilder:validation:Required
	Region string `json:"region"`
	// ServiceAccountKeySecretRef names a Secret in kapro-system containing a
	// STACKIT Service Account key JSON under key "key.json".
	// +kubebuilder:validation:Required
	ServiceAccountKeySecretRef string `json:"serviceAccountKeySecretRef"`
}

// ActuatorSpec selects and configures the delivery backend for this cluster.
// +kubebuilder:validation:XValidation:rule="self.type != 'flux' || has(self.flux)",message="actuator.flux must be set when type is flux"
type ActuatorSpec struct {
	// +kubebuilder:validation:Enum=flux
	Type string        `json:"type"`
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
	//   halt (default): stop the Sync and wait for human intervention.
	//     Use for checkout systems where automated rollback is too risky.
	//   rollback: automatically revert to the previous version.
	//     Only effective when a previous successful apply exists (PreviousVersion is set).
	//   continue: mark the gate as skipped and advance to the next phase.
	// +kubebuilder:validation:Enum=halt;rollback;continue
	// +kubebuilder:default=halt
	OnFailure     string             `json:"onFailure,omitempty"`
	Notifications []NotificationSpec `json:"notifications,omitempty"`
}

// ---- GateSpec (embedded in Stage.gate and Sync.spec.gate) -------------------

type GateSpec struct {
	SoakTime string `json:"soakTime,omitempty"`
	// GateTimeout is the maximum duration the metrics gate may remain un-passed
	// before the Sync is failed. Only applies to MetricsCheck; infrastructure
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
	// DependsOn lists stage names that must reach Complete before this stage starts.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []string `json:"dependsOn,omitempty"`
	// Gate is the inline gate policy evaluated after all targets in this
	// stage converge. If nil, the stage advances immediately on convergence.
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
	// Artifact is the OCI artifact name to deliver across the fleet.
	Artifact string `json:"artifact"`
	// Pipelines is the DAG of pipeline nodes that this Release executes.
	// Each node references a Pipeline CRD and may depend on other nodes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Pipelines []ReleasePipelineRef `json:"pipelines"`
	// AppKey is the key used in MemberCluster.status.currentVersions.
	// Defaults to the Artifact name when not set.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// Suspended pauses all advancement when true.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// DerivedFrom is the name of the parent Release this hotfix was derived from.
	// Set by CI for hotfix Releases that target a subset of clusters.
	// Immutable after creation — changing it has no effect on a running rollout.
	// +optional
	DerivedFrom string `json:"derivedFrom,omitempty"`
	// Scope restricts this Release to a subset of clusters.
	// When set, rollout entries are only created for the named target clusters.
	// Nil or empty = full-fleet rollout (normal behaviour).
	// Immutable after creation — set scope before the Release is created.
	// +optional
	Scope *ReleaseScope `json:"scope,omitempty"`
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
	// ResolvedVersion is the OCI digest resolved from the Artifact CR.
	// Format: <repository>@sha256:<digest>. Set once in Pending and never changed.
	ResolvedVersion string `json:"resolvedVersion,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
	// PipelineProgress tracks execution state of each pipeline node in the DAG.
	PipelineProgress []PipelineProgress `json:"pipelineProgress,omitempty"`
	// Targets tracks per-target rollout state. Replaces the Sync CRD.
	Targets []TargetStatus `json:"targets,omitempty"`
	// Report is the inline delivery summary. Replaces the ReleaseReport CRD.
	Report ReleaseReportSummary `json:"report,omitempty"`
	// AuditTrail records immutable delivery provenance. Capped at 50 entries.
	// Replaces the ReleaseRevision CRD.
	AuditTrail []AuditEntry       `json:"auditTrail,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=rel,categories=kapro-all
// +kubebuilder:printcolumn:name="Artifact",type=string,JSONPath=`.spec.artifact`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.report.syncedTargets`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.report.pendingTargets`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Release is the trigger for a progressive delivery rollout across the cluster fleet.
// It references an Artifact and a DAG of Pipelines that define the delivery path.
// The Release controller drives the pipeline DAG, advancing each target cluster
// through the delivery FSM (MetricsCheck → WaitingApproval → Applying → Applied).
// Target rollout state is stored inline in Release.status.targets.
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

// ---- Inline status types (folded from former CRDs) -------------------------

// TargetStatus records the rollout state of one target cluster within a Release.
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
	// Phase is the FSM state of this target rollout.
	Phase      SyncPhase `json:"phase,omitempty"`
	StartedAt  string    `json:"startedAt,omitempty"`
	FinishedAt string    `json:"finishedAt,omitempty"`
	// PhaseEnteredAt records when the current phase was entered (used by gate timeouts).
	PhaseEnteredAt string `json:"phaseEnteredAt,omitempty"`
	Message        string `json:"message,omitempty"`
	// PreviousVersion holds the version before this sync, used for rollback.
	PreviousVersion string `json:"previousVersion,omitempty"`
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
}

// ReleaseReportSummary is the inline delivery summary stored in
// Release.status.report. It replaces the standalone ReleaseReport CRD.
type ReleaseReportSummary struct {
	Phase             ReleasePhase   `json:"phase,omitempty"`
	Artifact          string         `json:"artifact,omitempty"`
	ResolvedVersion   string         `json:"resolvedVersion,omitempty"`
	StartedAt         string         `json:"startedAt,omitempty"`
	CompletedAt       string         `json:"completedAt,omitempty"`
	Duration          string         `json:"duration,omitempty"`
	TotalTargets      int            `json:"totalTargets,omitempty"`
	SyncedTargets     int            `json:"syncedTargets,omitempty"`
	FailedTargets     int            `json:"failedTargets,omitempty"`
	PendingTargets    int            `json:"pendingTargets,omitempty"`
	RolledBackTargets int            `json:"rolledBackTargets,omitempty"`
	Targets           []TargetReport `json:"targets,omitempty"`
	Gates             []GateReport   `json:"gates,omitempty"`
	PendingApprovals  []string       `json:"pendingApprovals,omitempty"`
}

// AuditEntry records the immutable delivery provenance of a completed Release.
// It replaces the standalone ReleaseRevision CRD. Stored in Release.status.auditTrail.
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

// SyncPhase is the execution state of one target cluster rollout within a Release.
// +kubebuilder:validation:Enum=Pending;Verification;HealthCheck;Soaking;MetricsCheck;WaitingApproval;Applying;Converged;Failed
type SyncPhase string

const (
	SyncPhasePending         SyncPhase = "Pending"
	SyncPhaseVerification    SyncPhase = "Verification"
	SyncPhaseHealthCheck     SyncPhase = "HealthCheck"
	SyncPhaseSoaking         SyncPhase = "Soaking"
	SyncPhaseMetricsCheck    SyncPhase = "MetricsCheck"
	SyncPhaseWaitingApproval SyncPhase = "WaitingApproval"
	SyncPhaseApplying        SyncPhase = "Applying"
	SyncPhaseConverged       SyncPhase = "Converged"
	SyncPhaseFailed          SyncPhase = "Failed"
)

// ---- Report helper types ----------------------------------------------------

// TargetReport is a per-target delivery summary within a ReleaseReportSummary.
type TargetReport struct {
	Name        string `json:"name"`
	PipelineRef string `json:"pipelineRef,omitempty"` // logical pipeline instance name in Release.spec.pipelines
	Stage       string `json:"stage,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Version     string `json:"version,omitempty"`
	SyncedAt    string `json:"syncedAt,omitempty"`
	Message     string `json:"message,omitempty"`
}

// GateReport is a summary of one gate evaluation within a ReleaseReport.
type GateReport struct {
	Type        string `json:"type"`
	PipelineRef string `json:"pipelineRef,omitempty"` // logical pipeline instance name
	Stage       string `json:"stage,omitempty"`
	Target      string `json:"target,omitempty"`
	Result      string `json:"result"`
	Message     string `json:"message,omitempty"`
}

// ---- Approval ---------------------------------------------------------------

type ApprovalKind string

const (
	ApprovalKindSync  ApprovalKind = "Sync"
	ApprovalKindStage ApprovalKind = "Stage"
)

type ApprovalSpec struct {
	// +kubebuilder:validation:Enum=Sync;Stage
	Kind       ApprovalKind `json:"kind"`
	Ref        string       `json:"ref"`
	Release    string       `json:"release"`
	Target     string       `json:"target,omitempty"`
	ApprovedBy string       `json:"approvedBy"`
	Bypass     bool         `json:"bypass,omitempty"`
	Comment    string       `json:"comment,omitempty"`
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
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.spec.release`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Recorded",type=string,JSONPath=`.status.conditions[?(@.type=="Recorded")].status`
// +kubebuilder:printcolumn:name="Ref",type=string,JSONPath=`.spec.ref`
// +kubebuilder:printcolumn:name="Approved By",type=string,JSONPath=`.spec.approvedBy`
// +kubebuilder:printcolumn:name="Bypass",type=boolean,JSONPath=`.spec.bypass`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Approval is a human gate signal to unblock a Sync or Stage.
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
// MemberCluster is the lean replacement for legacy target inventory split.
// One object per physical cluster in the Kapro fleet.
//
// Ownership split:
//   - spec (except desiredVersion/desiredAppKey): written by the platform team
//   - spec.desiredVersion, spec.desiredAppKey: written by the release/sync controller
//   - status: written by the cluster-controller (kapro-cluster-controller on the spoke)
//   - status.bootstrap: written by the hub csrapproval controller during registration

// MemberClusterSpec defines the desired state of a cluster in the Kapro fleet.
type MemberClusterSpec struct {
	// Actuator configures the delivery backend for this cluster.
	Actuator ActuatorSpec `json:"actuator"`

	// HealthCheck configures active health polling for this cluster.
	// +optional
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`

	// Provider configures how Kapro discovers and connects to the workload cluster.
	// +optional
	Provider *ProviderSpec `json:"provider,omitempty"`

	// Topology holds hardware and scheduling metadata used by Pipeline stage selectors.
	// +optional
	Topology *TargetTopology `json:"topology,omitempty"`

	// DesiredVersion is written by the kapro-operator (release/sync controller).
	// The cluster-controller polls this field and patches the local delivery system.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// DesiredAppKey is the key the cluster-controller uses when writing
	// status.currentVersions. Defaults to "default".
	// +optional
	DesiredAppKey string `json:"desiredAppKey,omitempty"`

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

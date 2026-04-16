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
	// BootstrapTokenFinalizer is added to BootstrapToken objects to allow RBAC cleanup on deletion.
	BootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer"
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
	// +kubebuilder:validation:Enum=flux;argocd;sveltos;ocm
	Type string       `json:"type"`
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

// ClusterRegistrationSpec defines the desired state for a registered workload cluster.
// spec.desiredVersion is written by the kapro-operator; all other spec fields are
// written once by the cluster-controller at bootstrap time and never changed.
type ClusterRegistrationSpec struct {
	// EnvironmentRef names the Environment CRD this cluster belongs to.
	EnvironmentRef string `json:"environmentRef"`

	// ControllerVersion is the version of kapro-cluster-controller running on this cluster.
	ControllerVersion string `json:"controllerVersion,omitempty"`

	// DesiredVersion is written by the kapro-operator (via ActuatorPlugin.Apply).
	// cluster-controller polls this field; on change, patches the local delivery system
	// (OCIRepository tag for Flux, Application revision for ArgoCD, etc.)
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// Capabilities is written by cluster-controller at registration time.
	// Declares which delivery systems are available on this cluster.
	// +optional
	Capabilities ClusterCapabilities `json:"capabilities,omitempty"`
}

// ClusterCapabilities describes what delivery systems and K8s capabilities are
// available on a registered cluster. Used by the operator to select the right actuator.
type ClusterCapabilities struct {
	// K8sVersion is the Kubernetes server version (e.g. "v1.30.2").
	K8sVersion string `json:"k8sVersion,omitempty"`
	// FluxVersion is the installed Flux version, or empty if Flux is not present.
	FluxVersion string `json:"fluxVersion,omitempty"`
	// ArgoCDVersion is the installed ArgoCD version, or empty if ArgoCD is not present.
	ArgoCDVersion string `json:"argoCDVersion,omitempty"`
	// SveltosVersion is the installed Sveltos version, or empty if Sveltos is not present.
	SveltosVersion string `json:"sveltosVersion,omitempty"`
	// NodeCount is the number of nodes in the cluster at registration time.
	NodeCount int `json:"nodeCount,omitempty"`
	// Region is the cloud region or datacenter label for this cluster.
	Region string `json:"region,omitempty"`
}

// ClusterRegistrationStatus is the fleet registry entry — written by kapro-cluster-controller.
// This is the single authoritative source of truth for "what is running where."
// Think of it as a Kubernetes Node object but for clusters, not nodes.
type ClusterRegistrationStatus struct {
	// CurrentVersions is a map of component → deployed version.
	// e.g. {"ocs": "v1.2.4", "keycloak": "24.0.1"}
	// Populated by cluster-controller from the local delivery system's status.
	// +optional
	CurrentVersions map[string]string `json:"currentVersions,omitempty"`

	// DeliverySystem reports which actuator is managing this cluster.
	// Written by cluster-controller based on what delivery system it detects locally.
	// +kubebuilder:validation:Enum=flux;argocd;sveltos;ocm;helm
	DeliverySystem string `json:"deliverySystem,omitempty"`

	// Health is the aggregated workload health from the local delivery system.
	Health ClusterHealth `json:"health,omitempty"`

	// ActiveRelease is the name of the Release currently being applied to this cluster.
	// Set by kapro-operator when a Promotion transitions to Applying.
	// +optional
	ActiveRelease string `json:"activeRelease,omitempty"`

	// LastHeartbeat is the RFC3339 timestamp of the last cluster-controller write.
	// Used by CRDProvider.IsReachable() to determine if the cluster is still live.
	LastHeartbeat string `json:"lastHeartbeat,omitempty"`

	// Phase is the current convergence state of this cluster.
	Phase ClusterPhase `json:"phase,omitempty"`

	// Conditions follow KEP-1623: Ready, Synced, Degraded, Unreachable.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterHealth aggregates workload health from the local delivery system.
type ClusterHealth struct {
	// AllWorkloadsReady is true when every tracked workload is in a ready state.
	AllWorkloadsReady bool `json:"allWorkloadsReady"`
	// ReadyWorkloads is the count of workloads in Ready state.
	ReadyWorkloads int `json:"readyWorkloads"`
	// FailedWorkloads is the count of workloads in a failed/degraded state.
	FailedWorkloads int `json:"failedWorkloads"`
	// TotalWorkloads is the total count of tracked workloads.
	TotalWorkloads int `json:"totalWorkloads"`
	// Message is a human-readable summary from the delivery system.
	// +optional
	Message string `json:"message,omitempty"`
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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environmentRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Delivery",type=string,JSONPath=`.status.deliverySystem`
// +kubebuilder:printcolumn:name="Healthy",type=boolean,JSONPath=`.status.health.allWorkloadsReady`
// +kubebuilder:printcolumn:name="Active Release",type=string,JSONPath=`.status.activeRelease`
// +kubebuilder:printcolumn:name="Heartbeat",type=string,JSONPath=`.status.lastHeartbeat`

// ClusterRegistration is the fleet registry entry for a workload cluster.
// One object per cluster, cluster-scoped on the Kapro control plane.
// The control plane writes spec.desiredVersion; cluster-controller writes status.
// Together they form the canonical read model for "what is running where."
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
	// +kubebuilder:validation:Enum=auto;manual;scheduled
	Mode     PromotionMode   `json:"mode"`
	Gate     GateSpec        `json:"gate,omitempty"`
	Approval *ApprovalConfig `json:"approval,omitempty"`
	// +kubebuilder:validation:Enum=halt;rollback;continue
	OnFailure string         `json:"onFailure,omitempty"`
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
	// +kubebuilder:validation:Enum=Promotion;Batch
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
	Phase           PromotionPhase     `json:"phase,omitempty"`
	StartedAt       string             `json:"startedAt,omitempty"`
	FinishedAt      string             `json:"finishedAt,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
	Message         string             `json:"message,omitempty"`
	// PreviousVersion holds the version before this promotion, used for rollback.
	PreviousVersion string             `json:"previousVersion,omitempty"`
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

// ---- BootstrapToken ---------------------------------------------------------

// BootstrapTokenSpec defines a one-time token used by a workload cluster to
// self-register with the Kapro control plane.
// Inspired by: Kubernetes TLS bootstrap tokens, OCM klusterlet CSR bootstrap.
// Security model:
//   - Token is stored as SHA-256 hash only — plaintext never persisted
//   - 24h TTL enforced by expiresAt
//   - One-time use: status.used=true is set atomically on first use
//   - After use, the issued credential (SA token) rotates every hour via TokenRequest API
type BootstrapTokenSpec struct {
	// ClusterName is the name of the ClusterRegistration that will be created.
	ClusterName string `json:"clusterName"`

	// TokenHash is the SHA-256 hex digest of the raw bootstrap token.
	// The plaintext token is NEVER stored. Only this hash is stored for validation.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	TokenHash string `json:"tokenHash"`

	// ExpiresAt is the time after which this token is no longer valid.
	ExpiresAt metav1.Time `json:"expiresAt"`

	// Labels are the labels to apply to the created ClusterRegistration.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// BootstrapTokenStatus tracks the one-time-use state of a bootstrap token.
type BootstrapTokenStatus struct {
	// Used is set to true when the token has been consumed by a cluster-controller.
	Used bool `json:"used"`

	// UsedAt is the timestamp when the token was consumed.
	// +optional
	UsedAt *metav1.Time `json:"usedAt,omitempty"`

	// IssuedCredentialFor is the ServiceAccount name created for this cluster.
	// +optional
	IssuedCredentialFor string `json:"issuedCredentialFor,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Used",type=boolean,JSONPath=`.status.used`
// +kubebuilder:printcolumn:name="Expires",type=string,JSONPath=`.spec.expiresAt`

// BootstrapToken is a one-time credential that allows a workload cluster's
// cluster-controller to self-register with the Kapro control plane.
// Created by `kapro cluster bootstrap`. Consumed once; then auto-deleted.
type BootstrapToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BootstrapTokenSpec   `json:"spec,omitempty"`
	Status            BootstrapTokenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BootstrapTokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BootstrapToken `json:"items"`
}

// ---- PluginRegistration -----------------------------------------------------

// PluginType identifies which PCL interface this plugin implements.
// +kubebuilder:validation:Enum=Actuator;Provider;Gate
type PluginType string

const (
	PluginTypeActuator PluginType = "Actuator"
	PluginTypeProvider PluginType = "Provider"
	PluginTypeGate     PluginType = "Gate"
)

// PluginRegistrationSpec defines how to locate and connect to a Kapro plugin.
// Plugins implement one of: ActuatorService, ProviderService, GateService (gRPC).
type PluginRegistrationSpec struct {
	// Type declares which PCL interface this plugin implements.
	Type PluginType `json:"type"`

	// SocketPath is the path to the Unix domain socket for local (sidecar) plugins.
	// Convention: /var/run/kapro-plugins/<plugin-name>.sock
	// Mutually exclusive with Endpoint.
	// +optional
	SocketPath string `json:"socketPath,omitempty"`

	// Endpoint is the gRPC endpoint for remote plugins (e.g. grpc://host:port).
	// Used for plugins that run as a separate service (not a sidecar).
	// Mutually exclusive with SocketPath.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// TLS configures mTLS for remote endpoint plugins.
	// +optional
	TLS *PluginTLSConfig `json:"tls,omitempty"`

	// HealthCheck configures how the operator probes plugin liveness.
	// +optional
	HealthCheck *PluginHealthCheck `json:"healthCheck,omitempty"`

	// Metadata is informational metadata about the plugin.
	// +optional
	Metadata PluginMeta `json:"metadata,omitempty"`
}

// PluginTLSConfig specifies mTLS configuration for remote plugin endpoints.
type PluginTLSConfig struct {
	// SecretRef names a K8s Secret with keys: tls.crt, tls.key, ca.crt
	SecretRef string `json:"secretRef"`
}

// PluginHealthCheck configures liveness probing for a plugin.
type PluginHealthCheck struct {
	// IntervalSeconds is how often to probe the plugin's gRPC health service.
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
	// TimeoutSeconds is the probe timeout.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// PluginMeta contains informational metadata about a plugin.
type PluginMeta struct {
	Vendor      string `json:"vendor,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
}

// PluginRegistrationStatus is written by the operator after it connects to the plugin.
type PluginRegistrationStatus struct {
	// Connected is true when the operator has established a gRPC connection.
	Connected bool `json:"connected"`
	// LastPing is the RFC3339 timestamp of the last successful health check.
	// +optional
	LastPing string `json:"lastPing,omitempty"`
	// Capabilities lists the capabilities reported by the plugin via GetCapabilities RPC.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
	// Message is a human-readable status or error message.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Connected",type=boolean,JSONPath=`.status.connected`
// +kubebuilder:printcolumn:name="Last Ping",type=string,JSONPath=`.status.lastPing`

// PluginRegistration registers an external gRPC plugin with the Kapro operator.
// The plugin implements one of: ActuatorService, ProviderService, GateService.
// See: proto/actuator.proto, proto/provider.proto, proto/gate.proto
type PluginRegistration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PluginRegistrationSpec   `json:"spec,omitempty"`
	Status            PluginRegistrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PluginRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PluginRegistration `json:"items"`
}

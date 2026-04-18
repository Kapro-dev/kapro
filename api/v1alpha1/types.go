// Package v1alpha1 contains the Kapro API types.
// +groupName=kapro.io
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finalizer constants — prevents premature deletion of stateful resources.
const (
	// ReleaseFinalizer is added to Release objects to allow cleanup of Promotions and BatchRuns.
	ReleaseFinalizer = "kapro.io/release-finalizer"
	// BatchRunFinalizer is added to BatchRun objects to allow cleanup of in-progress cluster applies.
	BatchRunFinalizer = "kapro.io/batchrun-finalizer"
	// BootstrapTokenFinalizer is added to BootstrapToken objects to allow RBAC cleanup on deletion.
	BootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer" //nolint:gosec // not a credential — it's a Kubernetes finalizer annotation key
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
	Actuator    ActuatorSpec     `json:"actuator"`
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`
	// Provider configures how Kapro discovers and connects to the workload cluster.
	Provider *ProviderSpec `json:"provider,omitempty"`
	// Topology holds hardware and scheduling metadata for this Environment.
	// Used by Pipeline batch selectors for GPU-aware promotion waves.
	// +optional
	Topology *EnvironmentTopology `json:"topology,omitempty"`
}

// EnvironmentTopology holds hardware and scheduling metadata for an Environment.
// Labels on the Environment object are the primary selector mechanism;
// Topology provides structured machine-readable fields for GPU-aware batching.
type EnvironmentTopology struct {
	// Accelerator is the GPU/accelerator type in this cluster.
	// Well-known values: nvidia-h100, nvidia-a100, nvidia-l40s, amd-mi300x, tpu-v5e.
	// Matches the node label kubernetes.io/accelerator or cloud-provider equivalents.
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
	// Tier classifies the cluster's role in the promotion wave.
	// Well-known values: canary, shadow, prod.
	// +optional
	Tier string `json:"tier,omitempty"`
}

// ProviderSpec selects the cluster-discovery/connectivity backend.
type ProviderSpec struct {
	// CAPI configures the Cluster API topology provider.
	CAPI *CAPIProviderSpec `json:"capi,omitempty"`
	// OCM configures the Open Cluster Management (hub-spoke) topology provider.
	OCM *OCMProviderSpec `json:"ocm,omitempty"`
	// OpenShift configures the Red Hat ACM / Hypershift topology provider.
	// Discovers OpenShift clusters registered in an ACM hub, including
	// Hypershift hosted control planes.
	OpenShift *OpenShiftProviderSpec `json:"openshift,omitempty"`
	// Rancher configures the Rancher Norman API provider.
	// Requires a PluginRegistration with name "rancher" to be connected.
	Rancher *RancherProviderSpec `json:"rancher,omitempty"`
}

// RancherProviderSpec configures the Rancher cluster provider plugin.
type RancherProviderSpec struct {
	// ServerURL is the Rancher server base URL, e.g. "https://rancher.example.com".
	ServerURL string `json:"serverURL"`
	// ClusterID is the Rancher cluster ID, e.g. "c-m-abc123".
	ClusterID string `json:"clusterID"`
	// TokenSecretRef is the name of a Secret in the same namespace as the
	// operator, containing key "token" with a Rancher API bearer token.
	TokenSecretRef string `json:"tokenSecretRef"`
}

// CAPIProviderSpec configures the Cluster API provider.
type CAPIProviderSpec struct {
	// ManagementClusterSecretRef is the name of the Secret holding the
	// management cluster kubeconfig (key: "value").
	ManagementClusterSecretRef string `json:"managementClusterSecretRef,omitempty"`
	// Namespace to watch for CAPI Cluster resources.
	// Defaults to the Environment's own namespace when empty.
	Namespace string `json:"namespace,omitempty"`
	// ClusterName is the CAPI Cluster name for direct targeting.
	// When set, SyncEnvironments only considers this specific cluster.
	ClusterName string `json:"clusterName,omitempty"`
}

// OCMProviderSpec configures the Open Cluster Management provider.
// OCM uses a hub-spoke architecture: the hub cluster holds ManagedCluster resources
// that represent each spoke cluster.
type OCMProviderSpec struct {
	// HubSecretRef is the name of the Secret (in the same namespace) holding
	// the OCM hub cluster kubeconfig (key: "value").
	HubSecretRef string `json:"hubSecretRef,omitempty"`
	// ClusterName is the OCM ManagedCluster name (spoke cluster).
	ClusterName string `json:"clusterName,omitempty"`
	// Namespace on the hub where the per-cluster kubeconfig Secret lives.
	// Defaults to ClusterName when empty (OCM convention).
	Namespace string `json:"namespace,omitempty"`
}

// OpenShiftProviderSpec configures the Red Hat ACM / Hypershift topology provider.
// ACM (Advanced Cluster Management) is the upstream OpenShift distribution of OCM.
// It uses the same ManagedCluster CRD (cluster.open-cluster-management.io/v1) but
// adds OpenShift-specific labels, cluster claims, and Hypershift hosted clusters.
type OpenShiftProviderSpec struct {
	// HubSecretRef is the name of the Secret (in the same namespace) holding
	// the ACM hub cluster kubeconfig (key: "value").
	HubSecretRef string `json:"hubSecretRef,omitempty"`
	// ClusterName is the ACM ManagedCluster name (spoke cluster).
	// When empty, defaults to the Environment name.
	ClusterName string `json:"clusterName,omitempty"`
	// Namespace on the hub where the per-cluster kubeconfig Secret lives.
	// Defaults to ClusterName when empty (ACM convention).
	Namespace string `json:"namespace,omitempty"`
	// HostedCluster marks this Environment as a Hypershift hosted control plane.
	// When true, the provider reads the HostedCluster CRD for API endpoint info
	// instead of the standard ManagedCluster kubeconfig secret.
	HostedCluster bool `json:"hostedCluster,omitempty"`
	// HostedClusterNamespace is the namespace on the management cluster where
	// HostedCluster resources live. Defaults to "clusters".
	HostedClusterNamespace string `json:"hostedClusterNamespace,omitempty"`
}

type ActuatorSpec struct {
	// +kubebuilder:validation:Enum=flux;argocd;sveltos;ocm;kserve
	Type   string              `json:"type"`
	Flux   *FluxActuator       `json:"flux,omitempty"`
	KServe *KServeActuatorSpec `json:"kserve,omitempty"`
}

type FluxActuator struct {
	Namespace         string `json:"namespace"`
	OCIRepository     string `json:"ociRepository"`
	KustomizationPath string `json:"kustomizationPath"`
}

// KServeActuatorSpec configures promotion of KServe InferenceService resources.
// Used for AI/ML model progressive delivery across clusters.
type KServeActuatorSpec struct {
	// Namespace is the Kubernetes namespace where the InferenceService lives.
	Namespace string `json:"namespace,omitempty"`
	// InferenceServiceName is the name of the KServe InferenceService to update.
	InferenceServiceName string `json:"inferenceServiceName"`
	// StorageURITemplate is a template for the model storage URI.
	// Use {{.Version}} as the version placeholder.
	// Example: "oci://registry.example.io/models/retail-llm:{{.Version}}"
	// When empty, the version string is used as-is.
	StorageURITemplate string `json:"storageURITemplate,omitempty"`
}

type HealthCheckSpec struct {
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}

// EnvironmentStatus defines the observed state of Environment.
type EnvironmentStatus struct {
	// ObservedGeneration is the .metadata.generation this status was derived from.
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	ActiveRelease      string `json:"activeRelease,omitempty"`
	Phase              string `json:"phase,omitempty"`
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

	// DesiredAppKey is the key the cluster-controller uses when writing
	// ClusterRegistration.status.currentVersions. Defaults to "default" when unset.
	// The kapro-operator sets this from Promotion.spec.appKey so convergence checks
	// and rollback version lookups are consistent.
	// +optional
	DesiredAppKey string `json:"desiredAppKey,omitempty"`

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
	// ObservedGeneration is the .metadata.generation this status was derived from.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

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
	OnFailure     string             `json:"onFailure,omitempty"`
	Notifications []NotificationSpec `json:"notifications,omitempty"`
}

type GateSpec struct {
	SoakTime    string            `json:"soakTime,omitempty"`
	HealthCheck bool              `json:"healthCheck,omitempty"`
	Metrics     []MetricGate      `json:"metrics,omitempty"`
	// Templates declares GateTemplates to evaluate (new path, AND semantics).
	// When set alongside Metrics, Templates are evaluated after Metrics pass.
	Templates []GateTemplateRef `json:"templates,omitempty"`
	// Verification configures artifact signature verification via cosign.
	// When set, the VerificationGate uses this policy instead of the
	// operator-level default (keyless with Sigstore public infrastructure).
	Verification *VerificationGateSpec `json:"verification,omitempty"`
}

// VerificationGateSpec configures per-policy artifact signature verification.
type VerificationGateSpec struct {
	// CosignPolicy selects the verification method (keyless OIDC or static key).
	// Exactly one of Keyless or Key should be set.
	CosignPolicy *CosignPolicySpec `json:"cosignPolicy,omitempty"`
}

// CosignPolicySpec specifies how cosign should verify the artifact signature.
// Exactly one of Keyless or Key must be set.
type CosignPolicySpec struct {
	// Keyless configures OIDC-based (Fulcio) keyless verification.
	// Uses Sigstore's public transparency log (Rekor) by default.
	Keyless *KeylessVerificationSpec `json:"keyless,omitempty"`
	// Key configures verification using a static cosign public key stored in a Secret.
	Key *KeyVerificationSpec `json:"key,omitempty"`
}

// KeylessVerificationSpec configures OIDC keyless cosign verification.
type KeylessVerificationSpec struct {
	// Issuer is the OIDC token issuer expected in the Fulcio certificate.
	// Example: "https://token.actions.githubusercontent.com"
	Issuer string `json:"issuer,omitempty"`
	// Subject is the OIDC subject identity (e.g. the GitHub workflow ref URI).
	// Example: "https://github.com/org/repo/.github/workflows/release.yml@refs/heads/main"
	Subject string `json:"subject,omitempty"`
	// RekorURL is the transparency log base URL.
	// Defaults to the Sigstore public instance (https://rekor.sigstore.dev).
	RekorURL string `json:"rekorURL,omitempty"`
}

// KeyVerificationSpec configures static public key cosign verification.
type KeyVerificationSpec struct {
	// SecretRef references a Secret in the operator namespace (kapro-system by default)
	// that contains the cosign public key.
	// PromotionPolicy is cluster-scoped; the Secret namespace must be explicitly provided.
	SecretRef CosignKeySecretRef `json:"secretRef"`
}

// CosignKeySecretRef identifies a cosign public key stored in a Kubernetes Secret.
type CosignKeySecretRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Namespace where the Secret lives.
	// Defaults to the Kapro operator namespace (kapro-system).
	// +kubebuilder:default=kapro-system
	Namespace string `json:"namespace,omitempty"`
	// Key is the data key within the Secret that holds the PEM-encoded public key.
	// +kubebuilder:default=cosign.pub
	Key string `json:"key,omitempty"`
}

// GateTemplateRef references a GateTemplate with optional arg overrides.
type GateTemplateRef struct {
	// Name of the GateTemplate CR (cluster-scoped).
	Name string `json:"name"`
	// Args overrides template-level arg defaults for this policy.
	Args map[string]string `json:"args,omitempty"`
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

// GateTemplateSpec defines a reusable, parameterised gate evaluation config.
// The type field selects the runner implementation; Kapro calls Gate.Evaluate()
// without knowing which runner is behind it (same as kubelet → CRI → containerd).
type GateTemplateSpec struct {
	// Type selects the gate runner: cel | job | webhook | argo-analysis | opa | plugin-gateway
	// +kubebuilder:validation:Enum=cel;job;webhook;argo-analysis;opa;plugin-gateway
	Type string `json:"type"`

	// Args declares parameters that can be injected at evaluation time.
	// Values here are defaults; policy-level and runtime args override them.
	Args []GateArg `json:"args,omitempty"`

	// FailurePolicy controls promotion behaviour when this gate fails.
	// +kubebuilder:validation:Enum=halt;retry;skip
	// +kubebuilder:default=halt
	FailurePolicy string `json:"failurePolicy,omitempty"`

	// InconclusivePolicy controls behaviour when the gate cannot determine pass/fail.
	// +kubebuilder:validation:Enum=retry;skip;halt
	// +kubebuilder:default=retry
	InconclusivePolicy string `json:"inconclusivePolicy,omitempty"`

	// Timeout is the maximum time to wait before declaring the gate failed.
	// Defaults to 30m.
	Timeout string `json:"timeout,omitempty"`

	// MaxAttempts is the maximum retry count before failing.
	// +kubebuilder:default=3
	MaxAttempts int `json:"maxAttempts,omitempty"`

	// CEL configures the built-in CEL expression gate.
	// Used when type == "cel".
	CEL *CELGateSpec `json:"cel,omitempty"`

	// ArgoAnalysis configures the Argo Rollouts AnalysisRun gate.
	// Used when type == "argo-analysis".
	ArgoAnalysis *ArgoAnalysisGateSpec `json:"argoAnalysis,omitempty"`

	// Job configures the Kubernetes Job gate.
	// Used when type == "job".
	Job *JobGateSpec `json:"job,omitempty"`

	// Webhook configures the HTTP webhook gate.
	// Used when type == "webhook".
	Webhook *WebhookGateSpec `json:"webhook,omitempty"`

	// PluginGateway references a namespace-scoped PluginGateway resource.
	// Used when type == "plugin-gateway". The PluginGateway must be in the
	// same namespace as the Promotion being evaluated.
	PluginGatewayRef *PluginGatewayRef `json:"pluginGatewayRef,omitempty"`
}

// GateArg declares a named parameter with an optional default value.
type GateArg struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// CELGateSpec configures the built-in CEL expression gate.
// The expression is evaluated against {args, environment, artifact, promotion}.
type CELGateSpec struct {
	// Expression is the CEL boolean expression to evaluate.
	// Available variables: args (map), environment (object), artifact (object).
	// Example: args.error_rate <= 0.01 && environment.labels.wave == "pilot"
	Expression string `json:"expression"`
}

// ArgoAnalysisGateSpec configures the Argo Rollouts AnalysisRun gate.
// Kapro creates an AnalysisRun, reads its status, and translates the result.
// Kapro does not know about Argo internals — it only reads the phase.
type ArgoAnalysisGateSpec struct {
	// TemplateName is the name of the AnalysisTemplate in the target namespace.
	TemplateName string `json:"templateName"`
	// Namespace where the AnalysisTemplate lives and AnalysisRuns are created.
	// Defaults to "argo-rollouts".
	Namespace string `json:"namespace,omitempty"`
}

// JobGateSpec configures the Kubernetes Job gate.
// The job must exit 0 for the gate to pass.
type JobGateSpec struct {
	// Image is the container image to run.
	Image string `json:"image"`
	// Command overrides the container entrypoint.
	Command []string `json:"command,omitempty"`
	// Args are passed to the container command.
	Args []string `json:"args,omitempty"`
	// Env injects environment variables. Args map values are available via
	// KAPRO_ARG_<UPPERCASED_NAME> automatically.
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// WebhookGateSpec configures the HTTP webhook gate.
// Kapro POSTs a GateRequest JSON body and polls for {"status":"passed"|"failed"|"inconclusive"}.
type WebhookGateSpec struct {
	// URL is the HTTP endpoint to POST to.
	URL string `json:"url"`
	// PollInterval controls how often to poll. Defaults to 30s.
	PollInterval string `json:"pollInterval,omitempty"`
}

// GateRunStatus is Kapro's authoritative snapshot of one gate evaluation.
// Written to Promotion.Status.Gates[] — same as ContainerStatus in PodStatus.
// The source of truth is the vendor resource (AnalysisRun, Job, etc.);
// this is the normalised cache Kapro's state machine reads from.
type GateRunStatus struct {
	// Name is the GateTemplate name.
	Name      string    `json:"name"`
	Phase     GatePhase `json:"phase"`
	Message   string    `json:"message,omitempty"`
	StartedAt string    `json:"startedAt,omitempty"`
	// FinishedAt is set when Phase reaches a terminal state.
	FinishedAt string `json:"finishedAt,omitempty"`
	// Attempts counts how many times this gate has been evaluated.
	Attempts int `json:"attempts,omitempty"`
	// VendorRef points to the vendor-managed resource (e.g., AnalysisRun, Job).
	// Nil for in-process gates (cel, webhook).
	VendorRef *corev1.ObjectReference `json:"vendorRef,omitempty"`
	// Results contains the per-condition breakdown returned by the runner.
	Results []GateConditionResult `json:"results,omitempty"`
}

// GateConditionResult is the per-metric/condition result within a GateRunStatus.
type GateConditionResult struct {
	Name    string    `json:"name"`
	Phase   GatePhase `json:"phase"`
	Value   string    `json:"value,omitempty"`
	Message string    `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Failure Policy",type=string,JSONPath=`.spec.failurePolicy`

// GateTemplate is a reusable, parameterised gate evaluation config.
// Referenced by PromotionPolicy.spec.gate.templates[].
// Kapro evaluates it via Gate.Evaluate() without knowing the runner behind it.
type GateTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GateTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type GateTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GateTemplate `json:"items"`
}

type MetricGate struct {
	// Provider selects the gate backend: prometheus | datadog | keda.
	Provider string `json:"provider"`
	Query    string `json:"query"`
	Window   string `json:"window"`

	// Endpoint is the gRPC address of a KEDA-compatible external scaler server.
	// Used when Provider == "keda". Example:
	// "grpc://keda-external-scaler.keda.svc.cluster.local:9090"
	Endpoint string `json:"endpoint,omitempty"`

	// Threshold is the value to compare the KEDA metric against.
	// Used when Provider == "keda".
	Threshold float64 `json:"threshold,omitempty"`

	// Config is an opaque JSON blob passed verbatim to the gate implementation.
	// For the KEDA gate this is a serialised keda.Config struct.
	// Takes precedence over Endpoint/Threshold/Query when set.
	// +kubebuilder:pruning:PreserveUnknownFields
	Config []byte `json:"config,omitempty"`
}

type ApprovalConfig struct {
	Required  bool     `json:"required"`
	Approvers []string `json:"approvers,omitempty"`
}

type NotificationSpec struct {
	// Type selects the notification provider: email | teams | slack | webhook | pagerduty | opsgenie
	Type string `json:"type"`
	// Channel is used by slack (channel name) and pagerduty/opsgenie (service/team ID).
	// Kept for backward compatibility.
	Channel string `json:"channel,omitempty"`
	// URL is used by teams and webhook provider types.
	// Kept for backward compatibility.
	URL string `json:"url,omitempty"`
	// Email configures the SMTP email provider.
	// Used when type == "email".
	Email *EmailNotifierSpec `json:"email,omitempty"`
}

// EmailNotifierSpec configures SMTP email delivery for approval notifications.
type EmailNotifierSpec struct {
	// To is the list of recipient email addresses.
	// +kubebuilder:validation:MinItems=1
	To []string `json:"to"`
	// From overrides the sender address. If empty, the value in the SMTP secret is used.
	From string `json:"from,omitempty"`
	// SmtpSecretRef references a Secret with keys: host, port, username, password.
	// Optional key "tls: true" enables direct TLS (port 465); default is STARTTLS (port 587).
	SmtpSecretRef corev1.LocalObjectReference `json:"smtpSecretRef"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// PromotionPolicy defines reusable gate rules for promoting between environments.
type PromotionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionPolicySpec `json:"spec,omitempty"`
}

// ---- ProgressionPolicy -------------------------------------------------------

// ProgressionOnFailure defines the failure policy for a batch progression gate.
// +kubebuilder:validation:Enum=Halt;Skip;Retry
type ProgressionOnFailure string

const (
	// ProgressionOnFailureHalt stops the pipeline when a gate fails (default).
	ProgressionOnFailureHalt  ProgressionOnFailure = "Halt"
	// ProgressionOnFailureSkip skips the failed batch and continues progression.
	ProgressionOnFailureSkip  ProgressionOnFailure = "Skip"
	// ProgressionOnFailureRetry retries the gate evaluation on next reconcile.
	ProgressionOnFailureRetry ProgressionOnFailure = "Retry"
)

// ProgressionPolicySpec defines batch-level gates that must pass before the
// next batch in the DAG is started.  Unlike PromotionPolicy (which gates each
// individual cluster promotion), ProgressionPolicy gates the whole batch — it
// evaluates aggregate metrics across all converged clusters and enforces a
// batch-level soak period that starts after the last cluster converges.
type ProgressionPolicySpec struct {
	// BatchSoak is the duration to wait after the final cluster in the batch
	// reaches Converged before advancing to the next batch.
	// Example: "30m", "1h"
	// +optional
	BatchSoak string `json:"batchSoak,omitempty"`

	// Metrics are aggregate metrics to evaluate across the batch after convergence.
	// All metrics must pass for the gate to succeed.
	// +optional
	Metrics []MetricGate `json:"metrics,omitempty"`

	// Approval requires a human Approval object to unblock advancement.
	// +optional
	Approval *ApprovalConfig `json:"approval,omitempty"`

	// OnFailure defines the behaviour when a gate fails.
	// Defaults to Halt.
	// +kubebuilder:default=Halt
	// +optional
	OnFailure ProgressionOnFailure `json:"onFailure,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="BatchSoak",type=string,JSONPath=`.spec.batchSoak`
// +kubebuilder:printcolumn:name="OnFailure",type=string,JSONPath=`.spec.onFailure`

// ProgressionPolicy defines batch-level gate rules for pipeline progression.
// It is evaluated by the BatchRun controller after all clusters in the batch
// are Converged, before the pipeline advances to the next batch.
type ProgressionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProgressionPolicySpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type ProgressionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProgressionPolicy `json:"items"`
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
	Name      string                 `json:"name"`
	DependsOn []string               `json:"dependsOn,omitempty"`
	Selectors []metav1.LabelSelector `json:"selectors"`
	// PromotionPolicyRef is the name of a PromotionPolicy to apply as a
	// per-cluster gate inside this batch (soak per cluster, metrics per cluster).
	// Optional — omit for no per-cluster gate.
	PromotionPolicyRef string `json:"promotionPolicyRef,omitempty"`
	// ProgressionPolicyRef is the name of a ProgressionPolicy to apply as a
	// batch-level gate after all clusters in the batch are Converged.
	// Optional — omit for no batch gate.
	ProgressionPolicyRef string `json:"progressionPolicyRef,omitempty"`
	// PolicyRef is deprecated. Use promotionPolicyRef instead.
	// +deprecated
	PolicyRef string `json:"policyRef,omitempty"`
}

// PipelineStatus defines the observed state of Pipeline.
type PipelineStatus struct {
	// Phase reflects the overall progression state of this Pipeline.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase      string `json:"phase,omitempty"`
	ActiveStep string `json:"activeStep,omitempty"`
	// BatchProgress summarises the state of each batch in progression order.
	BatchProgress []BatchProgressEntry `json:"batchProgress,omitempty"`
	// TotalBatches is the total number of batches in the progression DAG.
	TotalBatches int `json:"totalBatches,omitempty"`
	// CompletedBatches is the number of batches that have reached Complete phase.
	CompletedBatches int `json:"completedBatches,omitempty"`
	// ObservedGeneration is the .metadata.generation this status was produced from.
	// Used by Flux health detection and kstatus.
	// +kubebuilder:default=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds CNCF-standard metav1.Condition entries.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BatchProgressEntry is one row in Pipeline.status.batchProgress.
type BatchProgressEntry struct {
	// Name is the batch name from Pipeline.spec.progression.batches[].name.
	Name string `json:"name"`
	// Phase is the BatchRun phase for this batch.
	Phase string `json:"phase,omitempty"`
	// BatchRunRef is the name of the live BatchRun object.
	BatchRunRef string `json:"batchRunRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Pipeline defines the promotion DAG and batch progression owned by a Release.
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PipelineSpec   `json:"spec,omitempty"`
	Status            PipelineStatus `json:"status,omitempty"`
}

// ---- Release ----------------------------------------------------------------

type ReleaseSpec struct {
	Artifact          string             `json:"artifact"`
	Scope             ReleaseScope       `json:"scope"`
	PipelineRef       string             `json:"pipelineRef"`
	PipelineOverrides *PipelineOverrides `json:"pipelineOverrides,omitempty"`
	// AppKey is the key used to look up the current version in ClusterRegistration.status.currentVersions.
	// Defaults to the Artifact name when not set.
	// This field enables multi-tenant use: different teams can deploy different apps
	// to the same cluster without version-key collisions.
	// +kubebuilder:default=""
	AppKey string `json:"appKey,omitempty"`
	// Suspended pauses all FSM advancement for this Release when true.
	// In-flight Promotions and BatchRuns are not cancelled — they complete their
	// current phase, but the Release will not advance to the next step until
	// Suspended is set back to false.
	// Useful for pausing a mid-flight rollout during an incident without losing
	// accumulated gate results.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
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
	// ObservedGeneration is the .metadata.generation this status was derived from.
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	Phase              ReleasePhase `json:"phase,omitempty"`
	PipelineRef        string       `json:"pipelineRef,omitempty"`
	// ResolvedVersion is the OCI digest resolved from the Artifact CR at creation time.
	// Format: <repository>@sha256:<digest> — this is what actuators apply.
	// Set once in Pending phase and never changed.
	ResolvedVersion string             `json:"resolvedVersion,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
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
// +kubebuilder:resource:scope=Cluster

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
	PromotionPhaseVerification    PromotionPhase = "Verification"
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
	// AppKey is the key used to look up the current version in ClusterRegistration.status.currentVersions.
	// Copied from Release.Spec.AppKey at Promotion creation time.
	// Defaults to the Artifact name when not set.
	AppKey string `json:"appKey,omitempty"`
}

type PromotionStatus struct {
	// ObservedGeneration is the .metadata.generation this status was derived from.
	ObservedGeneration int64          `json:"observedGeneration,omitempty"`
	Phase              PromotionPhase `json:"phase,omitempty"`
	StartedAt          string         `json:"startedAt,omitempty"`
	FinishedAt         string         `json:"finishedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	Message            string             `json:"message,omitempty"`
	// PreviousVersion holds the version before this promotion, used for rollback.
	PreviousVersion string `json:"previousVersion,omitempty"`
	// ApprovalSentAt records when the approval notification was last dispatched.
	// Used to avoid re-notifying on every reconcile loop while in WaitingApproval.
	ApprovalSentAt string `json:"approvalSentAt,omitempty"`
	// Gates is Kapro's authoritative snapshot of GateTemplate evaluation state.
	// Written here from Gate.Evaluate() results — same pattern as ContainerStatus in PodStatus.
	// State machine reads this to decide phase transitions; vendor resources are the source of truth.
	Gates []GateRunStatus `json:"gates,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
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
	// BatchPhaseWaitingPromotions means BatchRun has created one Promotion per
	// cluster and is waiting for all of them to reach Converged (like a Job
	// waiting for its Pods). The Promotion state machine owns apply+gate logic.
	BatchPhaseWaitingPromotions BatchPhase = "WaitingPromotions"
	BatchPhaseGateCheck          BatchPhase = "GateCheck"
	BatchPhaseWaitingApproval    BatchPhase = "WaitingApproval"
	BatchPhaseComplete           BatchPhase = "Complete"
	BatchPhaseFailed             BatchPhase = "Failed"
)

type BatchRunSpec struct {
	ReleaseRef string                 `json:"releaseRef"`
	BatchName  string                 `json:"batchName"`
	Selectors  []metav1.LabelSelector `json:"selectors"`
	// PromotionPolicyRef is the per-cluster PromotionPolicy gate (copied from Batch).
	PromotionPolicyRef string `json:"promotionPolicyRef,omitempty"`
	// ProgressionPolicyRef is the batch-level ProgressionPolicy gate (copied from Batch).
	ProgressionPolicyRef string `json:"progressionPolicyRef,omitempty"`
	// PolicyRef is deprecated. Use promotionPolicyRef instead.
	// +deprecated
	PolicyRef string `json:"policyRef,omitempty"`
	DependsOn []string               `json:"dependsOn,omitempty"`
}

type ClusterStatus struct {
	EnvironmentRef string       `json:"environmentRef"`
	Phase          ClusterPhase `json:"phase"`
	Version        string       `json:"version,omitempty"`
	Message        string       `json:"message,omitempty"`
}

type BatchRunStatus struct {
	// ObservedGeneration is the .metadata.generation this status was derived from.
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	Phase              BatchPhase      `json:"phase,omitempty"`
	Clusters           []ClusterStatus `json:"clusters,omitempty"`
	// PromotionRefs tracks the names of Promotion objects owned by this BatchRun,
	// one per resolved cluster — analogous to a Job tracking its Pod names.
	PromotionRefs []string           `json:"promotionRefs,omitempty"`
	StartedAt     string             `json:"startedAt,omitempty"`
	FinishedAt    string             `json:"finishedAt,omitempty"`
	Conditions    []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
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

// PluginType identifies which KSI (Kapro Standard Interface) this plugin implements.
// +kubebuilder:validation:Enum=Actuator;Provider;Gate;Verifier;Registry;Notifier;Health
type PluginType string

const (
	// PluginTypeActuator — KAI: Apply/IsConverged/Rollback (delivery system).
	PluginTypeActuator PluginType = "Actuator"
	// PluginTypeProvider — KCI: Connect/IsReachable (cluster access).
	PluginTypeProvider PluginType = "Provider"
	// PluginTypeGate — KGI: Evaluate (promotion gate).
	PluginTypeGate PluginType = "Gate"
	// PluginTypeVerifier — KVI: Verify (artifact signature/attestation).
	PluginTypeVerifier PluginType = "Verifier"
	// PluginTypeRegistry — KRI: Exists/Inspect/Tag/Copy/ListTags (OCI registry).
	PluginTypeRegistry PluginType = "Registry"
	// PluginTypeNotifier — KNI: Notify (event fanout).
	PluginTypeNotifier PluginType = "Notifier"
	// PluginTypeHealth — KHI: AssessHealth (workload health).
	PluginTypeHealth PluginType = "Health"
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
	// Conditions holds standard Kubernetes condition objects (Ready, etc.).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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

// ── KAgent ────────────────────────────────────────────────────────────────────

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Last Trigger",type=string,JSONPath=`.status.lastTriggerAt`

// KAgent is an autonomous release trigger.
// It watches an external source (MLflow model registry, OCI tag, Prometheus metric)
// and creates a Kapro Release CR when the trigger condition is met.
//
// This closes the AI promotion loop:
//   MLflow pushes model → KAgent detects new version → KAgent creates Release → Kapro promotes it.
type KAgent struct {
metav1.TypeMeta   `json:",inline"`
metav1.ObjectMeta `json:"metadata,omitempty"`
Spec              KAgentSpec   `json:"spec,omitempty"`
Status            KAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KAgentList struct {
metav1.TypeMeta `json:",inline"`
metav1.ListMeta `json:"metadata,omitempty"`
Items           []KAgent `json:"items"`
}

// KAgentSpec defines what the agent watches and what Release it creates.
type KAgentSpec struct {
// Source defines the trigger source.
Source KAgentSource `json:"source"`
// ReleaseTemplate is the template used to create a Release CR when triggered.
ReleaseTemplate KAgentReleaseTemplate `json:"releaseTemplate"`
// PollInterval is how often the agent polls the source.
// +kubebuilder:default="60s"
// +optional
PollInterval string `json:"pollInterval,omitempty"`
// Suspend pauses the agent when true.
// +optional
Suspend bool `json:"suspend,omitempty"`
}

// KAgentSource describes what the agent watches.
type KAgentSource struct {
// Type selects the source backend.
// +kubebuilder:validation:Enum=mlflow;oci;prometheus
Type string `json:"type"`
// MLflow configures the MLflow Model Registry source.
// +optional
MLflow *KAgentMLflowSource `json:"mlflow,omitempty"`
// OCI configures an OCI registry tag watcher.
// +optional
OCI *KAgentOCISource `json:"oci,omitempty"`
// Prometheus configures a Prometheus metric threshold trigger.
// +optional
Prometheus *KAgentPrometheusSource `json:"prometheus,omitempty"`
}

// KAgentMLflowSource watches the MLflow Model Registry for new Production-stage model versions.
type KAgentMLflowSource struct {
// TrackingServerURL is the MLflow tracking server base URL.
// e.g. "http://mlflow.mlops-system.svc:5000"
TrackingServerURL string `json:"trackingServerURL"`
// ModelName is the registered model name in MLflow.
ModelName string `json:"modelName"`
// Stage filters which model stage triggers a release.
// +kubebuilder:validation:Enum=Production;Staging;Archived
// +kubebuilder:default=Production
// +optional
Stage string `json:"stage,omitempty"`
// TokenSecretRef is the name of a Secret with key "token" for authenticated MLflow.
// +optional
TokenSecretRef string `json:"tokenSecretRef,omitempty"`
}

// KAgentOCISource watches an OCI repository for a new tag matching a pattern.
type KAgentOCISource struct {
// Repository is the OCI repository URL, e.g. "registry.example.com/myapp".
Repository string `json:"repository"`
// TagPattern is a Go regexp matched against image tags.
// +kubebuilder:default="^v[0-9]+\\.[0-9]+\\.[0-9]+$"
// +optional
TagPattern string `json:"tagPattern,omitempty"`
// SecretRef is a Secret name containing ".dockerconfigjson" for private registries.
// +optional
SecretRef string `json:"secretRef,omitempty"`
}

// KAgentPrometheusSource triggers when a Prometheus query crosses a threshold.
type KAgentPrometheusSource struct {
// Address is the Prometheus endpoint, e.g. "http://prometheus.monitoring.svc:9090".
Address string `json:"address"`
// Query is the PromQL expression. Must return a scalar or single-series vector.
Query string `json:"query"`
// Threshold is the numeric threshold value.
Threshold float64 `json:"threshold"`
// Operator compares the query result to Threshold.
// +kubebuilder:validation:Enum=">"; ">="; "<"; "<="; "=="
Operator string `json:"operator"`
}

// KAgentReleaseTemplate is the Release CR template instantiated when the agent fires.
type KAgentReleaseTemplate struct {
// ArtifactPrefix is prepended to the detected version to form the Artifact name.
// e.g. prefix "myapp-" + version "v1.2.3" → Artifact "myapp-v1.2.3".
ArtifactPrefix string `json:"artifactPrefix"`
// Scope is the Environment label selector for the Release.
Scope ReleaseScope `json:"scope"`
// PipelineRef is the Pipeline template to clone for this Release.
// +optional
PipelineRef string `json:"pipelineRef,omitempty"`
// Labels to apply to the created Release.
// +optional
Labels map[string]string `json:"labels,omitempty"`
}

// KAgentStatus reports the agent's last known state.
type KAgentStatus struct {
// Active is true when the agent is running and not suspended.
Active bool `json:"active,omitempty"`
// LastTriggerAt is the RFC3339 timestamp of the most recent trigger.
LastTriggerAt string `json:"lastTriggerAt,omitempty"`
// LastVersion is the version string that last triggered a Release.
LastVersion string `json:"lastVersion,omitempty"`
// LastRelease is the name of the most recently created Release CR.
LastRelease string `json:"lastRelease,omitempty"`
// ObservedVersion is the latest version seen (may not have triggered yet).
ObservedVersion string `json:"observedVersion,omitempty"`
// Conditions holds standard Kubernetes condition objects.
// +optional
// +listType=map
// +listMapKey=type
Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ---- PluginGateway ----------------------------------------------------------

// PluginGatewayMode selects the execution model for a PluginGateway.
type PluginGatewayMode string

const (
	// PluginGatewayModeBuiltin routes evaluation to a named built-in gate
	// (soak, metrics, cel, argo-analysis, webhook). No external process needed.
	PluginGatewayModeBuiltin PluginGatewayMode = "builtin"
	// PluginGatewayModeJob creates an ephemeral Kubernetes Job per evaluation.
	// Job exit 0 = passed; non-zero = failed. No persistent plugin process.
	PluginGatewayModeJob PluginGatewayMode = "job"
	// PluginGatewayModeRemote calls an existing HTTP or gRPC endpoint.
	// Unlike PluginRegistration, Remote mode needs no deployed plugin process.
	PluginGatewayModeRemote PluginGatewayMode = "remote"
)

// PluginGatewayRef references a PluginGateway in the same namespace.
type PluginGatewayRef struct {
	// Name of the PluginGateway CR (namespace-scoped).
	Name string `json:"name"`
}

// BuiltinGatewaySpec routes evaluation to a named built-in gate.
type BuiltinGatewaySpec struct {
	// Name of the built-in gate to route to.
	// +kubebuilder:validation:Enum=soak;metrics;cel;argo-analysis;webhook
	Name string `json:"name"`
	// Config passes key/value configuration to the built-in gate evaluator.
	Config map[string]string `json:"config,omitempty"`
}

// JobGatewaySpec configures ephemeral Kubernetes Job execution per gate evaluation.
// A new Job is created for each evaluation; TTL cleanup is automatic.
type JobGatewaySpec struct {
	// Image is the container image to run.
	Image string `json:"image"`
	// Command overrides the container entrypoint.
	Command []string `json:"command,omitempty"`
	// Args are passed to the container command.
	Args []string `json:"args,omitempty"`
	// Env injects additional environment variables.
	// Kapro automatically injects KAPRO_VERSION, KAPRO_ENVIRONMENT, KAPRO_RELEASE.
	Env []corev1.EnvVar `json:"env,omitempty"`
	// ServiceAccountName is the SA used by the Job. Defaults to "default".
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// Timeout is the maximum Job run time. Defaults to 10m.
	Timeout string `json:"timeout,omitempty"`
	// BackoffLimit is the Job retry count. Defaults to 0 (no retries).
	// +kubebuilder:default=0
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
	// TTLSecondsAfterFinished cleans up completed Jobs. Defaults to 300.
	// +kubebuilder:default=300
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// RemoteGatewaySpec calls an existing HTTP or gRPC endpoint per evaluation.
// Unlike PluginRegistration (which requires a persistent gRPC server process),
// Remote mode makes stateless per-evaluation calls to any reachable endpoint.
type RemoteGatewaySpec struct {
	// Protocol selects the wire format.
	// +kubebuilder:validation:Enum=http;grpc
	// +kubebuilder:default=http
	Protocol string `json:"protocol,omitempty"`
	// URL is the endpoint to call.
	// HTTP: POST receives a GateRequest JSON body; expects {"status":"passed"|"failed"|"inconclusive"}.
	// gRPC: calls kapro.v1alpha1.GateService/Evaluate.
	URL string `json:"url"`
	// TLSSecretRef names a Secret (in the same namespace) with keys tls.crt, tls.key, ca.crt.
	TLSSecretRef string `json:"tlsSecretRef,omitempty"`
	// Timeout per call. Defaults to 30s.
	Timeout string `json:"timeout,omitempty"`
	// Headers are additional HTTP headers to send (HTTP mode only).
	Headers map[string]string `json:"headers,omitempty"`
}

// PluginGatewaySpec defines the gateway configuration.
type PluginGatewaySpec struct {
	// Mode selects the execution model: builtin | job | remote.
	// +kubebuilder:validation:Enum=builtin;job;remote
	Mode PluginGatewayMode `json:"mode"`

	// Builtin routes evaluation to a named built-in gate.
	// Required when mode == "builtin".
	Builtin *BuiltinGatewaySpec `json:"builtin,omitempty"`

	// Job configures ephemeral Kubernetes Job execution.
	// Required when mode == "job".
	Job *JobGatewaySpec `json:"job,omitempty"`

	// Remote configures outbound calls to an existing endpoint.
	// Required when mode == "remote".
	Remote *RemoteGatewaySpec `json:"remote,omitempty"`
}

// PluginGatewayStatus is written by the PluginGateway controller.
type PluginGatewayStatus struct {
	// Ready is true when the gateway has been validated and is usable.
	Ready bool `json:"ready"`
	// Message is a human-readable status summary.
	Message string `json:"message,omitempty"`
	// LastChecked is the time the controller last verified the gateway.
	LastChecked string `json:"lastChecked,omitempty"`
	// Conditions provides standard Kubernetes condition fields.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PluginGateway is a namespace-scoped, ambient-style gate integration.
//
// Unlike PluginRegistration (cluster-scoped, requires a persistent gRPC server),
// a PluginGateway needs no deployed plugin process. Three modes are supported:
//   - builtin: routes to a named built-in gate (soak, cel, metrics, etc.)
//   - job: spawns an ephemeral Kubernetes Job per evaluation
//   - remote: calls an existing HTTP or gRPC endpoint per evaluation
//
// Any team can drop a PluginGateway in their namespace and reference it from a
// GateTemplate (type: plugin-gateway) without touching cluster-level resources.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pgw
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PluginGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PluginGatewaySpec   `json:"spec,omitempty"`
	Status            PluginGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PluginGatewayList contains a list of PluginGateway.
type PluginGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PluginGateway `json:"items"`
}

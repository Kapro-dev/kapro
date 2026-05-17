// Package v1alpha1 contains the Kapro API types.
// +groupName=kapro.io
package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finalizer constants — prevents premature deletion of stateful resources.
const (
	// PromotionRunFinalizer is added to PromotionRun objects to allow cleanup of owned rollout state.
	PromotionRunFinalizer = "kapro.io/promotionrun-finalizer"
	// BootstrapTokenFinalizer is added to BootstrapToken objects to allow RBAC cleanup on deletion.
	BootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer" //nolint:gosec // not a credential
	// FleetClusterFinalizer is added to FleetCluster objects to allow bootstrap RBAC cleanup on deletion.
	FleetClusterFinalizer = "kapro.io/member-cluster-finalizer" //nolint:gosec // not a credential
)

// Condition type constants — Flux three-condition framework for operator status reporting.
const (
	// ConditionTypeReconciling indicates the controller is actively working on the object.
	// True while progressing, False when the object is terminal or suspended.
	ConditionTypeReconciling = "Reconciling"
	// ConditionTypeStalled indicates the object cannot progress without external intervention.
	// True when stuck (e.g. missing artifact, gate failure), False when healthy or recovering.
	ConditionTypeStalled = "Stalled"
	// ConditionTypeCompatible indicates a plugin reports a supported extension contract version.
	ConditionTypeCompatible = "Compatible"
	// ConditionTypeReady is the standard Kubernetes summary condition: True means the
	// object is observed-ready by its primary writer. For FleetCluster, this is True
	// after successful bootstrap registration AND a fresh heartbeat within the
	// configured staleness window. Surfaced in kubectl printcolumns.
	ConditionTypeReady = "Ready"
	// ConditionTypeRegistered indicates a FleetCluster has consumed its bootstrap
	// slot via a valid CSR exchange, and the hub has issued the per-cluster
	// ClusterRole + ClusterRoleBinding. True after first successful registration;
	// once True it stays True until the FleetCluster is deleted or its bootstrap
	// slot is rotated.
	ConditionTypeRegistered = "Registered"
)

// ---- Shared cluster types ---------------------------------------------------

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

// DeliveryMode controls where backend delivery is executed.
// +kubebuilder:validation:Enum=push;pull
type DeliveryMode string

const (
	// DeliveryModePush means the hub calls a hub-side backend adapter.
	DeliveryModePush DeliveryMode = "push"
	// DeliveryModePull means the hub records desired state and a spoke agent
	// calls a local backend adapter.
	DeliveryModePull DeliveryMode = "pull"
)

// BackendRuntime identifies where a backend adapter is allowed to run.
// +kubebuilder:validation:Enum=Hub;Spoke;Both
type BackendRuntime string

const (
	BackendRuntimeHub   BackendRuntime = "Hub"
	BackendRuntimeSpoke BackendRuntime = "Spoke"
	BackendRuntimeBoth  BackendRuntime = "Both"
)

// BackendDriver identifies the backend implementation family.
// +kubebuilder:validation:Enum=flux;argo;external
type BackendDriver string

const (
	BackendDriverFlux     BackendDriver = "flux"
	BackendDriverArgo     BackendDriver = "argo"
	BackendDriverExternal BackendDriver = "external"
)

// DeliverySpec selects a backend-neutral delivery profile for a cluster or fleet.
// Backend-specific resource names live in parameters and are interpreted only by
// the selected backend adapter.
type DeliverySpec struct {
	// Mode controls who performs delivery.
	// +kubebuilder:default="pull"
	Mode DeliveryMode `json:"mode"`
	// BackendRef is the BackendProfile name. Built-in profiles commonly use
	// "flux" or "argo"; external profiles may use any platform-defined name.
	BackendRef string `json:"backendRef"`
	// Parameters are opaque backend-specific settings, following the
	// StorageClass.parameters pattern. Kapro core does not interpret them.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// RegistryKey returns the composite key used to resolve the delivery adapter.
func (d *DeliverySpec) RegistryKey() string {
	if d == nil {
		return "/"
	}
	return string(d.Mode) + "/" + d.BackendRef
}

// Param returns a backend-specific delivery parameter with a default.
func (d *DeliverySpec) Param(name, fallback string) string {
	if d == nil || d.Parameters == nil || d.Parameters[name] == "" {
		return fallback
	}
	return d.Parameters[name]
}

type HealthCheckSpec struct {
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}

// ---- FleetCluster shared types --------------------------------------------
// registered workload cluster. Written by kapro-cluster-controller at bootstrap
// time and refreshed on each heartbeat.
//
// Platform engineers and promotionplan authors can reference these fields in stage
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
	// Preset references PromotionPlan.spec.metricPresets by name.
	// Inline fields override the preset when set.
	// +optional
	Preset string `json:"preset,omitempty"`
	// Provider selects the metrics backend. Required when preset is empty.
	// +optional
	Provider string `json:"provider,omitempty"`
	// Query is a PromQL expression. The gate passes when the query returns a non-zero value.
	// Use range functions directly in the query for window-based checks, e.g.:
	//   min_over_time(error_rate[30m]) < 0.01
	// Or reference the Window field as a template: {{.Window}} is substituted at evaluation time.
	// Required when preset is empty.
	// +optional
	Query string `json:"query,omitempty"`
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
	Interval string `json:"interval,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Threshold is optional and presence-aware so an inline metric can
	// intentionally override a preset threshold to 0.
	// +optional
	Threshold *float64 `json:"threshold,omitempty"`
	// Analysis selects optional research-backed metric semantics. Empty keeps
	// the original threshold behavior for backwards compatibility.
	// +optional
	Analysis *MetricAnalysisSpec `json:"analysis,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Config []byte `json:"config,omitempty"`
}

// MetricAnalysisSpec configures how a metric observation is interpreted.
type MetricAnalysisSpec struct {
	// Mode selects the metric analysis algorithm.
	// threshold: compare the current value to threshold.
	// sloBurnRate: treat the current value as an error-budget burn rate.
	// baseline: compare the current value to a baseline query as a ratio.
	// sequential: sample over the window and require enough confidence before deciding.
	// changePoint: detect a statistically meaningful change inside the window.
	// score: convert the metric into a 0-100 canary score.
	// +kubebuilder:validation:Enum=threshold;sloBurnRate;baseline;sequential;changePoint;score
	// +optional
	Mode string `json:"mode,omitempty"`
	// Comparator controls threshold comparison.
	// Defaults to "gt" for threshold/sequential and "lte" for sloBurnRate/baseline.
	// +kubebuilder:validation:Enum=gt;gte;lt;lte
	// +optional
	Comparator string `json:"comparator,omitempty"`
	// BaselineQuery is required for baseline analysis. The current value is
	// divided by the baseline value before applying the threshold.
	// +optional
	BaselineQuery string `json:"baselineQuery,omitempty"`
	// BaselineHealthQuery must pass before baseline or score analysis can use
	// the baseline as a control. It should return a positive/true value when
	// the baseline is healthy.
	// +optional
	BaselineHealthQuery string `json:"baselineHealthQuery,omitempty"`
	// MinSamples is the minimum number of range samples required for sequential
	// analysis before Kapro can pass or fail the gate.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinSamples int32 `json:"minSamples,omitempty"`
	// MaxSamples is the maximum number of samples to wait for before a
	// statistical analysis must return a terminal decision using the evidence
	// available. Empty means no maximum.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxSamples int32 `json:"maxSamples,omitempty"`
	// ConfidenceThreshold is the minimum confidence required for sequential
	// analysis to make a terminal decision. Defaults to 0.95.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	ConfidenceThreshold *float64 `json:"confidenceThreshold,omitempty"`
	// Alpha is the false-positive budget for statistical tests. Defaults to
	// 0.05. Lower values are more conservative.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	Alpha *float64 `json:"alpha,omitempty"`
	// ScoreThreshold is the minimum score required for score analysis. Defaults
	// to 95.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	ScoreThreshold *float64 `json:"scoreThreshold,omitempty"`
}

type ApprovalConfig struct {
	Required  bool     `json:"required"`
	Approvers []string `json:"approvers,omitempty"`
}

// NotificationSpec configures where and when to send delivery lifecycle events.
type NotificationSpec struct {
	// Type selects the notification backend.
	// +kubebuilder:validation:Enum=webhook;slack;email
	Type string `json:"type"`
	// Events filters which lifecycle events trigger this notification.
	// Uses stable semantic event types. Currently emitted events:
	//   kapro.promotionrun.started, kapro.promotionrun.completed, kapro.promotionrun.failed,
	//   kapro.promotionrun.rollback.started, kapro.promotionrun.stage.completed,
	//   kapro.promotionrun.gate.passed, kapro.promotionrun.gate.failed,
	//   kapro.promotionrun.approval.required,
	//   kapro.promotionrun.target.pending, kapro.promotionrun.target.verification,
	//   kapro.promotionrun.target.health_check, kapro.promotionrun.target.soaking,
	//   kapro.promotionrun.target.metrics_check, kapro.promotionrun.target.applying,
	//   kapro.promotionrun.target.converged, kapro.promotionrun.target.failed,
	//   kapro.promotionrun.target.skipped.
	// Empty means all events.
	// +optional
	Events []string `json:"events,omitempty"`
	// Webhook configures HTTP POST delivery.
	// Required when type=webhook.
	// +optional
	Webhook *WebhookNotifierSpec `json:"webhook,omitempty"`
	// Slack configures Slack message delivery.
	// Required when type=slack.
	// +optional
	Slack *SlackNotifierSpec `json:"slack,omitempty"`
	// Email configures SMTP email delivery.
	// Required when type=email.
	// +optional
	Email *EmailNotifierSpec `json:"email,omitempty"`
}

// WebhookNotifierSpec configures HTTP POST notification delivery.
type WebhookNotifierSpec struct {
	// URL is the HTTP endpoint to POST events to.
	URL string `json:"url"`
	// Format selects the payload format.
	//   json (default): plain JSON event payload.
	//   cloudevents: CloudEvents v1.0 structured content mode.
	// +kubebuilder:validation:Enum=json;cloudevents
	// +kubebuilder:default="json"
	Format string `json:"format,omitempty"`
}

// SlackNotifierSpec configures Slack message delivery.
type SlackNotifierSpec struct {
	// Channel is the Slack channel to post to.
	Channel string `json:"channel"`
}

// EmailNotifierSpec configures SMTP email delivery.
type EmailNotifierSpec struct {
	// +kubebuilder:validation:MinItems=1
	To   []string `json:"to"`
	From string   `json:"from,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	SmtpSecretRef corev1.LocalObjectReference `json:"smtpSecretRef"`
}

// ---- Notification provider/policy API preview ------------------------------

// NotificationProviderSpec declares where lifecycle notifications can be sent.
// It is an API preview and is not wired into runtime dispatch yet.
//
// +kubebuilder:validation:XValidation:rule="self.type != 'webhook' || (has(self.webhook) && (has(self.webhook.url) || (has(self.secretRefs) && self.secretRefs.exists(s, s.purpose == 'webhookURL'))))",message="webhook config requires webhook.url or a secretRef with purpose=webhookURL when type=webhook"
// +kubebuilder:validation:XValidation:rule="self.type != 'slack' || has(self.slack)",message="slack config is required when type=slack"
// +kubebuilder:validation:XValidation:rule="self.type != 'email' || has(self.email)",message="email config is required when type=email"
// +kubebuilder:validation:XValidation:rule="self.type != 'git' || has(self.git)",message="git config is required when type=git"
type NotificationProviderSpec struct {
	// Type selects the notification provider backend.
	// +kubebuilder:validation:Enum=webhook;slack;email;git
	Type string `json:"type"`
	// Webhook configures HTTP POST notification delivery.
	// Required when type=webhook.
	// +optional
	Webhook *NotificationWebhookProviderSpec `json:"webhook,omitempty"`
	// Slack configures Slack notification delivery.
	// Required when type=slack.
	// +optional
	Slack *NotificationSlackProviderSpec `json:"slack,omitempty"`
	// Email configures SMTP notification delivery.
	// Required when type=email.
	// +optional
	Email *NotificationEmailProviderSpec `json:"email,omitempty"`
	// Git configures Git-backed notification delivery, for example audit commits.
	// Required when type=git.
	// +optional
	Git *NotificationGitProviderSpec `json:"git,omitempty"`
	// SecretRefs references provider credentials. Because NotificationProvider
	// is cluster-scoped, each reference must include a namespace.
	// +optional
	SecretRefs []NotificationProviderSecretRef `json:"secretRefs,omitempty"`
	// Parameters are provider-specific key-value settings for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// NotificationProviderSecretRef identifies one credential entry used by a provider.
type NotificationProviderSecretRef struct {
	// Name is the Secret name.
	Name string `json:"name"`
	// Namespace is the Secret namespace.
	Namespace string `json:"namespace"`
	// Key is the optional Secret data key within the Secret.
	// +optional
	Key string `json:"key,omitempty"`
	// Purpose describes how the credential is used, for example token,
	// webhookURL, smtpPassword, or sshPrivateKey.
	// +optional
	Purpose string `json:"purpose,omitempty"`
}

// NotificationWebhookProviderSpec configures HTTP POST notification delivery.
type NotificationWebhookProviderSpec struct {
	// URL is the HTTP endpoint to POST events to.
	// It may be omitted when supplied through secretRefs.
	// +optional
	URL string `json:"url,omitempty"`
	// Format selects the payload format.
	//   json (default): plain JSON event payload.
	//   cloudevents: CloudEvents v1.0 structured content mode.
	// +kubebuilder:validation:Enum=json;cloudevents
	// +kubebuilder:default="json"
	Format string `json:"format,omitempty"`
	// Headers are static HTTP headers sent with every request.
	// Do not put credentials here; use secretRefs instead.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`
}

// NotificationSlackProviderSpec configures Slack notification delivery.
type NotificationSlackProviderSpec struct {
	// Channel is the Slack channel to post to.
	Channel string `json:"channel"`
	// WebhookURL is the Slack incoming webhook URL.
	// It may be omitted when supplied through secretRefs.
	// +optional
	WebhookURL string `json:"webhookUrl,omitempty"`
}

// NotificationEmailProviderSpec configures SMTP notification delivery.
type NotificationEmailProviderSpec struct {
	// To is the default recipient list for this provider.
	// Policies may further narrow when this provider is used.
	// +kubebuilder:validation:MinItems=1
	To []string `json:"to"`
	// From is the sender address.
	From string `json:"from,omitempty"`
	// Host is the SMTP server host.
	Host string `json:"host"`
	// Port is the SMTP server port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// NotificationGitProviderSpec configures Git-backed notification delivery.
type NotificationGitProviderSpec struct {
	// Repository is the Git repository URL.
	Repository string `json:"repository"`
	// Branch is the branch to write notification records to.
	// +kubebuilder:default="main"
	Branch string `json:"branch,omitempty"`
	// Path is the repository path for notification records.
	Path string `json:"path,omitempty"`
	// AuthorName is used for generated commits.
	// +optional
	AuthorName string `json:"authorName,omitempty"`
	// AuthorEmail is used for generated commits.
	// +optional
	AuthorEmail string `json:"authorEmail,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=notifprov,categories=kapro-all
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationProvider is an API-preview declaration of where Kapro lifecycle
// notifications can be sent. It is spec-only; runtime dispatch remains future work.
type NotificationProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NotificationProviderSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type NotificationProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationProvider `json:"items"`
}

// NotificationPolicySpec declares when lifecycle notifications should be sent.
// It is an API preview and is not wired into runtime dispatch yet.
type NotificationPolicySpec struct {
	// Subscriptions route matching events to notification providers.
	// +kubebuilder:validation:MinItems=1
	Subscriptions []NotificationSubscription `json:"subscriptions"`
}

// NotificationSubscription routes matching events to one provider.
type NotificationSubscription struct {
	// Name identifies this subscription within the policy.
	// +optional
	Name string `json:"name,omitempty"`
	// ProviderRef references a NotificationProvider by name.
	ProviderRef NotificationProviderRef `json:"providerRef"`
	// Filter selects the lifecycle events delivered to the provider.
	// Empty means all events.
	// +optional
	Filter *NotificationEventFilter `json:"filter,omitempty"`
	// Parameters are subscription-specific key-value settings for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// NotificationProviderRef references a NotificationProvider by name.
type NotificationProviderRef struct {
	// Name is the NotificationProvider metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// NotificationEventFilter selects lifecycle events for a subscription.
type NotificationEventFilter struct {
	// Types filters by stable semantic event type, for example
	// kapro.promotionrun.completed or kapro.promotionrun.target.failed.
	// Empty means all event types.
	// +optional
	Types []string `json:"types,omitempty"`
	// PromotionRunSelector filters by PromotionRun labels.
	// +optional
	PromotionRunSelector *metav1.LabelSelector `json:"promotionrunSelector,omitempty"`
	// PromotionPlans filters by PromotionRun.spec.promotionplans[].name.
	// +optional
	PromotionPlans []string `json:"promotionplans,omitempty"`
	// Stages filters by PromotionPlan stage name.
	// +optional
	Stages []string `json:"stages,omitempty"`
	// Targets filters by FleetCluster name.
	// +optional
	Targets []string `json:"targets,omitempty"`
	// Phases filters by normalized event phase.
	// +optional
	Phases []string `json:"phases,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=notifpol,categories=kapro-all
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.subscriptions[0].providerRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationPolicy is an API-preview declaration of when Kapro lifecycle
// notifications should be routed to NotificationProvider objects. It is
// spec-only; runtime dispatch remains future work.
type NotificationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NotificationPolicySpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type NotificationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationPolicy `json:"items"`
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
	// +kubebuilder:validation:Enum=cel;job;webhook;plugin
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
	Plugin      *PluginGateSpec  `json:"plugin,omitempty"`
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

// PluginGateSpec references an external gate registered through PluginRegistration.
type PluginGateSpec struct {
	// Name is PluginRegistration.spec.name for a ready gate plugin.
	Name string `json:"name"`
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
	// +kubebuilder:validation:MaxItems=16
	Results []GateConditionResult `json:"results,omitempty"`
	// Evidence is structured, non-secret data that explains the gate decision.
	// It is intended for audit, debugging, notifications, and future AI agents.
	// +optional
	Evidence []GateEvidence `json:"evidence,omitempty"`
}

// GateConditionResult is the per-metric/condition result within a GateRunStatus.
type GateConditionResult struct {
	Name    string    `json:"name"`
	Phase   GatePhase `json:"phase"`
	Value   string    `json:"value,omitempty"`
	Message string    `json:"message,omitempty"`
	// Evidence is structured, non-secret data for this condition.
	// +optional
	Evidence []GateEvidence `json:"evidence,omitempty"`
}

// GateEvidence records the facts and analysis that produced a gate decision.
type GateEvidence struct {
	Type                string          `json:"type,omitempty"`
	Provider            string          `json:"provider,omitempty"`
	AnalysisMode        string          `json:"analysisMode,omitempty"`
	Comparator          string          `json:"comparator,omitempty"`
	Query               string          `json:"query,omitempty"`
	BaselineQuery       string          `json:"baselineQuery,omitempty"`
	BaselineHealthQuery string          `json:"baselineHealthQuery,omitempty"`
	Window              string          `json:"window,omitempty"`
	Interval            string          `json:"interval,omitempty"`
	ObservedValue       string          `json:"observedValue,omitempty"`
	Threshold           string          `json:"threshold,omitempty"`
	BaselineValue       string          `json:"baselineValue,omitempty"`
	BaselineHealthy     *bool           `json:"baselineHealthy,omitempty"`
	SampleCount         int64           `json:"sampleCount,omitempty"`
	Confidence          *float64        `json:"confidence,omitempty"`
	Alpha               *float64        `json:"alpha,omitempty"`
	PValue              *float64        `json:"pValue,omitempty"`
	EffectSize          string          `json:"effectSize,omitempty"`
	Score               *float64        `json:"score,omitempty"`
	DecisionRule        string          `json:"decisionRule,omitempty"`
	Reason              string          `json:"reason,omitempty"`
	Projection          *GateProjection `json:"projection,omitempty"`
}

// GateProjection records an optional forecast derived from gate evidence.
type GateProjection struct {
	Horizon string `json:"horizon,omitempty"`
	Value   string `json:"value,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ---- PromotionPlan ---------------------------------------------------------------

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

// StageStrategySpec controls how many targets a stage may bind concurrently.
type StageStrategySpec struct {
	// MaxParallel limits how many targets in this stage may be non-terminal at once.
	// Zero means unlimited.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxParallel int32 `json:"maxParallel,omitempty"`
	// MaxUnavailable is reserved for availability-aware strategies. The current
	// controller records the field but only enforces MaxParallel.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
}

// Stage is one node in a PromotionPlan's delivery DAG.
// It selects a set of target clusters by label selector, optionally gates them
// with a GatePolicy, and declares ordering via DependsOn.
//
// A single stage can target one or many clusters — the selector determines the
// fleet subset. Add a cluster to a wave by labeling its FleetCluster object;
// no PromotionPlan changes required.
type Stage struct {
	// Name uniquely identifies this stage within the promotionplan.
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
	// Strategy controls target binding concurrency for this stage.
	// +optional
	Strategy *StageStrategySpec `json:"strategy,omitempty"`
	// Gate is the inline gate policy evaluated after all targets in this
	// stage converge. If nil, the stage advances immediately on convergence.
	// Use for complex gates (webhook, job, approval). For simple soak time,
	// prefer StageDependency.RequiredSoakTime instead.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// OnFailure controls what Kapro does when this stage fails.
	// halt (default): stop the promotionplan, mark PromotionRun Failed.
	// skip: continue to downstream stages.
	// rollback: stop AND revert all targets promoted by earlier stages.
	// +kubebuilder:default=halt
	// +optional
	OnFailure StageFailurePolicy `json:"onFailure,omitempty"`
}

// PromotionPlanSpec defines a reusable progressive delivery path as a flat DAG of stages.
// A PromotionPlan is a template — referenced by PromotionRun.spec.promotionplans[].
// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type PromotionPlanSpec struct {
	// MetricPresets defines reusable metric gate snippets referenced by
	// Stage.gate.metrics[].preset. Presets are expanded into each target's
	// gate policy when a PromotionRun binds targets.
	// +optional
	MetricPresets map[string]MetricGate `json:"metricPresets,omitempty"`
	// Stages is the flat DAG of delivery stages.
	// Order is declared via DependsOn, not list position.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Stages []Stage `json:"stages"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pl,categories=kapro-all
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionPlan defines a reusable progressive delivery path as a DAG of stages.
// Each stage selects a fleet subset via label selectors and optionally gates
// advancement with a GatePolicy. Referenced by PromotionRun.spec.promotionplans[].
// PromotionPlan is a pure template — it has no controller, no status, no reconciler.
// Validation is enforced by the admission webhook. Execution state lives in PromotionRun.
type PromotionPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionPlanSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionPlan `json:"items"`
}

// ---- PromotionRun ----------------------------------------------------------------

// PromotionPlanRef is one node in the PromotionRun's promotionplan DAG.
// Multiple promotionplans can run in parallel; DependsOn declares ordering between them.
type PromotionPlanRef struct {
	// Name uniquely identifies this promotionplan node within the PromotionRun.
	Name string `json:"name"`
	// PromotionPlan is the name of the PromotionPlan CRD to execute.
	PromotionPlan string `json:"promotionplan"`
	// DependsOn lists promotionplan node names that must reach Complete before this one starts.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []string `json:"dependsOn,omitempty"`
}

// StageProgress tracks the execution state of one Stage within a promotionplan.
// Designed to render well in k9s describe view — operators see per-stage
// progress like CI promotionplan steps.
type StageProgress struct {
	// Name is the stage name from PromotionPlan.spec.stages[].name.
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
	// Deferred is the number of eligible targets not yet bound by the planner
	// or stage strategy.
	Deferred int `json:"deferred,omitempty"`
	// PlannerResults records why targets were skipped or deferred during the
	// latest planning cycle. Capped by the controller.
	// +optional
	PlannerResults []PlannerResult `json:"plannerResults,omitempty"`
	// Message is a human-readable summary of stage progress, designed for
	// k9s describe output. Examples:
	//   "2/5 clusters converged, soak: 12m/30m remaining"
	//   "waiting for canary stage"
	//   "blocked: manual approval required for de-prod"
	// +optional
	Message string `json:"message,omitempty"`
	// StartedAt is when this stage first had a Progressing target.
	// +optional
	StartedAt string `json:"startedAt,omitempty"`
	// CompletedAt is when all targets in this stage reached a terminal state.
	// +optional
	CompletedAt string `json:"completedAt,omitempty"`
}

// PlannerResult explains one planner decision for operator visibility.
type PlannerResult struct {
	// Target is the FleetCluster name affected by the decision.
	Target string `json:"target,omitempty"`
	// Plugin is the planner plugin or built-in strategy that made the decision.
	Plugin string `json:"plugin,omitempty"`
	// Phase is the planner phase, for example Filter, Score, Permit, or Bind.
	Phase string `json:"phase,omitempty"`
	// Reason is a short machine-readable reason.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable explanation.
	Message string `json:"message,omitempty"`
}

// PromotionPlanProgress tracks the execution state of one promotionplan node in a PromotionRun.
type PromotionPlanProgress struct {
	// Name matches PromotionPlanRef.name.
	Name string `json:"name"`
	// PromotionPlan is the PromotionPlan CRD name.
	PromotionPlan string `json:"promotionplan"`
	// ObservedGeneration pins the PromotionPlan generation used by this
	// PromotionRun. If the referenced PromotionPlan changes while the run is in
	// flight, the controller fails the run instead of silently switching DAGs.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is the current execution state of this promotionplan node.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase string `json:"phase,omitempty"`
	// ActiveStage is the name of the currently progressing stage (or the last completed one).
	// Gives operators a quick "where are we?" without expanding StageProgress.
	// +optional
	ActiveStage string `json:"activeStage,omitempty"`
	// StageProgress summarises the state of each stage in this promotionplan.
	StageProgress []StageProgress `json:"stageProgress,omitempty"`
}

// PromotionRunScope restricts a PromotionRun to an explicit subset of clusters.
// Only clusters listed in Targets will receive rollout entries.
type PromotionRunScope struct {
	// Targets is the allowlist of target cluster names.
	// Must be non-empty when Scope is set — an empty list is ignored.
	Targets []string `json:"targets,omitempty"`
}

// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type PromotionRunSpec struct {
	// Version is the default revision to deliver across the fleet.
	// For brownfield/native sources this is the revision for every unit that is
	// not explicitly listed in versions.
	// +optional
	Version string `json:"version,omitempty"`
	// Versions maps promotion unit name to the backend-native revision to
	// deliver. Use this when a PromotionRun promotes multiple existing Argo/Flux
	// objects together without creating a synthetic application object.
	// +optional
	Versions map[string]string `json:"versions,omitempty"`
	// PromotionPlans is the DAG of promotionplan nodes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	PromotionPlans []PromotionPlanRef `json:"promotionplans"`
	// Suspended pauses all advancement when true.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts this PromotionRun to a subset of clusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for the entire PromotionRun.
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

type PromotionRunPhase string

const (
	PromotionRunPhasePending     PromotionRunPhase = "Pending"
	PromotionRunPhaseProgressing PromotionRunPhase = "Progressing"
	PromotionRunPhaseComplete    PromotionRunPhase = "Complete"
	PromotionRunPhaseFailed      PromotionRunPhase = "Failed"
)

// PromotionRunStatus defines the observed state of PromotionRun.
type PromotionRunStatus struct {
	ObservedGeneration int64             `json:"observedGeneration,omitempty"`
	Phase              PromotionRunPhase `json:"phase,omitempty"`
	// ResolvedVersion is the OCI digest or tag resolved from spec.version.
	// Set once in Pending and never changed.
	ResolvedVersion string `json:"resolvedVersion,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
	// PromotionPlanProgress tracks execution state of each promotionplan node in the DAG.
	PromotionPlanProgress []PromotionPlanProgress `json:"promotionplanProgress,omitempty"`
	// Targets is deprecated compatibility state. The authoritative per-target
	// rollout state lives in child PromotionTarget objects.
	Targets []TargetStatus `json:"targets,omitempty"`
	// Report is the inline delivery summary.
	Report PromotionRunReportSummary `json:"report,omitempty"`
	// AuditTrail records immutable delivery provenance. Capped at 50 entries.
	AuditTrail []AuditEntry       `json:"auditTrail,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=rel,categories=kapro-all
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`,priority=0
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,priority=0
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=0
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.report.syncedTargets`,priority=0
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.report.failedTargets`,priority=0
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.report.pendingTargets`,priority=0
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.report.totalTargets`,priority=0
// +kubebuilder:printcolumn:name="Duration",type=string,JSONPath=`.status.report.duration`,priority=0
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`,priority=1
// +kubebuilder:printcolumn:name="Artifacts",type=integer,JSONPath=`.status.report.totalArtifacts`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,priority=0

// PromotionRun is the trigger for a progressive delivery rollout across the cluster fleet.
// It references an Artifact and a DAG of PromotionPlans that define the delivery path.
// The PromotionRun controller drives the promotionplan DAG, advancing each target cluster
// through the delivery FSM (MetricsCheck → WaitingApproval → Applying → Applied).
// Per-target execution state lives in child PromotionTarget objects; PromotionRun.status
// stores only rollout summary, promotionplan progress, and audit metadata.
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

// ---- PromotionTrigger ---------------------------------------------------------

// PromotionTriggerSpec defines an autonomous source that can create PromotionRun
// objects from verified artifact changes. The controller currently provides
// preview behavior for this API, and the API is intentionally safe by default.
//
// +kubebuilder:validation:XValidation:rule="self.source.type != 'oci' || has(self.source.oci)",message="source.oci is required when source.type=oci"
// +kubebuilder:validation:XValidation:rule="!has(self.maxActive) || self.maxActive >= 1",message="maxActive must be at least 1"
type PromotionTriggerSpec struct {
	// Suspended pauses source observation and promotionrun creation.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Source configures where artifact changes are observed.
	Source PromotionTriggerSource `json:"source"`
	// PromotionRunTemplate defines the PromotionRun created for a verified artifact.
	PromotionRunTemplate PromotionTriggerTemplate `json:"promotionrunTemplate"`
	// Cooldown is the minimum duration between promotionruns created by this trigger.
	// +kubebuilder:default="30m"
	Cooldown string `json:"cooldown,omitempty"`
	// MaxActive limits concurrently active PromotionRuns created by this trigger.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxActive int32 `json:"maxActive,omitempty"`
	// DryRun records what would be created without creating a PromotionRun.
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`
	// Parameters are source-specific key-value pairs for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PromotionTriggerSource selects the artifact source observed by a PromotionTrigger.
type PromotionTriggerSource struct {
	// Type selects the source backend.
	// +kubebuilder:validation:Enum=oci
	Type string `json:"type"`
	// OCI configures OCI registry tag observation.
	// +optional
	OCI *OCIPromotionTriggerSource `json:"oci,omitempty"`
}

// OCIPromotionTriggerSource configures OCI registry observation.
type OCIPromotionTriggerSource struct {
	// Repository is the OCI repository to observe.
	Repository string `json:"repository"`
	// TagPattern is a regular expression. Only matching tags can create promotionruns.
	// +kubebuilder:validation:MinLength=1
	TagPattern string `json:"tagPattern"`
	// RequireSignature requires a configured verifier to pass before creating a
	// PromotionRun. Defaults to false so triggers do not fail closed unless a
	// signature policy is intentionally enabled.
	// +kubebuilder:default=false
	RequireSignature bool `json:"requireSignature,omitempty"`
	// PollInterval controls how often the source is checked.
	// +kubebuilder:default="5m"
	PollInterval string `json:"pollInterval,omitempty"`
	// SecretRef references registry credentials.
	// Cluster-scoped triggers must include the Secret namespace.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// PromotionTriggerTemplate defines the PromotionRun created from a verified artifact.
type PromotionTriggerTemplate struct {
	// NameTemplate controls the created PromotionRun name. Empty means the controller
	// derives a deterministic name from trigger name and artifact digest.
	// +optional
	NameTemplate string `json:"nameTemplate,omitempty"`
	// PromotionPlans is copied into PromotionRun.spec.promotionplans.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	PromotionPlans []PromotionPlanRef `json:"promotionplans"`
	// Suspended controls PromotionRun.spec.suspended on created PromotionRuns.
	// Defaults to true so detection does not equal deployment.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts created PromotionRuns to a subset of clusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is copied into PromotionRun.spec.timeout.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Labels are added to created PromotionRuns.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are added to created PromotionRuns.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PromotionTriggerStatus records observed source state and created promotionruns.
type PromotionTriggerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// LastCheckedAt is the last time the source was checked.
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	// LastTriggeredAt is the last time a PromotionRun was created.
	LastTriggeredAt string `json:"lastTriggeredAt,omitempty"`
	// LastArtifact is the most recent artifact observed by the trigger.
	LastArtifact *PromotionTriggerArtifact `json:"lastArtifact,omitempty"`
	// ActivePromotionRuns lists non-terminal PromotionRuns created by this trigger.
	ActivePromotionRuns []string `json:"activePromotionRuns,omitempty"`
	// ActivePromotionRunCount is the number of non-terminal PromotionRuns created by this trigger.
	ActivePromotionRunCount int32 `json:"activePromotionRunCount,omitempty"`
	// Conditions summarize readiness, suspension, verification, and promotionrun creation.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PromotionTriggerArtifact identifies an observed immutable artifact.
type PromotionTriggerArtifact struct {
	// Tag is the source tag that matched the trigger pattern.
	Tag string `json:"tag,omitempty"`
	// Digest is the immutable artifact digest.
	Digest string `json:"digest,omitempty"`
	// Version is the value copied into PromotionRun.spec.version.
	Version string `json:"version,omitempty"`
	// ObservedAt is the RFC3339 time this artifact was observed.
	ObservedAt string `json:"observedAt,omitempty"`
	// SignatureVerified records whether signature policy passed.
	SignatureVerified bool `json:"signatureVerified,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=reltrig,categories=kapro-all
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
// +kubebuilder:printcolumn:name="LastVersion",type=string,JSONPath=`.status.lastArtifact.version`,priority=0
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activePromotionRunCount`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionTrigger observes verified artifact changes and creates PromotionRun objects.
// It is safe by default: triggers are suspended by default, created PromotionRuns are
// suspended by default, and OCI signature verification defaults to required.
type PromotionTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionTriggerSpec   `json:"spec,omitempty"`
	Status            PromotionTriggerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionTrigger `json:"items"`
}

// ---- Per-target execution ---------------------------------------------------

// TargetStatus records the rollout state of one target cluster rollout. It is
// used as the status payload of PromotionTarget and retained here as the
// controller's in-memory execution shape.
type TargetStatus struct {
	// PromotionRunRef is the owning PromotionRun name.
	PromotionRunRef string `json:"promotionrunRef,omitempty"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PromotionPlanRef is the logical promotionplan reference name from PromotionRun.spec.promotionplans[i].name.
	// Used to disambiguate when the same PromotionPlan CRD is referenced multiple times.
	PromotionPlanRef string `json:"promotionplanRef,omitempty"`
	// PromotionPlan is the PromotionPlan CRD name this entry belongs to.
	PromotionPlan string `json:"promotionplan"`
	// Stage is the stage name within the PromotionPlan.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in FleetCluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// When set, the actuator must converge all of these versions before the target completes.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// BackendObjects records the backend-native objects this target expects to
	// converge, for example Argo CD Applications selected by a label selector.
	// It is status evidence only; backend adapters own the actual resources.
	// +optional
	BackendObjects []BackendObjectStatus `json:"backendObjects,omitempty"`
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
	// +kubebuilder:validation:MaxItems=16
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
	// MissingMCCount tracks consecutive reconciles where the FleetCluster was not found.
	// When it reaches missingMCFailThreshold the target is transitioned to Failed.
	MissingMCCount int `json:"missingMCCount,omitempty"`
	// HeartbeatStaleSince records when the target's FleetCluster heartbeat first
	// became stale. Used to implement a configurable timeout — if the heartbeat
	// remains stale for longer than the threshold, the target is failed.
	// Reset when the heartbeat becomes fresh again.
	// +optional
	HeartbeatStaleSince string `json:"heartbeatStaleSince,omitempty"`
	// HeartbeatStaleCount tracks consecutive reconciles that observed a stale
	// FleetCluster heartbeat. The target fails only after both the stale timeout
	// and the consecutive observation threshold are reached.
	// +optional
	HeartbeatStaleCount int `json:"heartbeatStaleCount,omitempty"`
}

// BackendObjectStatus reports the health of one backend-native object expected
// to converge for a PromotionTarget.
type BackendObjectStatus struct {
	// APIVersion is the backend object's API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// Kind is the backend object's kind.
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the backend object's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the backend object's name.
	// +optional
	Name string `json:"name,omitempty"`
	// Unit is the PromotionSource/promotionrun unit this object belongs to.
	// +optional
	Unit string `json:"unit,omitempty"`
	// DesiredVersion is the revision Kapro expects this object to run.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`
	// CurrentVersion is the revision currently reported by the backend object.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`
	// SyncStatus is the backend sync status when available.
	// +optional
	SyncStatus string `json:"syncStatus,omitempty"`
	// HealthStatus is the backend health status when available.
	// +optional
	HealthStatus string `json:"healthStatus,omitempty"`
	// Phase summarizes this object's convergence state.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message gives a short diagnostic when the object is not converged.
	// +optional
	Message string `json:"message,omitempty"`
}

// PromotionTargetSpec defines the immutable identity and desired intent for one
// target rollout entry within a PromotionRun.
type PromotionTargetSpec struct {
	// PromotionRunRef is the owning PromotionRun name.
	PromotionRunRef string `json:"promotionrunRef"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PromotionPlanRef is the logical promotionplan reference name from PromotionRun.spec.promotionplans[i].name.
	PromotionPlanRef string `json:"promotionplanRef,omitempty"`
	// PromotionPlan is the PromotionPlan CRD name this entry belongs to.
	PromotionPlan string `json:"promotionplan"`
	// Stage is the stage name within the PromotionPlan.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in FleetCluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// Rollback is true when this entry was created by a rollback trigger.
	Rollback bool `json:"rollback,omitempty"`
	// Cancelled is set by the parent PromotionRunReconciler to signal that this
	// target should stop progressing (e.g., stage halted due to peer failure).
	// The child PromotionTargetReconciler observes this and transitions to Failed.
	// This avoids cross-controller status writes — parent owns spec, child owns status.
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`
	// CancelledReason explains why the target was cancelled.
	// +optional
	CancelledReason string `json:"cancelledReason,omitempty"`
}

// PromotionTargetStatus is the live execution state for one target rollout.
type PromotionTargetStatus struct {
	TargetStatus `json:",inline"`
	// ObservedGeneration records the PromotionTarget generation last processed by
	// the child reconciler.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions provide the Kubernetes-native readiness/reconciling/stalled contract
	// for this execution object.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DecisionTrace stores the audit trail of AI agent and human decisions
	// for this target's approval gates. Written by the Decision API (webhook
	// server), never by the PromotionTargetReconciler.
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
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.promotionrunRef`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="PromotionPlan",type=string,JSONPath=`.spec.promotionplanRef`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Previous",type=string,JSONPath=`.status.previousVersion`,priority=1
// +kubebuilder:printcolumn:name="Rollback",type=boolean,JSONPath=`.spec.rollback`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionTarget is the child execution resource for one target rollout entry
// within a PromotionRun. It is the authoritative live state store for rollout
// execution and replaces PromotionRun.status.targets as the persistence layer.
type PromotionTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionTargetSpec   `json:"spec,omitempty"`
	Status            PromotionTargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionTarget `json:"items"`
}

// PromotionRunReportSummary is the inline delivery summary stored in
// PromotionRun.status.report. Counters + PendingApprovals only — per-target and
// per-gate detail live authoritatively in child PromotionTarget objects (not
// duplicated here).
type PromotionRunReportSummary struct {
	Phase             PromotionRunPhase `json:"phase,omitempty"`
	Artifact          string            `json:"artifact,omitempty"`
	ResolvedVersion   string            `json:"resolvedVersion,omitempty"`
	StartedAt         string            `json:"startedAt,omitempty"`
	CompletedAt       string            `json:"completedAt,omitempty"`
	Duration          string            `json:"duration,omitempty"`
	TotalTargets      int               `json:"totalTargets,omitempty"`
	SyncedTargets     int               `json:"syncedTargets,omitempty"`
	FailedTargets     int               `json:"failedTargets,omitempty"`
	PendingTargets    int               `json:"pendingTargets,omitempty"`
	RolledBackTargets int               `json:"rolledBackTargets,omitempty"`
	// TotalArtifacts is the number of artifacts in the resolved (merged) artifact list.
	TotalArtifacts int `json:"totalArtifacts,omitempty"`
	// DeltaArtifacts is the number of artifacts explicitly changed by this PromotionRun.
	// For derivedFrom promotionruns, inherited artifacts are excluded.
	DeltaArtifacts int `json:"deltaArtifacts,omitempty"`
	// PendingApprovals lists "<promotionrun>-<ref>" Approval names that are
	// awaiting human signal. Derived from PromotionTarget objects.
	PendingApprovals []string `json:"pendingApprovals,omitempty"`
}

// AuditEntry records the immutable delivery provenance of a completed PromotionRun.
// Stored in PromotionRun.status.auditTrail.
type AuditEntry struct {
	// Artifact is the OCI artifact that was delivered.
	Artifact string `json:"artifact"`
	// PromotionRun is the PromotionRun name.
	PromotionRun string `json:"promotionrun"`
	// DerivedFrom is the parent Artifact name.
	// +optional
	DerivedFrom string `json:"derivedFrom,omitempty"`
	// PromotionRunDerivedFrom is the parent PromotionRun name.
	// +optional
	PromotionRunDerivedFrom string `json:"promotionrunDerivedFrom,omitempty"`
	// ChangedUnits lists the units that changed relative to the parent artifact.
	// +optional
	ChangedUnits []string `json:"changedUnits,omitempty"`
	// Scope lists the target cluster names that were targeted. Nil = full-fleet rollout.
	// +optional
	Scope []string `json:"scope,omitempty"`
	// CompletedAt is when the PromotionRun completed.
	CompletedAt string `json:"completedAt,omitempty"`
}

// ---- Rollout execution ------------------------------------------------------

// TargetPhase is the execution state of one target cluster rollout within a PromotionRun.
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
	// on a gate policy. A skipped target does not block subsequent targets in the promotionplan.
	TargetPhaseSkipped TargetPhase = "Skipped"
)

// ---- Approval ---------------------------------------------------------------

// ApprovalSpec is the human signal that unblocks a waiting target.
//
// Identity is deterministic: one cluster-scoped Approval per (promotionrun, ref)
// pair. The object name is "<promotionrun>-<ref>". For target FSM approvals, ref is
// the stable sync key "<promotionrun>-<promotionplanRef>-<stage>-<target>", so each
// waiting-approval step requires its own approval object.
type ApprovalSpec struct {
	// PromotionRun is the name of the PromotionRun this approval unblocks.
	// +kubebuilder:validation:Required
	PromotionRun string `json:"promotionrun"`
	// Target is the FleetCluster name this approval is for.
	// +kubebuilder:validation:Required
	Target string `json:"target"`
	// Ref identifies the exact approval scope within the PromotionRun. For target FSM
	// approvals this is the stable sync key "<promotionrun>-<promotionplanRef>-<stage>-<target>".
	// External integrators may use another deterministic ref as long as
	// metadata.name is "<promotionrun>-<ref>".
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
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.promotionrun`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Recorded",type=string,JSONPath=`.status.conditions[?(@.type=="Recorded")].status`
// +kubebuilder:printcolumn:name="Approved By",type=string,JSONPath=`.spec.approvedBy`
// +kubebuilder:printcolumn:name="Bypass",type=boolean,JSONPath=`.spec.bypass`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Approval is the human gate signal that unblocks a waiting target rollout.
// Object name convention: "<promotionrun>-<ref>" as a cluster-scoped object.
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

// ---- FleetCluster ----------------------------------------------------------
//
// FleetCluster is the cluster-inventory CRD for Kapro. One object per physical
// cluster in the fleet.
//
// Ownership split:
//   - spec (except desiredVersion/desiredAppKey): written by the platform team
//   - spec.desiredVersion, spec.desiredAppKey: written by the kapro-operator (PromotionRunReconciler)
//   - status: written by the cluster-controller (kapro-cluster-controller on the spoke)
//   - status.bootstrap: written by the hub csrapproval controller during registration

// FleetClusterSpec defines the desired state of a cluster in the Kapro fleet.
type FleetClusterSpec struct {
	// Delivery configures the backend-neutral delivery adapter for this cluster.
	Delivery DeliverySpec `json:"delivery"`

	// HealthCheck configures active health polling for this cluster.
	// +optional
	HealthCheck *HealthCheckSpec `json:"healthCheck,omitempty"`

	// Topology holds hardware and scheduling metadata used by PromotionPlan stage selectors.
	// +optional
	Topology *TargetTopology `json:"topology,omitempty"`

	// DesiredVersion is written by the kapro-operator (PromotionRunReconciler).
	// The cluster-controller polls this field and patches the local delivery system.
	// Deprecated: use DesiredVersions map for multi-artifact promotionruns.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// DesiredAppKey is the key the cluster-controller uses when writing
	// status.currentVersions. Defaults to "default".
	// Deprecated: use DesiredVersions map for multi-artifact promotionruns.
	// +optional
	DesiredAppKey string `json:"desiredAppKey,omitempty"`

	// DesiredVersions is a map of appKey → version written by the kapro-operator.
	// The cluster-controller iterates this map and patches local delivery for each changed entry.
	// This replaces the single DesiredVersion/DesiredAppKey pair for multi-artifact promotionruns.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`

	// Suspend pauses all reconciliation for this cluster.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// ConsecutiveFailureThreshold is the number of consecutive heartbeat
	// misses required before the FleetCluster Ready condition flips to False
	// and the Phase transitions to Unreachable. Defaults to 3 to absorb
	// transient network blips without flapping. Pattern adopted from Sveltos
	// SveltosCluster.spec.consecutiveFailureThreshold.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ConsecutiveFailureThreshold *int32 `json:"consecutiveFailureThreshold,omitempty"`

	// Bootstrap configures one-time cluster self-registration.
	//
	// Protocol (SA-token-mediated CSR; built-in K8s signer; works across any
	// cloud / any cluster / any K8s distribution):
	//
	//  1. Platform team creates this FleetCluster with spec.bootstrap set.
	//     `tokenHash` (when supplied) is an opaque slot identifier — its
	//     value isn't cryptographically verified by the hub today (the
	//     bootstrap ServiceAccount identity is the effective auth factor).
	//     Mutating it counts as a re-bootstrap intent (planned: hub resets
	//     status.bootstrap; see RE-BOOTSTRAP below).
	//
	//  2. The hub FleetClusterBootstrapReconciler provisions a per-cluster
	//     ServiceAccount `kapro-bootstrap-<cluster>` in kapro-system with a
	//     narrowly-scoped ClusterRole (CSR-create only, for this signer
	//     name). It issues a TokenRequest for that SA (default 1h TTL,
	//     audience `kapro-bootstrap`) and writes the rendered kubeconfig
	//     into Secret `kapro-bootstrap-kubeconfig-<cluster>`. The Secret
	//     name is recorded in `status.bootstrap.issuedBootstrapKubeconfig`.
	//
	//  3. The platform team ships that Secret out-of-band to the spoke
	//     cluster (Helm chart mount, kubectl image copy, GitOps, etc.).
	//     The kapro-cluster-controller pod on the spoke mounts the Secret,
	//     generates an ECDSA keypair, and submits a CertificateSigningRequest
	//     to the hub apiserver. The CSR carries:
	//       signerName = kubernetes.io/kube-apiserver-client
	//       Subject.CN = "kapro-cluster:<cluster>"
	//       Subject.O  = "kapro:cluster-controllers"
	//       Usages     = [client auth]
	//     The CSR's `spec.username` is automatically set by the apiserver
	//     to the bootstrap SA's identity
	//     ("system:serviceaccount:kapro-system:kapro-bootstrap-<cluster>").
	//
	//  4. The hub approver validates: (a) signer/CN/O/usages exactly match
	//     above; (b) Username equals the SA we provisioned for this
	//     FleetCluster — preventing a leaked Secret for cluster A from
	//     registering cluster B; (c) spec.bootstrap.expiresAt is in the
	//     future and status.bootstrap.used is false. It then approves the
	//     CSR via UpdateApproval. The K8s kube-controller-manager signs the
	//     cert with the apiserver's own client CA.
	//
	//  5. On approval the hub also creates a long-lived per-cluster
	//     ClusterRole and ClusterRoleBinding for the issued cert identity
	//     (User CN=`kapro-cluster:<cluster>`). Both names are
	//     `kapro:cluster-controller:<cluster>`. resourceNames lock the
	//     access scope to THIS cluster's FleetCluster + its own heartbeat
	//     Lease — a compromised spoke cannot patch a sibling.
	//
	//  6. The spoke uses the signed client cert for steady-state K8s API
	//     calls and rotates it via a renewal CSR before expiry (Username
	//     becomes the cluster cert identity rather than the bootstrap SA;
	//     the approver recognises the renewal class and skips the bootstrap
	//     slot check).
	//
	// RE-BOOTSTRAP (planned, not yet wired): mutating
	// spec.bootstrap.tokenHash signals re-bootstrap intent; the hub will
	// reset status.bootstrap. For now the workaround is to delete and
	// recreate the FleetCluster.
	// +optional
	Bootstrap *FleetClusterBootstrapSpec `json:"bootstrap,omitempty"`
}

// FleetClusterBootstrapSpec holds the one-time registration slot for a
// FleetCluster. See FleetClusterSpec.Bootstrap doc for the full protocol.
type FleetClusterBootstrapSpec struct {
	// TokenHash is an opaque, platform-supplied bootstrap-slot identifier in
	// SHA-256-hex shape (exactly 64 lowercase hex chars). It is NOT a
	// pre-image-of-token check today: the hub controller's effective
	// authorization is the bootstrap ServiceAccount it provisions (see the
	// FleetClusterSpec.Bootstrap protocol). Mutating this value counts as
	// a re-bootstrap intent — a future change will have the hub controller
	// reset status.bootstrap when it observes a TokenHash mutation.
	// Validation pattern remains a SHA-256 hex so existing tooling that
	// pre-computes the hash keeps working unmodified.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +optional
	TokenHash string `json:"tokenHash,omitempty"`

	// ExpiresAt is the absolute UTC time after which this bootstrap slot is invalid.
	// Set explicitly by the platform team for auditability.
	// If empty and TTL is set, the FleetCluster controller computes it on first reconcile.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// TTL is a convenience duration (e.g. "24h") used when ExpiresAt is not set explicitly.
	// The FleetCluster controller writes spec.bootstrap.expiresAt from
	// metadata.creationTimestamp + TTL at creation time and leaves it immutable.
	// +optional
	TTL string `json:"ttl,omitempty"`

	// Labels are applied to bootstrap resources created during registration
	// (ServiceAccount, kubeconfig Secret). Not used for stage selection — use
	// FleetCluster.metadata.labels for that.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// FleetClusterStatus is the observed state — written by cluster-controller and hub.
type FleetClusterStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ClusterPhase       `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`

	// Version is the primary deployed version (first entry in CurrentVersions).
	// Shown in kubectl/k9s printcolumns for quick fleet overview.
	// +optional
	Version string `json:"version,omitempty"`

	// Provider identifies how this cluster is managed (e.g. "gcp-fleet", "kubeconfig").
	// +optional
	Provider string `json:"provider,omitempty"`

	// CurrentVersions maps app keys to deployed versions. Written by cluster-controller.
	// +optional
	CurrentVersions map[string]string `json:"currentVersions,omitempty"`

	// DeliverySystem is the delivery system detected by the cluster-controller (e.g. "flux").
	// +optional
	DeliverySystem string `json:"deliverySystem,omitempty"`

	// Health aggregates workload health. Written by cluster-controller.
	// +optional
	Health ClusterHealth `json:"health,omitempty"`

	// ActivePromotionRun is the PromotionRun currently being processed for this cluster.
	// +optional
	ActivePromotionRun string `json:"activePromotionRun,omitempty"`

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
	Bootstrap *FleetClusterBootstrapStatus `json:"bootstrap,omitempty"`
}

// FleetClusterBootstrapStatus tracks the one-time bootstrap registration state.
// Written by the hub FleetClusterBootstrapReconciler — first when it provisions
// the bootstrap SA + kubeconfig Secret, then again when a matching CSR is
// approved.
type FleetClusterBootstrapStatus struct {
	// Used is true once a matching CSR has been approved and the per-cluster
	// long-lived RBAC has been created. Enforces one-bootstrap-slot-per-
	// FleetCluster: a second CSR matching this slot but with a different
	// BoundCSRName is denied as a replay attempt.
	Used bool `json:"used,omitempty"`

	// UsedAt is when the bootstrap slot was consumed (the first matching CSR
	// approved).
	// +optional
	UsedAt *metav1.Time `json:"usedAt,omitempty"`

	// IssuedCredentialFor is the cluster name the bootstrap credential was
	// issued for — mirrors metadata.name on a successful registration. Serves
	// as a defensive cross-check when the hub controller later loads RBAC by
	// deterministic name.
	// +optional
	IssuedCredentialFor string `json:"issuedCredentialFor,omitempty"`

	// IssuedBootstrapKubeconfig is the name of the kubeconfig Secret in
	// kapro-system (`kapro-bootstrap-kubeconfig-<cluster>`) that the hub
	// controller provisioned for the spoke to mount. It is populated on every
	// (re-)provisioning pass and is the spoke's input for its first CSR
	// submission. The Secret carries a TokenRequest-issued bearer token whose
	// expiry is recorded in the Secret annotation
	// `kapro.io/bootstrap-expires-at`; the hub re-issues the Secret when the
	// token nears expiry while spec.bootstrap.expiresAt is still in the future.
	// +optional
	IssuedBootstrapKubeconfig string `json:"issuedBootstrapKubeconfig,omitempty"`

	// BoundCSRName is the CSR that consumed this bootstrap slot. Enables
	// idempotent retry: if status patching crashes after the slot is marked
	// Used but before UpdateApproval succeeds, the next reconcile recognises
	// the same CSR via this field and re-runs the approve step instead of
	// rejecting it as a replay.
	// +optional
	BoundCSRName string `json:"boundCSRName,omitempty"`

	// IssuedClusterRole is the name of the per-cluster long-lived ClusterRole
	// the bootstrap reconciler created (deterministic shape
	// `kapro:cluster-controller:<cluster>`). Recording it makes deletion
	// cascade trivial without re-derivation.
	// +optional
	IssuedClusterRole string `json:"issuedClusterRole,omitempty"`

	// IssuedClusterRoleBinding is the name of the per-cluster long-lived
	// ClusterRoleBinding (same `kapro:cluster-controller:<cluster>` shape as
	// IssuedClusterRole). Same rationale.
	// +optional
	IssuedClusterRoleBinding string `json:"issuedClusterRoleBinding,omitempty"`
}

// IsHeartbeatFresh returns true when the cluster last reported a heartbeat
// within the given timeout window.
func (s *FleetClusterStatus) IsHeartbeatFresh(timeout time.Duration) bool {
	if s.LastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.LastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < timeout
}

// ---- BackendProfile ---------------------------------------------------------

// BackendProfileSpec registers a delivery backend profile that can be selected
// by Kapro or FleetCluster delivery.backendRef.
// +kubebuilder:validation:XValidation:rule="self.driver == \"external\" ? (has(self.pluginRef) && self.pluginRef != \"\") : (!has(self.pluginRef) || self.pluginRef == \"\")",message="pluginRef must be set when driver is external, and must be omitted otherwise"
type BackendProfileSpec struct {
	// Driver identifies the backend implementation family.
	Driver BackendDriver `json:"driver"`
	// Runtime identifies where this backend can run.
	// +kubebuilder:default="Both"
	Runtime BackendRuntime `json:"runtime,omitempty"`
	// PluginRef references a PluginRegistration when driver=external.
	// +optional
	PluginRef string `json:"pluginRef,omitempty"`
	// Discovery configures optional adoption of objects already owned by the
	// backend, for example Argo CD cluster Secrets and Applications.
	// +optional
	Discovery *BackendDiscoverySpec `json:"discovery,omitempty"`
	// Parameters are backend-specific defaults inherited by selected delivery
	// configs unless overridden there.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// BackendDiscoverySpec configures backend-native discovery for migration.
type BackendDiscoverySpec struct {
	// Enabled turns on backend-native discovery.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// ManagementPolicy controls whether Kapro only observes discovered objects
	// or may adopt them for promotion writes.
	// +kubebuilder:validation:Enum=Observe;Adopt
	// +kubebuilder:default="Observe"
	// +optional
	ManagementPolicy string `json:"managementPolicy,omitempty"`
	// Selector limits which backend-native objects Kapro discovers. For Argo CD
	// this selects Applications and cluster Secrets. For Flux this selects
	// Kustomizations and HelmReleases.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// MaxObjects bounds each backend-native list call during discovery. When a
	// list returns more objects than this limit, discovery fails closed and asks
	// the user to narrow the selector. Defaults to 1000.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1000
	// +optional
	MaxObjects int32 `json:"maxObjects,omitempty"`
}

// BackendProfileStatus records backend discovery and compatibility.
type BackendProfileStatus struct {
	ObservedGeneration int64          `json:"observedGeneration,omitempty"`
	Ready              bool           `json:"ready,omitempty"`
	Driver             BackendDriver  `json:"driver,omitempty"`
	Runtime            BackendRuntime `json:"runtime,omitempty"`
	// LastDiscoveryTime records when backend-native discovery last completed or
	// failed for this profile.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`
	// DiscoveredClusters is the number of backend-native clusters seen during
	// discovery, for example Argo CD cluster Secrets.
	// +optional
	DiscoveredClusters int32 `json:"discoveredClusters,omitempty"`
	// DiscoveredApplications is the number of backend-native applications seen
	// during discovery.
	// +optional
	DiscoveredApplications int32 `json:"discoveredApplications,omitempty"`
	// DiscoveredApplicationSets is the number of Argo CD ApplicationSets seen
	// during discovery.
	// +optional
	DiscoveredApplicationSets int32 `json:"discoveredApplicationSets,omitempty"`
	// SelectedObjects is a bounded sample of backend-native objects Kapro can
	// map to promotion units under the current discovery selector.
	// +optional
	SelectedObjects []DiscoveredBackendObject `json:"selectedObjects,omitempty"`
	// SkippedObjects is a bounded sample of backend-native objects Kapro saw
	// but will not promote directly.
	// +optional
	SkippedObjects []DiscoveredBackendObject `json:"skippedObjects,omitempty"`
	// UnsupportedPatterns is a bounded sample of objects that matched discovery
	// but need a different ownership level or an external backend plugin.
	// +optional
	UnsupportedPatterns []DiscoveredBackendObject `json:"unsupportedPatterns,omitempty"`
	// DiscoveryErrors is a bounded sample of non-fatal discovery errors. Fatal
	// errors are also surfaced through the DiscoveryReady condition.
	// +optional
	DiscoveryErrors []string           `json:"discoveryErrors,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// DiscoveredBackendObject describes one backend-native object found during
// BackendProfile discovery. The controller keeps this as bounded status
// evidence; counts remain the source of truth for fleet scale.
type DiscoveredBackendObject struct {
	// APIVersion is the discovered object's API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// Kind is the discovered object's Kubernetes kind.
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the discovered object's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the discovered object's name.
	// +optional
	Name string `json:"name,omitempty"`
	// Pattern identifies the backend-native topology pattern, for example
	// application, applicationset-child, app-of-apps-root, helmrelease, or
	// kustomization.
	// +optional
	Pattern string `json:"pattern,omitempty"`
	// Reason explains why the object was selected, skipped, or unsupported.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Unit is the inferred PromotionSource unit name when available.
	// +optional
	Unit string `json:"unit,omitempty"`
	// VersionField is the field Kapro would write in Adopt mode when available.
	// +optional
	VersionField string `json:"versionField,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bp;backend,categories=kapro-all
// +kubebuilder:printcolumn:name="Driver",type=string,JSONPath=`.spec.driver`
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.spec.runtime`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackendProfile defines a selectable delivery backend. Built-in backends such
// as Flux and Argo are first-party adapters behind this same profile contract.
type BackendProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackendProfileSpec   `json:"spec,omitempty"`
	Status            BackendProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BackendProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackendProfile `json:"items"`
}

// ---- PluginRegistration -----------------------------------------------------

// PluginType identifies which Kapro extension contract a plugin implements.
// +kubebuilder:validation:Enum=actuator;gate;planner
type PluginType string

const (
	// PluginTypeActuator registers an implementation of the Kapro Actuator Interface.
	PluginTypeActuator PluginType = "actuator"
	// PluginTypeGate registers an implementation of the Kapro Gate Interface.
	PluginTypeGate PluginType = "gate"
	// PluginTypePlanner registers an implementation of the Kapro Planner Interface.
	PluginTypePlanner PluginType = "planner"
)

// PluginProtocol identifies how Kapro talks to a registered plugin.
// +kubebuilder:validation:Enum=grpc
type PluginProtocol string

const (
	// PluginProtocolGRPC uses the KAI/KGI/KPI gRPC contracts.
	PluginProtocolGRPC PluginProtocol = "grpc"
)

// PluginRegistrationSpec registers an external actuator, gate, or planner plugin endpoint.
// Runtime dispatch is an opt-in preview enabled with KAPRO_ENABLE_PLUGIN_GATEWAY=true.
type PluginRegistrationSpec struct {
	// Type selects which extension contract the plugin implements.
	Type PluginType `json:"type"`
	// Name is the registry key exposed by this plugin, for example "argo/pull"
	// or "slo".
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Protocol selects the wire protocol.
	// +kubebuilder:default="grpc"
	Protocol PluginProtocol `json:"protocol,omitempty"`
	// Endpoint is the plugin endpoint URI, for example
	// dns:///argocd-actuator.kapro-system.svc:9090.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// Timeout bounds one plugin call.
	// +kubebuilder:default="10s"
	Timeout string `json:"timeout,omitempty"`
	// TLSSecretRef references a Secret containing client TLS material or CA data.
	// Cluster-scoped registrations must include the Secret namespace.
	// +optional
	TLSSecretRef *corev1.SecretReference `json:"tlsSecretRef,omitempty"`
	// Parameters are plugin-specific key-value pairs.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PluginRegistrationStatus records plugin discovery and readiness.
type PluginRegistrationStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Ready indicates whether the plugin endpoint is currently usable.
	Ready bool `json:"ready,omitempty"`
	// LastSeen is the RFC3339 time of the last successful health or capability check.
	LastSeen string `json:"lastSeen,omitempty"`
	// Version is the plugin-reported implementation version.
	Version string `json:"version,omitempty"`
	// ContractVersion is the plugin-reported KAI, KGI, or KPI contract version.
	ContractVersion string `json:"contractVersion,omitempty"`
	// Capabilities are plugin-reported feature names.
	Capabilities []string `json:"capabilities,omitempty"`
	// SchemaHash is a sha256 of (contractVersion + sorted capabilities). The
	// reconciler uses it to detect schema drift between hot-reloads of the same
	// PluginRegistration: when the plugin reports a different set of
	// capabilities or contract version than the previously-stored hash, a
	// SchemaChanged condition is surfaced and an event emitted so operators
	// can notice silent breaking changes from plugin upgrades.
	// +optional
	SchemaHash string `json:"schemaHash,omitempty"`
	// Conditions summarize plugin registration readiness.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=pluginreg,categories=kapro-all
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PluginRegistration declares an external actuator, gate, or planner plugin endpoint.
// It is an API preview. Runtime registration is opt-in and hot-loaded after
// readiness probes succeed.
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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mc;fc;fleetcluster,categories=kapro-all
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Registered",type=string,JSONPath=`.status.conditions[?(@.type=="Registered")].status`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Healthy",type=boolean,JSONPath=`.status.health.allWorkloadsReady`
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.status.activePromotionRun`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.status.capabilities.region`,priority=1
// +kubebuilder:printcolumn:name="Cloud",type=string,JSONPath=`.status.capabilities.cloud`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FleetCluster represents one physical cluster in the Kapro fleet.
// It merges delivery config, fleet registration state,
// and BootstrapToken (one-time registration credential) into a single resource.
//
// Labels on FleetCluster drive PromotionPlan stage selection (tier, region, wave, cloud, etc.).
type FleetCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FleetClusterSpec   `json:"spec,omitempty"`
	Status            FleetClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetCluster `json:"items"`
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
	// BlastRadius caps the maximum fleet impact per PromotionRun.
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
	// ExcludeClusters is an explicit denylist of FleetCluster names.
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
	// the agent may approve in a single PromotionRun.
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

// ---- PromotionSource ---------------------------------------------------------------

// PromotionSourceSpec defines the native promotion units Kapro can move
// through a fleet. Units may map to generated Flux resources in greenfield mode
// or to backend-native objects discovered from Argo/Flux in native mode.
// Referenced by Kapro.spec.sourceRef.
type PromotionSourceSpec struct {
	// BackendRef is the BackendProfile this source is normally discovered from
	// or packaged for. Kapro uses it as metadata; delivery still comes from
	// Kapro.spec.delivery and FleetCluster.spec.delivery.
	// +optional
	BackendRef string `json:"backendRef,omitempty"`
	// Registries defines HelmRepository sources for generated Flux resources.
	// +optional
	Registries []SourceRegistry `json:"registries,omitempty"`
	// Units defines the native deployable units Kapro promotes.
	// +kubebuilder:validation:MinItems=1
	Units []PromotionUnit `json:"units"`
	// Defaults are inherited by every unit unless overridden.
	// +optional
	Defaults *SourceDefaults `json:"defaults,omitempty"`
	// Overrides are per-cluster or per-label value patches layered on top of defaults.
	// +optional
	Overrides []SourceOverride `json:"overrides,omitempty"`
	// HelmReleaseNamespace is where HelmRelease CRs live on spokes (not the workloads).
	// +kubebuilder:default="flux-system"
	HelmReleaseNamespace string `json:"helmReleaseNamespace,omitempty"`
}

// SourceRegistry defines a Helm chart source. Generates a HelmRepository on spoke.
type SourceRegistry struct {
	// Name is the registry identifier referenced by units via repo field.
	Name string `json:"name"`
	// URL is the Helm repository URL (OCI or HTTPS).
	// Supports ${variable} substitution (e.g. oci://${gcpArtifactRegistry}/helm/ldl).
	URL string `json:"url"`
	// Type is "oci" (auto-detected for oci:// URLs) or "default" (HTTPS).
	// +optional
	Type string `json:"type,omitempty"`
	// Provider is the auth provider: "generic" (default), "gcp", "aws", "azure".
	// "gcp" uses Workload Identity — no credentials needed.
	// +kubebuilder:default="generic"
	// +optional
	Provider string `json:"provider,omitempty"`
	// Interval is how often to check for new chart versions.
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`
}

// SourceDefaults are inherited by every unit unless overridden at unit level.
type SourceDefaults struct {
	// Repo is the default registry name (from spec.registries).
	// +optional
	Repo string `json:"repo,omitempty"`
	// TargetNamespace is where workload pods run. Supports ${variable} substitution.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Timeout for install and upgrade operations.
	// +kubebuilder:default="10m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Retries is the number of install/upgrade retry attempts.
	// +kubebuilder:default=3
	// +optional
	Retries int32 `json:"retries,omitempty"`
	// ValuesFrom references ConfigMaps/Secrets with Helm values applied to all units.
	// +optional
	ValuesFrom []ValuesReference `json:"valuesFrom,omitempty"`
	// Values are base Helm values applied to every unit (deep-merged with unit values).
	// +optional
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// PromotionUnit is one deployable unit within a PromotionSource.
// It can describe a generated Helm unit for greenfield scaffolds or an existing
// backend-native object discovered from Argo/Flux.
type PromotionUnit struct {
	// Name is the stable Kapro unit identifier.
	Name string `json:"name"`
	// BackendKind identifies the backend-native object kind when this unit maps
	// to an existing object, for example Application, ApplicationSet,
	// Kustomization, or HelmRelease.
	// +optional
	BackendKind string `json:"backendKind,omitempty"`
	// Namespace is the backend-native object namespace when applicable.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// VersionField is the backend-native field Kapro changes for this unit,
	// for example spec.source.targetRevision for Argo CD Applications.
	// +optional
	VersionField string `json:"versionField,omitempty"`
	// SourcePath is the repo-relative file path Kapro updates for Git-native
	// brownfield promotion. It is required for file-backed units whose
	// VersionField does not already include a file path.
	// +optional
	SourcePath string `json:"sourcePath,omitempty"`
	// Version is the default chart/revision for generated units. Supports
	// ${VARIABLE} substitution from cluster-vars.
	// +optional
	Version string `json:"version,omitempty"`
	// Repo references a registry from spec.registries by name. Inherits from defaults if empty.
	// +optional
	Repo string `json:"repo,omitempty"`
	// ChartName overrides the Helm chart name when different from unit name.
	// Example: unit "keycloak" uses chart "keycloakx".
	// +optional
	ChartName string `json:"chartName,omitempty"`
	// TargetNamespace is where workload pods run on spoke. Inherits from defaults if empty.
	// Supports ${variable} substitution.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Wave controls deployment ordering (lower = earlier). Units in the same wave
	// deploy in parallel. Wave N waits for wave N-1 to be Ready.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Wave int32 `json:"wave,omitempty"`
	// DependsOn lists unit names that must be Ready before this one starts.
	// Creates HelmRelease-level dependsOn within the same wave.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// Values are inline Helm values. Deep-merged with defaults.values (unit wins on conflict).
	// +optional
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
	// ValuesFrom references ConfigMaps/Secrets with Helm values.
	// When set, REPLACES defaults.valuesFrom (not appended).
	// +optional
	ValuesFrom []ValuesReference `json:"valuesFrom,omitempty"`
	// Timeout for install and upgrade. Inherits from defaults if empty.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Retries for install/upgrade remediation. Inherits from defaults if empty.
	// +optional
	Retries *int32 `json:"retries,omitempty"`
	// Prune controls whether Flux deletes resources when removed. Default: true.
	// Set to false for databases, Kafka, PVCs.
	// +optional
	Prune *bool `json:"prune,omitempty"`
	// CRDs controls CRD install policy: "Skip" (default), "Create", "CreateReplace".
	// +kubebuilder:validation:Enum=Skip;Create;CreateReplace
	// +optional
	CRDs string `json:"crds,omitempty"`
	// Suspend pauses reconciliation for this unit.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ValuesReference references a ConfigMap or Secret for Helm values.
type ValuesReference struct {
	// Kind is "ConfigMap" (default) or "Secret".
	// +kubebuilder:default="ConfigMap"
	// +optional
	Kind string `json:"kind,omitempty"`
	// Name of the ConfigMap or Secret.
	Name string `json:"name"`
	// ValuesKey is the data key to use. Default: "values.yaml".
	// +optional
	ValuesKey string `json:"valuesKey,omitempty"`
	// Optional marks this values source as non-required.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// SourceOverride patches Helm values for a subset of clusters.
type SourceOverride struct {
	// Selector matches clusters by labels. Applied to all matching clusters.
	// +optional
	Selector map[string]string `json:"selector,omitempty"`
	// Clusters is an explicit list of cluster names. Takes precedence over selector.
	// +optional
	Clusters []string `json:"clusters,omitempty"`
	// Unit restricts this override to a single unit. Empty means all.
	// +optional
	Unit string `json:"unit,omitempty"`
	// Values are Helm value patches merged on top of defaults.
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ps;source;sources,categories=kapro-all
// +kubebuilder:printcolumn:name="Units",type=integer,JSONPath=`.metadata.annotations.kapro\.io/unit-count`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionSource defines the units Kapro promotes. It is the source/app-unit
// contract for both generated greenfield layouts and native Argo/Flux layouts.
type PromotionSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionSourceSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionSource `json:"items"`
}

// ---- Kapro ------------------------------------------------------------------

// KaproSpec defines the desired state of a Kapro fleet.
type KaproSpec struct {
	// Registry is the OCI registry URL for generated pull-mode artifacts.
	// Native Argo/Flux sources may omit it when no Kapro-packaged artifact is used.
	// +optional
	Registry KaproRegistry `json:"registry,omitempty"`
	// SourceRef is the name of the PromotionSource that defines units to deploy.
	SourceRef string `json:"sourceRef"`
	// Delivery selects the backend-neutral fleet delivery profile.
	Delivery DeliverySpec `json:"delivery"`
	// Clusters defines the target clusters in the fleet.
	// +kubebuilder:validation:MinItems=1
	Clusters []KaproCluster `json:"clusters"`
	// PromotionPlan defines the progressive delivery stages.
	PromotionPlan KaproPromotionPlan `json:"promotionplan"`
	// Suspended pauses Kapro reconciliation.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// KaproRegistry configures the OCI registry used by FluxInstance on spokes.
type KaproRegistry struct {
	// URL is the OCI registry URL (e.g. oci://europe-west1-docker.pkg.dev/project/repo)
	URL string `json:"url"`
	// Provider is the auth provider (generic, gcp, aws, azure).
	// +kubebuilder:default="generic"
	Provider string `json:"provider,omitempty"`
	// SecretRef references a Secret for registry auth (pushed to spokes).
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// KaproCluster defines a spoke cluster in the fleet.
type KaproCluster struct {
	// Name is the cluster identifier.
	Name string `json:"name"`
	// Labels for stage selection.
	Labels map[string]string `json:"labels"`
	// Provider is "kubeconfig" (default), "gcp", or "gcp-fleet".
	// gcp-fleet uses Fleet API for discovery + WI auth via gke-gcloud-auth-plugin.
	// +kubebuilder:validation:Enum=kubeconfig;gcp;gcp-fleet
	// +kubebuilder:default="kubeconfig"
	Provider string `json:"provider,omitempty"`
	// KubeconfigSecret references the kubeconfig Secret name (provider=kubeconfig).
	// For gcp/gcp-fleet providers, this is auto-generated by the controller.
	// +optional
	KubeconfigSecret string `json:"kubeconfigSecret,omitempty"`
	// GCP config (provider=gcp or gcp-fleet).
	// +optional
	GCP *KaproClusterGCP `json:"gcp,omitempty"`
}

// KaproClusterGCP holds GCP-specific cluster config.
type KaproClusterGCP struct {
	// Project is the GCP project ID containing the spoke cluster.
	Project string `json:"project"`
	// ClusterName is the GKE cluster name (for gcp provider).
	// For gcp-fleet, this is resolved from Fleet membership.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`
	// Region is the GKE cluster location (zone or region).
	// +optional
	Region string `json:"region,omitempty"`
}

// KaproPromotionPlan defines the progressive delivery stages.
type KaproPromotionPlan struct {
	// Stages defines the progressive delivery wave ordering.
	Stages []KaproStage `json:"stages"`
}

// KaproStage is one wave in the delivery promotionplan.
type KaproStage struct {
	// Name of the stage.
	Name string `json:"name"`
	// Selector matches clusters by labels.
	Selector map[string]string `json:"selector"`
	// DependsOn declares upstream stage dependencies.
	// +optional
	DependsOn []StageDependency `json:"dependsOn,omitempty"`
	// Gate defines approval/soak/metrics requirements for this stage.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
}

// KaproStatus defines the observed state of Kapro.
type KaproStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	// ClusterCount is the number of clusters in the fleet.
	ClusterCount int32 `json:"clusterCount,omitempty"`
	// ConvergedCount is the number of clusters where all HelmReleases are Ready.
	ConvergedCount int32 `json:"convergedCount,omitempty"`
	// UnitCount is the number of units from the resolved PromotionSource.
	UnitCount int32 `json:"unitCount,omitempty"`
	// Version is the current primary unit version being deployed.
	// +optional
	Version string `json:"version,omitempty"`
	// Inventory lists the generated spoke resources (FluxInstance, OCIRepository names).
	// +optional
	Inventory []string `json:"inventory,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=kp,categories=kapro-all
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="SourceRef",type=string,JSONPath=`.spec.sourceRef`
// +kubebuilder:printcolumn:name="Clusters",type=integer,JSONPath=`.status.clusterCount`
// +kubebuilder:printcolumn:name="Converged",type=integer,JSONPath=`.status.convergedCount`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Kapro is the single entry point for fleet delivery. Users reference a
// PromotionSource, select a backend profile, and define clusters and promotion
// stages. Backend adapters own Flux, Argo, or other delivery-system details.
type Kapro struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KaproSpec   `json:"spec,omitempty"`
	Status            KaproStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KaproList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kapro `json:"items"`
}

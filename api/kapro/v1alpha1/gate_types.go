// Gate policy, gate template, metric analysis, and verification policy
// types embedded inside Plan stages.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// ---- GatePolicy -------------------------------------------------------------

type GateMode string

const (
	GateModeAuto      GateMode = "auto"
	GateModeManual    GateMode = "manual"
	GateModeScheduled GateMode = "scheduled"
)

// +kubebuilder:validation:XValidation:rule="has(self.mode)",message="mode is required"
// +kubebuilder:validation:XValidation:rule="!has(self.expressionRef)",message="expressionRef is reserved until external gate expression resolution is implemented"
type GatePolicySpec struct {
	// ExpressionRef is reserved for future external gate expression resolution.
	// v0.6.0 rejects this field so referenced gates cannot become a silent no-op.
	// +kubebuilder:validation:MinLength=1
	// +optional
	ExpressionRef string `json:"expressionRef,omitempty"`
	// Mode controls how this stage gate is evaluated.
	// auto evaluates configured gate checks without human approval.
	// manual waits only when approval.required=true.
	// scheduled is reserved for time-windowed gates.
	// +kubebuilder:validation:Enum=auto;manual;scheduled
	// +optional
	Mode GateMode `json:"mode,omitempty"`
	// Gate configures automated checks such as soak time, health checks,
	// metrics, template gates, and delegated verification policy.
	// +optional
	Gate GateSpec `json:"gate,omitempty"`
	// Approval configures the human approval requirement for manual gates.
	// +optional
	Approval *ApprovalConfig `json:"approval,omitempty"`
	// OnFailure controls what Fleet does when a gate fails or times out.
	//   halt (default): stop the rollout for this target and wait for human intervention.
	//     Use for checkout systems where automated rollback is too risky.
	//   rollback: automatically revert to the previous version.
	//     Only effective when a previous successful apply exists (PreviousVersion is set).
	//   continue: mark the gate as skipped and advance to the next phase.
	// +kubebuilder:validation:Enum=halt;rollback;continue
	// +kubebuilder:default=halt
	OnFailure string `json:"onFailure,omitempty"`
	// Notifications configures stage-level notification targets for gate
	// outcomes. Notification delivery is best-effort and does not decide the
	// gate result.
	// +optional
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

// VerificationGateSpec configures verification policy that Kapro delegates to
// the configured delivery substrate.
type VerificationGateSpec struct {
	CosignPolicy *CosignPolicySpec `json:"cosignPolicy,omitempty"`
}

// CosignPolicySpec records the cosign policy the delivery substrate should
// enforce for the artifact.
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
	// Preset references Plan.spec.metricPresets by name.
	// Preset fields are copied first; inline fields override the preset when set.
	// +optional
	Preset string `json:"preset,omitempty"`
	// Provider selects the metrics substrate. Required when preset is empty.
	// +optional
	Provider string `json:"provider,omitempty"`
	// Query is a PromQL expression. When threshold or analysis is configured,
	// that configuration decides pass/fail. Without threshold or analysis, a
	// truthy/non-zero query result passes the gate.
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
	// analysis before Fleet can pass or fail the gate.
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
	// Required makes this gate wait for an Approval before the target can apply.
	Required bool `json:"required"`
	// Approvers lists identities allowed to approve this gate. Empty means the
	// default approval policy decides who can approve.
	// +optional
	Approvers []string `json:"approvers,omitempty"`
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
	// Type dispatches to a built-in, plugin-backed, or in-process registered gate.
	// +kubebuilder:validation:MinLength=1
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

// PluginGateSpec references an external gate registered through Plugin.
type PluginGateSpec struct {
	// Name is Plugin.spec.name for a ready gate plugin.
	Name string `json:"name"`
}

// GateRunStatus is Fleet's authoritative snapshot of one gate evaluation.
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
	// It is intended for audit, debugging, and notifications.
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

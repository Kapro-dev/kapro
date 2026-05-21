// Gate policy, gate template, metric analysis, and cosign verification
// types embedded inside Plan stages.
package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
)

// ---- GatePolicy -------------------------------------------------------------

// GatePolicySpec is the flat gate configuration block embedded inside Stage
// in v1alpha2. Two layers of nesting (`gate.mode` + `gate.gate.*`) were
// folded into one in this version per ADR-0008. Mode is now derived from
// presence: `approvers:` set ⇒ manual; otherwise auto.
//
// Inspired by Flagger Canary.spec.analysis: flat keys, no enum wrapper.
type GatePolicySpec struct {
	// Soak is the minimum duration a stage's targets must stay healthy
	// before the stage advances. Was `gate.gate.soakTime` in v1alpha1.
	// +optional
	Soak string `json:"soak,omitempty"`
	// GateTimeout is the maximum duration the gate may remain un-passed
	// before targets are failed. Empty = retry indefinitely.
	// +optional
	GateTimeout string `json:"gateTimeout,omitempty"`
	// HealthCheck enables the basic workload-readiness check. Was
	// `gate.gate.healthCheck` in v1alpha1.
	// +optional
	HealthCheck bool `json:"healthCheck,omitempty"`
	// Metrics is the list of metric gates. Order is preserved (sequential
	// evaluation). Was `gate.gate.metrics` in v1alpha1.
	// +optional
	Metrics []MetricGate `json:"metrics,omitempty"`
	// Templates is the list of inline gate templates (cel/job/webhook/plugin).
	// Was `gate.gate.templates` in v1alpha1.
	// +optional
	Templates []GateTemplateSpec `json:"templates,omitempty"`
	// Verification is the per-policy artifact signature gate. Was
	// `gate.gate.verification` in v1alpha1.
	// +optional
	Verification *VerificationGateSpec `json:"verification,omitempty"`
	// Approvers is the list of usernames or group names whose approval
	// unlocks the gate. PRESENCE OF THIS FIELD implies manual mode in
	// v1alpha2 — the explicit `mode:` enum was removed because the data
	// already encodes the mode.
	// +optional
	Approvers []string `json:"approvers,omitempty"`
	// OnFailure controls what Fleet does when a gate fails or times out.
	//   halt (default): stop the rollout for this target and wait for human intervention.
	//   rollback: automatically revert to the previous version.
	//   continue: mark the gate as skipped and advance to the next phase.
	// +kubebuilder:validation:Enum=halt;rollback;continue
	// +kubebuilder:default=halt
	// +optional
	OnFailure string `json:"onFailure,omitempty"`
	// Notifications fires per-channel events on gate state changes.
	// +optional
	Notifications []NotificationSpec `json:"notifications,omitempty"`
}

// GateSpec is the v1alpha1 nested gate block; in v1alpha2 every field
// it carried lives directly on GatePolicySpec above. Kept as a transitional
// alias so generated deepcopy doesn't trip; the type itself is unused.
//
// Deprecated: use GatePolicySpec directly in v1alpha2. Removed in the next
// alpha rev.
type GateSpec = GatePolicySpec

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
	// Preset references Plan.spec.metricPresets by name.
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
	Required  bool     `json:"required"`
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

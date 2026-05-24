// Policy CRD plus the TargetDecisionTrace audit types written by the
// Decision API for AI-agent and human override accountability.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Policy ---------------------------------------------------------------

// PolicyMode controls the agent's authority level.
// +kubebuilder:validation:Enum=auto;recommend;disabled
type PolicyMode string

const (
	// PolicyModeAuto allows the agent to create Approval objects autonomously
	// when confidence meets the threshold.
	PolicyModeAuto PolicyMode = "auto"
	// PolicyModeRecommend allows the agent to post a recommendation
	// but a human must still create the Approval object.
	PolicyModeRecommend PolicyMode = "recommend"
	// PolicyModeDisabled suspends the agent entirely.
	PolicyModeDisabled PolicyMode = "disabled"
)

// EscalationAction controls behavior when confidence is below threshold.
// +kubebuilder:validation:Enum=reject;hold;escalate
type EscalationAction string

const (
	EscalationReject   EscalationAction = "reject"
	EscalationHold     EscalationAction = "hold"
	EscalationEscalate EscalationAction = "escalate"
)

// PolicySpec defines the trust boundary for one AI agent identity.
type PolicySpec struct {
	// Identity binds this policy to a specific agent ServiceAccount.
	Identity PolicyIdentity `json:"identity"`
	// Mode controls the agent's authority level.
	// +kubebuilder:default=recommend
	Mode PolicyMode `json:"mode"`
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

// PolicyIdentity binds a policy to a ServiceAccount.
type PolicyIdentity struct {
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
	// ExcludeClusters is an explicit denylist of Cluster names.
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
	Mode *PolicyMode `json:"mode,omitempty"`
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
	// MaxConcurrent is the maximum in-flight Decision API submissions at any time.
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

// PolicyStatus is the observed state of the Policy.
type PolicyStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	// ActiveDecisions is the count of in-flight Decision API submissions by this agent.
	ActiveDecisions int32 `json:"activeDecisions,omitempty"`
	// DecisionsToday is the count of decisions made in the current UTC day.
	DecisionsToday int32 `json:"decisionsToday,omitempty"`
	// LastDecisionAt is the timestamp of the last decision.
	// +optional
	LastDecisionAt string `json:"lastDecisionAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=pol,categories=kapro-all
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="SA",type=string,JSONPath=`.spec.identity.serviceAccountName`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeDecisions`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Policy defines the trust boundary, scope, and guardrails for one AI
// agent identity within the Fleet progressive delivery system.
type Policy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PolicySpec   `json:"spec,omitempty"`
	Status            PolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Policy `json:"items"`
}

// TargetDecisionTrace is the full audit trail of Decision API approval
// decisions for one target. It is stored inline at Target.status.decisionTrace.
// Durable controller decisions are recorded as DecisionTrace CR objects.
// It stores the current decision, historical decisions, and human overrides.
type TargetDecisionTrace struct {
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
	// TrustLevel is the resolved trust level from the Policy.
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

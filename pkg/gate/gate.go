// Package gate defines KGI — the Kapro Gate Interface.
//
// KGI v1alpha1 is the pluggable evaluation contract for delivery gates.
// A gate answers one question: "is it safe to advance this target-cluster rollout right now?"
//
// Built-in implementations live in internal/gate/:
//   - soak.go              — time-based bake period
//   - metrics.go           — Prometheus query evaluation
//   - approval.go          — human approval gate
//   - verification_gate.go — OCI artifact signature verification (cosign)
//   - cel/                 — CEL expression gate
//   - job/                 — Kubernetes Job gate
//   - webhook/             — HTTP webhook gate
//
// External implementations can implement this interface and wire in at startup
// via the gate registry used by the release controller.
//
// # The CRI analogy
//
// Kapro is to gates what Kubernetes is to containers:
//   - Kapro manages gate lifecycle (when, timeout, retry, failure policy)
//   - Gate.Evaluate() is the KGI contract — analogous to CRI's RunPodSandbox
//   - Built-in gates (cel, job, webhook) are like runc — always available
//   - Release.Status.Targets[].Gates[] is like PodStatus.ContainerStatuses[]
//     — Kapro's authoritative state; gates are stateless evaluators only
//
// # Stability
//
// KGI v1alpha1 is stable. The Gate interface and all exported types in this
// package have backwards-compatibility guarantees within a major version.
package gate

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ConditionResult is the per-metric/condition breakdown within a Result.
// Returned when a gate evaluates multiple sub-conditions (e.g. multiple
// Prometheus queries in a MetricsGate).
type ConditionResult struct {
	Name    string                  `json:"name"`
	Phase   kaprov1alpha1.GatePhase `json:"phase"`
	Value   string                  `json:"value,omitempty"`
	Message string                  `json:"message,omitempty"`
	// Evidence explains the facts and analysis behind this condition.
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Evidence is structured, non-secret data that explains a gate decision.
type Evidence struct {
	Type                string      `json:"type,omitempty"`
	Provider            string      `json:"provider,omitempty"`
	AnalysisMode        string      `json:"analysisMode,omitempty"`
	Comparator          string      `json:"comparator,omitempty"`
	Query               string      `json:"query,omitempty"`
	BaselineQuery       string      `json:"baselineQuery,omitempty"`
	BaselineHealthQuery string      `json:"baselineHealthQuery,omitempty"`
	Window              string      `json:"window,omitempty"`
	Interval            string      `json:"interval,omitempty"`
	ObservedValue       string      `json:"observedValue,omitempty"`
	Threshold           string      `json:"threshold,omitempty"`
	BaselineValue       string      `json:"baselineValue,omitempty"`
	BaselineHealthy     *bool       `json:"baselineHealthy,omitempty"`
	SampleCount         int64       `json:"sampleCount,omitempty"`
	Confidence          *float64    `json:"confidence,omitempty"`
	Alpha               *float64    `json:"alpha,omitempty"`
	PValue              *float64    `json:"pValue,omitempty"`
	EffectSize          string      `json:"effectSize,omitempty"`
	Score               *float64    `json:"score,omitempty"`
	DecisionRule        string      `json:"decisionRule,omitempty"`
	Reason              string      `json:"reason,omitempty"`
	Projection          *Projection `json:"projection,omitempty"`
}

// Projection records an optional forecast derived from gate evidence.
type Projection struct {
	Horizon string `json:"horizon,omitempty"`
	Value   string `json:"value,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Result carries the outcome of a gate evaluation.
//
// # Phase is the authoritative outcome field
//
// Implementations MUST set Phase. The controller drives all gate state
// transitions from Result.Phase.
//
// Phase values:
//   - Passed       — gate condition satisfied; rollout may advance
//   - Inconclusive — gate needs more time; controller requeues after RetryAfter
//   - Failed       — gate condition not met; failure policy applies
//   - Running      — gate-managed resource (e.g. Job) is still executing
type Result struct {
	// Phase is the gate outcome. Always set this field.
	// The controller uses Phase as the authoritative state.
	Phase kaprov1alpha1.GatePhase

	// Message is a human-readable explanation shown in rollout status and
	// notifications. Be specific: include metric values, threshold,
	// actual vs expected. Good: "p99 latency 48ms > threshold 40ms".
	Message string

	// RetryAfter is the recommended requeue delay for Inconclusive results.
	// Format: Go duration string (e.g. "30s", "5m").
	// Empty means requeue with the controller's default backoff.
	RetryAfter string

	// VendorRef points to the vendor-managed resource created by this gate
	// (e.g. a Kubernetes Job, a Prometheus recording rule, an AnalysisRun).
	// Nil for in-process gates (cel, webhook, soak).
	// Stored in Release.Status.Targets[].Gates[].VendorRef for observability.
	VendorRef *corev1.ObjectReference

	// Results contains per-condition breakdowns for multi-condition gates
	// (e.g. multiple Prometheus queries in one MetricsGate evaluation).
	Results []ConditionResult

	// Evidence explains the facts and analysis behind the gate decision.
	// Implementations must not include secrets, headers, tokens, or raw webhook
	// payloads in this field.
	Evidence []Evidence
}

// IsPassed returns true when Phase is Passed.
// This is the canonical way to test a gate result.
func (r Result) IsPassed() bool {
	return r.Phase == kaprov1alpha1.GatePhasePassed
}

// IsInconclusive returns true when Phase is Inconclusive.
// The controller requeues after RetryAfter when this returns true.
func (r Result) IsInconclusive() bool {
	return r.Phase == kaprov1alpha1.GatePhaseInconclusive
}

// IsFailed returns true when Phase is Failed.
func (r Result) IsFailed() bool {
	return r.Phase == kaprov1alpha1.GatePhaseFailed
}

// Context is the per-target rollout context passed into gate evaluation.
// It is a runtime value owned by the release controller, not a Kubernetes API object.
type Context struct {
	Name       string
	Namespace  string
	ReleaseRef string
	Target     string
	Pipeline   string
	Stage      string
	Version    string
	StartedAt  string

	// OwnerUID and OwnerName identify the ReleaseTarget that triggered this gate
	// evaluation. Gates that create Kubernetes resources (e.g. Job gate) must set
	// OwnerReferences using these fields so created resources are garbage-collected
	// when the ReleaseTarget is deleted.
	OwnerUID  k8stypes.UID
	OwnerName string
}

// Request carries everything a gate needs to evaluate its condition.
// Gates must not modify any field of Request.
type Request struct {
	// Context is the per-target rollout state being gated.
	// Never nil.
	Context *Context

	// Policy is the resolved gate policy for this sync.
	// May be nil when no gate is configured for the stage.
	Policy *kaprov1alpha1.GatePolicySpec

	// MetricIndex addresses a specific metric in Policy.Gate.Metrics.
	// Meaningful only on the Metrics[] evaluation path.
	MetricIndex int

	// Template is the inline gate template for template-based evaluation.
	// Nil on the Metrics[] path; non-nil on the GateTemplate path.
	Template *kaprov1alpha1.GateTemplateSpec

	// Args carries runtime-injected parameters merged with this precedence:
	//   GateTemplateSpec defaults < sync context (version, target, stage)
	// Nil on the Metrics[] path.
	Args map[string]string
}

// Gate is KGI v1alpha1: the Kapro Gate Interface.
//
// Evaluate returns a Result indicating whether the target rollout may advance.
// The controller persists gate state to Release.status.targets[].gates after each
// evaluation — implementations must not attempt to store state themselves.
//
// Contract:
//   - Implementations MUST set Result.Phase
//   - Evaluate MUST respect ctx.Done() — do not block indefinitely
//   - Evaluate MUST NOT mutate any field of req
//   - Evaluate MUST be safe for concurrent use from multiple goroutines
//   - Evaluate MUST be idempotent for a given (release/env/stage, gate state) tuple
type Gate interface {
	Evaluate(ctx context.Context, req Request) (Result, error)
}

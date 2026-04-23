// Package gate defines KGI — the Kapro Gate Interface.
//
// KGI v1alpha1 is the pluggable evaluation contract for delivery gates.
// A gate answers one question: "is it safe to sync right now?"
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
// via the gateForTemplate dispatch function in the SyncReconciler.
//
// # The CRI analogy
//
// Kapro is to gates what Kubernetes is to containers:
//   - Kapro manages gate lifecycle (when, timeout, retry, failure policy)
//   - Gate.Evaluate() is the KGI contract — analogous to CRI's RunPodSandbox
//   - Built-in gates (cel, job, webhook) are like runc — always available
//   - Sync.Status.Gates[] is like PodStatus.ContainerStatuses[]
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
}

// Result carries the outcome of a gate evaluation.
//
// # Phase is the authoritative outcome field
//
// Implementations MUST set Phase. The controller drives all gate state
// transitions from Result.Phase.
//
// Phase values:
//   - Passed       — gate condition satisfied; Sync may advance
//   - Inconclusive — gate needs more time; controller requeues after RetryAfter
//   - Failed       — gate condition not met; failure policy applies
//   - Running      — gate-managed resource (e.g. Job) is still executing
type Result struct {
	// Phase is the gate outcome. Always set this field.
	// The controller uses Phase as the authoritative state.
	Phase kaprov1alpha1.GatePhase

	// Message is a human-readable explanation shown in Sync.status.conditions
	// and in notifications. Be specific: include metric values, threshold,
	// actual vs expected. Good: "p99 latency 48ms > threshold 40ms".
	Message string

	// RetryAfter is the recommended requeue delay for Inconclusive results.
	// Format: Go duration string (e.g. "30s", "5m").
	// Empty means requeue with the controller's default backoff.
	RetryAfter string

	// VendorRef points to the vendor-managed resource created by this gate
	// (e.g. a Kubernetes Job, a Prometheus recording rule, an AnalysisRun).
	// Nil for in-process gates (cel, webhook, soak).
	// Stored in Sync.Status.Gates[].vendorRef for observability.
	VendorRef *corev1.ObjectReference

	// Results contains per-condition breakdowns for multi-condition gates
	// (e.g. multiple Prometheus queries in one MetricsGate evaluation).
	Results []ConditionResult
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

// Request carries everything a gate needs to evaluate its condition.
// Gates must not modify any field of Request.
type Request struct {
	// Sync is the per-environment delivery object being gated.
	// Never nil.
	Sync *kaprov1alpha1.Sync

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
	//   GateTemplateSpec defaults < sync context (version, env, stage)
	// Nil on the Metrics[] path.
	Args map[string]string
}

// Gate is KGI v1alpha1: the Kapro Gate Interface.
//
// Evaluate returns a Result indicating whether the Sync may advance.
// The controller persists gate state to Sync.Status.Gates[] after each
// evaluation — implementations must not attempt to store state themselves.
//
// Contract:
//   - Implementations MUST set Result.Phase
//   - Evaluate MUST respect ctx.Done() — do not block indefinitely
//   - Evaluate MUST NOT mutate any field of req
//   - Evaluate MUST be safe for concurrent use from multiple goroutines
//   - Evaluate MUST be idempotent for a given (Sync.UID, gate state) pair
type Gate interface {
	Evaluate(ctx context.Context, req Request) (Result, error)
}

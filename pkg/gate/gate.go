// Package gate defines KGI — the Kapro Gate Interface.
//
// KGI is the pluggable evaluation contract for promotion gates.
// A gate answers one question: "is it safe to promote right now?"
//
// Built-in implementations live in internal/gate/:
//   - soak.go              — time-based bake period
//   - metrics.go           — Prometheus / Datadog query evaluation
//   - cel/                 — CEL expression gate (built-in, no external dep)
//   - keda/                — KEDA ScaledObject lag gate
//   - mlflow/              — MLflow model registry gate
//   - argo/                — Argo Rollouts AnalysisRun gate
//   - verification_gate.go — OCI signature verification gate
//
// External implementations register via PluginRegistration CRD and communicate
// over proto/kapro/v1alpha1/gate.proto (gRPC).
//
// # The CRI analogy
//
// Kapro is to gates what Kubernetes is to containers:
//   - Kapro manages gate lifecycle (when, timeout, retry, failure policy)
//   - Gate.Evaluate() is the CRI contract
//   - Built-in gates (cel, job) are like runc — always available
//   - Plugin gates (argo, opa) are like containerd — out-of-process
//   - Promotion.Status.Gates[] is like PodStatus.ContainerStatuses[]
//     — Kapro's authoritative snapshot; vendor resource is the source of truth
package gate

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ConditionResult is the per-metric/condition breakdown within a Result.
type ConditionResult struct {
	Name    string                  `json:"name"`
	Phase   kaprov1alpha1.GatePhase `json:"phase"`
	Value   string                  `json:"value,omitempty"`
	Message string                  `json:"message,omitempty"`
}

// Result carries the outcome of a gate evaluation.
type Result struct {
	// Passed is true when the gate condition is satisfied and the promotion
	// may advance to the next phase.
	Passed bool
	// Message is a human-readable explanation (shown in status conditions).
	Message string
	// RetryAfter is a hint for how long to wait before re-evaluating.
	// Empty string means requeue with default backoff.
	RetryAfter string

	// Phase is the normalised gate phase — populated on the GateTemplate path.
	// Legacy gates that only set Passed/Message may leave this empty;
	// the controller normalises it from Passed.
	Phase kaprov1alpha1.GatePhase
	// VendorRef points to the vendor-managed resource (e.g., AnalysisRun, Job).
	// Nil for in-process gates (cel, webhook).
	VendorRef *corev1.ObjectReference
	// Results contains per-condition breakdowns from the runner.
	Results []ConditionResult
}

// Request carries everything a gate needs to evaluate its condition.
type Request struct {
	// Promotion is the object being gated.
	Promotion *kaprov1alpha1.Promotion
	// Policy is the resolved PromotionPolicy for this promotion.
	Policy *kaprov1alpha1.PromotionPolicy
	// MetricIndex addresses a specific metric in Policy.Spec.Gate.Metrics.
	// Used on the legacy Metrics[] path only.
	MetricIndex int

	// Template is the resolved GateTemplate for template-based evaluation.
	// Nil on the legacy Metrics[] path.
	Template *kaprov1alpha1.GateTemplate
	// Args carries runtime-injected parameters:
	//   template defaults < policy overrides < promotion context (version, env, country)
	// Nil on the legacy Metrics[] path.
	Args map[string]string
}

// Gate is KGI: the Kapro Gate Interface.
//
// Gates are intentionally stateless — all timing/run state is stored on
// Promotion.Status.Gates[] so it survives controller restarts.
// Implementations must be safe for concurrent use.
type Gate interface {
	Evaluate(ctx context.Context, req Request) (Result, error)
}

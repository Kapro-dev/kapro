// Package gate provides pluggable gate implementations for the Kapro promotion
// pipeline. Each gate evaluates a condition and returns a GateResult that the
// promotion controller uses to decide whether to advance the state machine.
//
// Gates are intentionally stateless; all timing state is stored on the
// Promotion.Status so it survives controller restarts.
package gate

import (
	"context"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Result carries the outcome of a gate evaluation.
type Result struct {
	// Passed is true when the gate condition is satisfied and the promotion
	// may advance to the next phase.
	Passed bool
	// Message is a human-readable explanation of the result (shown in conditions).
	Message string
	// RetryAfter is a hint for how long the controller should wait before
	// re-evaluating the gate. Zero means requeue immediately.
	RetryAfter string
}

// Gate is the single interface that every gate implementation must satisfy.
//
//	type Gate interface {
//	    Evaluate(ctx context.Context, req Request) (Result, error)
//	}
type Gate interface {
	Evaluate(ctx context.Context, req Request) (Result, error)
}

// Request carries everything a gate needs to evaluate its condition.
type Request struct {
	// Promotion is the object being gated.
	Promotion *kaprov1alpha1.Promotion
	// Policy is the resolved PromotionPolicy spec for this promotion.
	Policy *kaprov1alpha1.PromotionPolicy
	// MetricIndex is the index into Policy.Spec.Gate.Metrics being evaluated
	// (used by the metrics gate to address the correct query).
	MetricIndex int
}

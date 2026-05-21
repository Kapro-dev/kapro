package kapro

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
)

// Gate is the minimal SDK interface for custom gate evaluators.
type Gate interface {
	Evaluate(ctx context.Context, req GateRequest) (GateResult, error)
}

// GateRequest identifies the PromotionRun, plan, stage, and target being
// evaluated.
type GateRequest struct {
	PromotionRun types.NamespacedName
	Plan         string
	Stage        string
	Target       string
}

// GateResult is the outcome returned by an SDK gate evaluator.
type GateResult struct {
	// Phase is the gate outcome. Common values are "Pending", "Running",
	// "Inconclusive", "Passed", and "Failed".
	Phase   string
	Reason  string
	Message string
}

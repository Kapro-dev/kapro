package gate

import (
	"context"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// VerificationGate is a pass-through gate. Artifact signature verification
// is handled by Flux Operator (cosign verification on OCIRepository).
// This gate exists to satisfy the FSM's Verification phase without blocking.
type VerificationGate struct{}

var _ Gate = &VerificationGate{}

func (g *VerificationGate) Evaluate(_ context.Context, _ Request) (Result, error) {
	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: "verification delegated to Flux Operator",
	}, nil
}

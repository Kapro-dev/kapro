package gate

import (
	"context"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// VerificationGate is a pass-through gate. Artifact signature verification
// is handled by Flux Operator (cosign verification on OCIRepository).
// This gate exists to satisfy the FSM's Verification phase without blocking.
type VerificationGate struct{}

var _ Gate = &VerificationGate{}

func (g *VerificationGate) Evaluate(_ context.Context, _ Request) (Result, error) {
	return Result{
		Phase:   kaprov1alpha2.GatePhasePassed,
		Message: "verification delegated to Flux Operator",
		Evidence: []Evidence{{
			Type:   "verification",
			Reason: "artifact verification is delegated to the configured backend",
		}},
	}, nil
}

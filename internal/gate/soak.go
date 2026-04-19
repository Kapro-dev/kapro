package gate

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// SoakGate blocks the promotion until a minimum duration (soak period) has
// elapsed since the promotion entered the Soaking phase.
//
// The start time is read from Sync.Status.StartedAt so the gate is
// fully restartable: if the controller crashes mid-soak, it resumes from the
// persisted timestamp rather than restarting the clock.
type SoakGate struct{}

// Evaluate returns Passed when the soak period has elapsed.
//
// If Policy.Spec.Gate.SoakTime is empty or un-parseable the gate is
// considered satisfied immediately — callers should guard against calling
// this gate when no soak is configured.
func (g *SoakGate) Evaluate(_ context.Context, req Request) (Result, error) {
	if req.Policy == nil || req.Policy.Spec.Gate.SoakTime == "" {
		return Result{Phase: kaprov1alpha1.GatePhasePassed, Message: "no soak configured"}, nil
	}

	soakDuration, err := time.ParseDuration(req.Policy.Spec.Gate.SoakTime)
	if err != nil {
		return Result{}, fmt.Errorf("soakTime %q is not a valid duration: %w",
			req.Policy.Spec.Gate.SoakTime, err)
	}

	if req.Sync.Status.StartedAt == "" {
		// Clock not started yet; caller must set StartedAt before calling again.
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    "soak clock not started",
			RetryAfter: soakDuration.String(),
		}, nil
	}

	startedAt, err := time.Parse(time.RFC3339, req.Sync.Status.StartedAt)
	if err != nil {
		return Result{}, fmt.Errorf("Sync.Status.StartedAt %q is not RFC3339: %w",
			req.Sync.Status.StartedAt, err)
	}

	elapsed := time.Since(startedAt)
	if elapsed < soakDuration {
		remaining := soakDuration - elapsed
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("soaking: %s remaining", remaining.Round(time.Second)),
			RetryAfter: remaining.String(),
		}, nil
	}

	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: fmt.Sprintf("soak period %s elapsed", soakDuration),
	}, nil
}

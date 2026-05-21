package kapro

import (
	"context"
	"testing"
)

type gateFunc func(context.Context, GateRequest) (GateResult, error)

func (f gateFunc) Evaluate(ctx context.Context, req GateRequest) (GateResult, error) {
	return f(ctx, req)
}

func TestGateInterface(t *testing.T) {
	var gate Gate = gateFunc(func(context.Context, GateRequest) (GateResult, error) {
		return GateResult{Phase: "Passed", Reason: "Ok", Message: "gate passed"}, nil
	})

	result, err := gate.Evaluate(context.Background(), GateRequest{Plan: "progressive", Stage: "dev", Target: "kind-dev"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Phase != "Passed" {
		t.Fatalf("phase = %q", result.Phase)
	}
}

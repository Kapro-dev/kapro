package gate

import (
	"context"
	"testing"

	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
)

func TestRun(t *testing.T) {
	Run(t, fakeGateClient{}, DefaultScenario())
}

type fakeGateClient struct{}

func (fakeGateClient) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return &kgiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "test",
		Capabilities:    []string{"evaluate"},
	}, nil
}

func (fakeGateClient) Evaluate(context.Context, *kgiv1alpha1.EvaluateRequest, ...grpc.CallOption) (*kgiv1alpha1.EvaluateResponse, error) {
	return &kgiv1alpha1.EvaluateResponse{
		Phase:   kgiv1alpha1.GatePhase_GATE_PHASE_PASSED,
		Message: "passed",
	}, nil
}

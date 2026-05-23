package actuator

import (
	"context"
	"strings"
	"testing"

	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
)

func TestRun(t *testing.T) {
	Run(t, fakeActuatorClient{}, DefaultScenario())
}

func TestCheckReportsContextCancellationFailure(t *testing.T) {
	report := Check(context.Background(), contextIgnoringActuatorClient{}, DefaultScenario())
	if report.Passed() {
		t.Fatalf("Check passed for actuator that ignores context cancellation: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "ApplyRespectsContextCancellation" &&
			strings.Contains(result.Message, "nil error") {
			return
		}
	}
	t.Fatalf("Check did not report ApplyRespectsContextCancellation failure: %#v", report.Failed())
}

func TestCheckReportsMissingRequiredCapabilities(t *testing.T) {
	report := Check(context.Background(), missingCapabilitiesActuatorClient{}, DefaultScenario())
	if report.Passed() {
		t.Fatalf("Check passed for actuator with missing capabilities: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "GetCapabilitiesReportsRequiredCapabilities" &&
			strings.Contains(result.Message, "missing required capabilities") {
			return
		}
	}
	t.Fatalf("Check did not report missing capabilities: %#v", report.Failed())
}

type missingCapabilitiesActuatorClient struct {
	fakeActuatorClient
}

func (missingCapabilitiesActuatorClient) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "test",
		Capabilities:    []string{"apply"},
	}, nil
}

type fakeActuatorClient struct{}

func (fakeActuatorClient) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "test",
		Capabilities:    []string{"apply", "convergence", "rollback"},
	}, nil
}

func (fakeActuatorClient) Apply(ctx context.Context, _ *kaiv1alpha1.ApplyRequest, _ ...grpc.CallOption) (*kaiv1alpha1.ApplyResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.ApplyResponse{Accepted: true, Message: "accepted"}, nil
}

func (fakeActuatorClient) IsConverged(context.Context, *kaiv1alpha1.IsConvergedRequest, ...grpc.CallOption) (*kaiv1alpha1.IsConvergedResponse, error) {
	return &kaiv1alpha1.IsConvergedResponse{Converged: true, Message: "converged"}, nil
}

func (fakeActuatorClient) Rollback(context.Context, *kaiv1alpha1.RollbackRequest, ...grpc.CallOption) (*kaiv1alpha1.RollbackResponse, error) {
	return &kaiv1alpha1.RollbackResponse{Accepted: true, Message: "rolled back"}, nil
}

type contextIgnoringActuatorClient struct {
	fakeActuatorClient
}

func (contextIgnoringActuatorClient) Apply(context.Context, *kaiv1alpha1.ApplyRequest, ...grpc.CallOption) (*kaiv1alpha1.ApplyResponse, error) {
	return &kaiv1alpha1.ApplyResponse{Accepted: true, Message: "accepted"}, nil
}

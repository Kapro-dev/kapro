package actuator

import (
	"context"
	"testing"

	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
)

func TestRun(t *testing.T) {
	Run(t, fakeActuatorClient{}, DefaultScenario())
}

type fakeActuatorClient struct{}

func (fakeActuatorClient) Apply(context.Context, *kaiv1alpha1.ApplyRequest, ...grpc.CallOption) (*kaiv1alpha1.ApplyResponse, error) {
	return &kaiv1alpha1.ApplyResponse{Accepted: true, Message: "accepted"}, nil
}

func (fakeActuatorClient) IsConverged(context.Context, *kaiv1alpha1.IsConvergedRequest, ...grpc.CallOption) (*kaiv1alpha1.IsConvergedResponse, error) {
	return &kaiv1alpha1.IsConvergedResponse{Converged: true, Message: "converged"}, nil
}

func (fakeActuatorClient) Rollback(context.Context, *kaiv1alpha1.RollbackRequest, ...grpc.CallOption) (*kaiv1alpha1.RollbackResponse, error) {
	return &kaiv1alpha1.RollbackResponse{Accepted: true, Message: "rolled back"}, nil
}

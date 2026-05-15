package planner

import (
	"context"
	"testing"

	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
)

func TestRun(t *testing.T) {
	Run(t, fakePlannerClient{}, DefaultScenario())
}

type fakePlannerClient struct{}

func (fakePlannerClient) GetCapabilities(context.Context, *kpiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kpiv1alpha1.GetCapabilitiesResponse, error) {
	return &kpiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "test",
		Capabilities:    []string{"filter", "score"},
	}, nil
}

func (fakePlannerClient) Plan(ctx context.Context, req *kpiv1alpha1.PlanRequest, _ ...grpc.CallOption) (*kpiv1alpha1.PlanResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp := &kpiv1alpha1.PlanResponse{
		Targets: make([]*kpiv1alpha1.PlannedTarget, 0, len(req.GetTargets())),
	}
	for _, target := range req.GetTargets() {
		decision := kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE
		if !target.GetReady() {
			decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER
		}
		resp.Targets = append(resp.Targets, &kpiv1alpha1.PlannedTarget{
			Name:     target.GetName(),
			Decision: decision,
			Score:    100,
			Reason:   "conformance",
			Message:  "planned",
		})
	}
	return resp, nil
}

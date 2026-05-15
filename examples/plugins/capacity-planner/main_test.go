package main

import (
	"context"
	"net"
	"reflect"
	"testing"

	plannerconformance "kapro.io/kapro/conformance/planner"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestKPIConformance(t *testing.T) {
	client := newTestClient(t, &capacityPlannerServer{})
	scenario := plannerconformance.DefaultScenario()
	scenario.Plan.Parameters = map[string]string{
		"minAvailableCapacityPercent": "20",
	}
	plannerconformance.Run(t, client, scenario)
}

func TestPlanFiltersDefersOrdersAndLimitsTargets(t *testing.T) {
	resp, err := (&capacityPlannerServer{}).Plan(context.Background(), &kpiv1alpha1.PlanRequest{
		Strategy: &kpiv1alpha1.StageStrategy{MaxParallel: 2},
		Parameters: map[string]string{
			"minAvailableCapacityPercent": "20",
			"requiredLabel.region":        "eu",
		},
		Targets: []*kpiv1alpha1.Target{
			target("gamma", true, "", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "80"}),
			target("alpha", true, "", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "90"}),
			target("beta", true, "", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "70"}),
			target("delta", false, "", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "100"}),
			target("epsilon", true, "release-a", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "100"}),
			target("zeta", true, "", map[string]string{"region": "us", "kapro.io/available-capacity-percent": "100"}),
			target("eta", true, "", map[string]string{"region": "eu", "kapro.io/available-capacity-percent": "10"}),
		},
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	want := []string{
		"alpha:PLANNING_DECISION_INCLUDE:Eligible",
		"gamma:PLANNING_DECISION_INCLUDE:Eligible",
		"epsilon:PLANNING_DECISION_DEFER:ActiveRelease",
		"beta:PLANNING_DECISION_DEFER:MaxParallelLimit",
		"eta:PLANNING_DECISION_DEFER:InsufficientCapacity",
		"delta:PLANNING_DECISION_SKIP:TargetNotReady",
		"zeta:PLANNING_DECISION_SKIP:RequiredLabelMismatch",
	}
	if got := summarize(resp.GetTargets()); !reflect.DeepEqual(got, want) {
		t.Fatalf("planned targets:\n got=%v\nwant=%v", got, want)
	}
}

func TestPlanIsDeterministicForInputOrder(t *testing.T) {
	reqA := &kpiv1alpha1.PlanRequest{
		Targets: []*kpiv1alpha1.Target{
			target("b", true, "", map[string]string{"region": "eu", "capacity": "50"}),
			target("a", true, "", map[string]string{"region": "eu", "capacity": "50"}),
		},
	}
	reqB := &kpiv1alpha1.PlanRequest{
		Targets: []*kpiv1alpha1.Target{
			target("a", true, "", map[string]string{"region": "eu", "capacity": "50"}),
			target("b", true, "", map[string]string{"region": "eu", "capacity": "50"}),
		},
	}
	server := &capacityPlannerServer{}
	respA, err := server.Plan(context.Background(), reqA)
	if err != nil {
		t.Fatalf("Plan A returned error: %v", err)
	}
	respB, err := server.Plan(context.Background(), reqB)
	if err != nil {
		t.Fatalf("Plan B returned error: %v", err)
	}
	if gotA, gotB := summarize(respA.GetTargets()), summarize(respB.GetTargets()); !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("plans differ:\nA=%v\nB=%v", gotA, gotB)
	}
}

func TestPlanInvalidMinCapacity(t *testing.T) {
	_, err := (&capacityPlannerServer{}).Plan(context.Background(), &kpiv1alpha1.PlanRequest{
		Parameters: map[string]string{"minAvailableCapacityPercent": "120"},
		Targets:    []*kpiv1alpha1.Target{target("a", true, "", nil)},
	})
	if err == nil {
		t.Fatal("expected invalid min capacity error")
	}
}

func target(name string, ready bool, activeRelease string, labels map[string]string) *kpiv1alpha1.Target {
	return &kpiv1alpha1.Target{
		Name:          name,
		Ready:         ready,
		ActiveRelease: activeRelease,
		Labels:        labels,
	}
}

func summarize(targets []*kpiv1alpha1.PlannedTarget) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.GetName()+":"+target.GetDecision().String()+":"+target.GetReason())
	}
	return out
}

func newTestClient(t *testing.T, server *capacityPlannerServer) kpiv1alpha1.PlannerServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	kpiv1alpha1.RegisterPlannerServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return kpiv1alpha1.NewPlannerServiceClient(conn)
}

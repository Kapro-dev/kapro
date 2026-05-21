package adapter

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"
)

func TestActuatorAdapterApplyMapsRequest(t *testing.T) {
	server := &recordingActuatorServer{}
	client, stop := actuatorClient(t, server)
	defer stop()

	adapter, err := NewActuatorAdapter(pluginReg(kaprov1alpha2.PluginTypeActuator, "argo/pull"), client)
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Apply(context.Background(), actuator.ApplyRequest{
		Cluster:         &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "de-prod"}},
		Version:         "1.2.3",
		PreviousVersion: "1.2.2",
		AppKey:          "api",
	})
	if err != nil {
		t.Fatal(err)
	}

	if server.apply.Target != "de-prod" || server.apply.Version != "1.2.3" || server.apply.PreviousVersion != "1.2.2" {
		t.Fatalf("ApplyRequest = %+v", server.apply)
	}
	if server.apply.Parameters[appKeyParam] != "api" || server.apply.Parameters["tenant"] != "payments" {
		t.Fatalf("Parameters = %v", server.apply.Parameters)
	}
}

func TestActuatorAdapterReturnsGRPCError(t *testing.T) {
	client, stop := actuatorClient(t, &recordingActuatorServer{applyErr: status.Error(codes.Unavailable, "down")})
	defer stop()

	adapter, err := NewActuatorAdapter(pluginReg(kaprov1alpha2.PluginTypeActuator, "argo/pull"), client)
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Apply(context.Background(), actuator.ApplyRequest{
		Cluster: &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "de-prod"}},
		Version: "1.2.3",
	})
	if err == nil || !strings.Contains(err.Error(), "Apply RPC to \"bufnet\" failed") || !strings.Contains(err.Error(), "down") {
		t.Fatalf("error = %v", err)
	}
}

func TestActuatorAdapterApplyObservesRuntimeMetrics(t *testing.T) {
	server := &recordingActuatorServer{}
	client, stop := actuatorClient(t, server)
	defer stop()

	adapter, err := NewActuatorAdapter(pluginReg(kaprov1alpha2.PluginTypeActuator, "metrics/apply"), client)
	if err != nil {
		t.Fatal(err)
	}
	counter := kaprometrics.PluginRuntimeCalls.WithLabelValues("actuator", "metrics/apply", "Apply", "success")
	before := testutil.ToFloat64(counter)
	err = adapter.Apply(context.Background(), actuator.ApplyRequest{
		Cluster: &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "de-prod"}},
		Version: "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(counter); got != before+1 {
		t.Fatalf("runtime call counter = %v, want %v", got, before+1)
	}
}

func TestActuatorAdapterValidationErrorsObserveRuntimeMetrics(t *testing.T) {
	adapter, err := NewActuatorAdapter(pluginReg(kaprov1alpha2.PluginTypeActuator, "metrics/validation"), fakeActuatorClient{})
	if err != nil {
		t.Fatal(err)
	}
	counter := kaprometrics.PluginRuntimeCalls.WithLabelValues("actuator", "metrics/validation", "Apply", "error")
	before := testutil.ToFloat64(counter)
	if err := adapter.Apply(context.Background(), actuator.ApplyRequest{Version: "1.2.3"}); err == nil {
		t.Fatal("Apply returned nil error")
	}
	if got := testutil.ToFloat64(counter); got != before+1 {
		t.Fatalf("runtime validation error counter = %v, want %v", got, before+1)
	}
}

func TestGateAdapterValidationErrorsObserveRuntimeMetrics(t *testing.T) {
	adapter, err := NewGateAdapter(pluginReg(kaprov1alpha2.PluginTypeGate, "metrics/gate-validation"), fakeGateClient{})
	if err != nil {
		t.Fatal(err)
	}
	counter := kaprometrics.PluginRuntimeCalls.WithLabelValues("gate", "metrics/gate-validation", "Evaluate", "error")
	before := testutil.ToFloat64(counter)
	if _, err := adapter.Evaluate(context.Background(), gate.Request{}); err == nil {
		t.Fatal("Evaluate returned nil error")
	}
	if got := testutil.ToFloat64(counter); got != before+1 {
		t.Fatalf("runtime validation error counter = %v, want %v", got, before+1)
	}
}

func TestGateAdapterMapsPhases(t *testing.T) {
	tests := []struct {
		name string
		in   kgiv1alpha1.GatePhase
		want kaprov1alpha2.GatePhase
	}{
		{name: "passed", in: kgiv1alpha1.GatePhase_GATE_PHASE_PASSED, want: kaprov1alpha2.GatePhasePassed},
		{name: "failed", in: kgiv1alpha1.GatePhase_GATE_PHASE_FAILED, want: kaprov1alpha2.GatePhaseFailed},
		{name: "running", in: kgiv1alpha1.GatePhase_GATE_PHASE_RUNNING, want: kaprov1alpha2.GatePhaseRunning},
		{name: "inconclusive", in: kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE, want: kaprov1alpha2.GatePhaseInconclusive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &recordingGateServer{phase: tt.in}
			client, stop := gateClient(t, server)
			defer stop()

			adapter, err := NewGateAdapter(pluginReg(kaprov1alpha2.PluginTypeGate, "slo"), client)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Evaluate(context.Background(), gate.Request{
				Context: &gate.Context{
					PromotionRunRef: "rel-1",
					Target:          "de-prod",
					Plan:   "prod",
					Stage:           "canary",
					Version:         "1.2.3",
				},
				Template: &kaprov1alpha2.GateTemplateSpec{Name: "error-budget"},
				Args:     map[string]string{"window": "5m"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != tt.want {
				t.Fatalf("Phase = %q, want %q", result.Phase, tt.want)
			}
			if server.evaluate.Gate != "error-budget" || server.evaluate.Parameters["window"] != "5m" {
				t.Fatalf("EvaluateRequest = %+v", server.evaluate)
			}
		})
	}
}

func TestPlannerAdapterMapsPlanDecisions(t *testing.T) {
	server := &recordingPlannerServer{
		targets: []*kpiv1alpha1.PlannedTarget{
			{Name: "cluster-b", Decision: kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE, Score: 90},
			{Name: "cluster-a", Decision: kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER, Reason: "CapacityFull", Message: "wait for capacity"},
		},
	}
	client, stop := plannerClient(t, server)
	defer stop()

	adapter, err := NewPlannerAdapter(pluginReg(kaprov1alpha2.PluginTypePlanner, "fleet-capacity"), client)
	if err != nil {
		t.Fatal(err)
	}
	framework := planner.NewFramework(adapter)
	result, err := framework.PlanWithResult(context.Background(), planner.Request{
		PromotionRun:         &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}, Spec: kaprov1alpha2.PromotionRunSpec{Version: "1.2.3"}},
		PromotionPlanRefName: "checkout",
		Stage: kaprov1alpha2.Stage{
			Name:     "prod",
			Strategy: &kaprov1alpha2.StageStrategySpec{MaxParallel: 5},
		},
	}, []kaprov1alpha2.Cluster{
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Labels: map[string]string{"region": "eu"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-b", Labels: map[string]string{"region": "us"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 1 || result.Targets[0].Name != "cluster-b" {
		t.Fatalf("targets = %#v, want only cluster-b", result.Targets)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].Target != "cluster-a" || result.Decisions[0].Reason != "CapacityFull" {
		t.Fatalf("decisions = %#v", result.Decisions)
	}
	if server.plan.PromotionRun != "rel-1" || server.plan.PromotionPlan != "checkout" || server.plan.Stage != "prod" || server.plan.Version != "1.2.3" {
		t.Fatalf("PlanRequest = %+v", server.plan)
	}
	if server.plan.Strategy.GetMaxParallel() != 5 || len(server.plan.Targets) != 2 || server.plan.Targets[0].Labels["region"] != "eu" {
		t.Fatalf("PlanRequest = %+v", server.plan)
	}
}

func TestRegisterReadyPluginsSkipsStaleAndRegistersReady(t *testing.T) {
	server := &recordingActuatorServer{}
	client, stop := actuatorClient(t, server)
	defer stop()
	_ = client

	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := pluginReg(kaprov1alpha2.PluginTypeActuator, "argo/pull")
	ready.Name = "ready"
	ready.Generation = 3
	ready.Spec.Endpoint = "bufnet"
	ready.Status.Ready = true
	ready.Status.ObservedGeneration = 3
	stale := pluginReg(kaprov1alpha2.PluginTypeActuator, "stale/pull")
	stale.Name = "stale"
	stale.Generation = 4
	stale.Spec.Endpoint = "bufnet"
	stale.Status.Ready = true
	stale.Status.ObservedGeneration = 3

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&ready, &stale).WithStatusSubresource(&kaprov1alpha2.Plugin{}).Build()
	actuatorReg := actuator.NewRegistry()
	gateReg := gate.NewRegistry()
	plannerFramework := planner.NewDefaultFramework()
	kaprometrics.PluginRuntimeRegistered.WithLabelValues("gate").Set(7)

	registered, err := (Registrar{DialOptions: bufDialOptions(server.listener)}).RegisterReady(context.Background(), k8sClient, actuatorReg, gateReg, plannerFramework)
	if err != nil {
		t.Fatal(err)
	}
	if registered != 1 {
		t.Fatalf("registered = %d, want 1", registered)
	}
	if _, err := actuatorReg.Resolve("argo/pull"); err != nil {
		t.Fatalf("ready plugin not registered: %v", err)
	}
	if _, err := actuatorReg.Resolve("stale/pull"); err == nil {
		t.Fatal("stale plugin was registered")
	}
	if got := testutil.ToFloat64(kaprometrics.PluginRuntimeRegistered.WithLabelValues("actuator")); got != 1 {
		t.Fatalf("registered actuator gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(kaprometrics.PluginRuntimeRegistered.WithLabelValues("gate")); got != 0 {
		t.Fatalf("registered gate gauge = %v, want 0", got)
	}
}

func TestEnabledFromEnv(t *testing.T) {
	t.Setenv(EnableEnv, "")
	if EnabledFromEnv() {
		t.Fatal("expected plugin gateway disabled")
	}
	t.Setenv(EnableEnv, "true")
	if !EnabledFromEnv() {
		t.Fatal("expected plugin gateway enabled")
	}
}

func pluginReg(pluginType kaprov1alpha2.PluginType, name string) kaprov1alpha2.Plugin {
	return kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: strings.ReplaceAll(name, "/", "-"), Generation: 1},
		Spec: kaprov1alpha2.PluginSpec{
			Type:       pluginType,
			Name:       name,
			Protocol:   kaprov1alpha2.PluginProtocolGRPC,
			Endpoint:   "bufnet",
			Timeout:    "1s",
			Parameters: map[string]string{"tenant": "payments"},
		},
		Status: kaprov1alpha2.PluginStatus{Ready: true, ObservedGeneration: 1},
	}
}

func actuatorClient(t *testing.T, srv kaiv1alpha1.ActuatorServiceServer) (kaiv1alpha1.ActuatorServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	if recorder, ok := srv.(*recordingActuatorServer); ok {
		recorder.listener = listener
	}
	server := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.DialContext(context.Background(), "bufnet", append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock()}, bufDialOptions(listener)...)...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		t.Fatal(err)
	}
	return kaiv1alpha1.NewActuatorServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
	}
}

func gateClient(t *testing.T, srv kgiv1alpha1.GateServiceServer) (kgiv1alpha1.GateServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kgiv1alpha1.RegisterGateServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.DialContext(context.Background(), "bufnet", append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock()}, bufDialOptions(listener)...)...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		t.Fatal(err)
	}
	return kgiv1alpha1.NewGateServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
	}
}

func plannerClient(t *testing.T, srv kpiv1alpha1.PlannerServiceServer) (kpiv1alpha1.PlannerServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kpiv1alpha1.RegisterPlannerServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.DialContext(context.Background(), "bufnet", append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock()}, bufDialOptions(listener)...)...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		t.Fatal(err)
	}
	return kpiv1alpha1.NewPlannerServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
	}
}

func bufDialOptions(listener *bufconn.Listener) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	}
}

type recordingActuatorServer struct {
	kaiv1alpha1.UnimplementedActuatorServiceServer
	listener *bufconn.Listener
	apply    *kaiv1alpha1.ApplyRequest
	applyErr error
}

type fakeActuatorClient struct{}

func (fakeActuatorClient) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return nil, nil
}

func (fakeActuatorClient) Apply(context.Context, *kaiv1alpha1.ApplyRequest, ...grpc.CallOption) (*kaiv1alpha1.ApplyResponse, error) {
	return nil, nil
}

func (fakeActuatorClient) IsConverged(context.Context, *kaiv1alpha1.IsConvergedRequest, ...grpc.CallOption) (*kaiv1alpha1.IsConvergedResponse, error) {
	return nil, nil
}

func (fakeActuatorClient) Rollback(context.Context, *kaiv1alpha1.RollbackRequest, ...grpc.CallOption) (*kaiv1alpha1.RollbackResponse, error) {
	return nil, nil
}

type fakeGateClient struct{}

func (fakeGateClient) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return nil, nil
}

func (fakeGateClient) Evaluate(context.Context, *kgiv1alpha1.EvaluateRequest, ...grpc.CallOption) (*kgiv1alpha1.EvaluateResponse, error) {
	return nil, nil
}

func (s *recordingActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{ContractVersion: "v1alpha1", PluginVersion: "test"}, nil
}

func (s *recordingActuatorServer) Apply(_ context.Context, req *kaiv1alpha1.ApplyRequest) (*kaiv1alpha1.ApplyResponse, error) {
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	s.apply = req
	return &kaiv1alpha1.ApplyResponse{Accepted: true}, nil
}

func (s *recordingActuatorServer) IsConverged(context.Context, *kaiv1alpha1.IsConvergedRequest) (*kaiv1alpha1.IsConvergedResponse, error) {
	return &kaiv1alpha1.IsConvergedResponse{Converged: true}, nil
}

func (s *recordingActuatorServer) Rollback(context.Context, *kaiv1alpha1.RollbackRequest) (*kaiv1alpha1.RollbackResponse, error) {
	return &kaiv1alpha1.RollbackResponse{Accepted: true}, nil
}

type recordingGateServer struct {
	kgiv1alpha1.UnimplementedGateServiceServer
	phase    kgiv1alpha1.GatePhase
	evaluate *kgiv1alpha1.EvaluateRequest
}

func (s *recordingGateServer) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return &kgiv1alpha1.GetCapabilitiesResponse{ContractVersion: "v1alpha1", PluginVersion: "test"}, nil
}

func (s *recordingGateServer) Evaluate(_ context.Context, req *kgiv1alpha1.EvaluateRequest) (*kgiv1alpha1.EvaluateResponse, error) {
	s.evaluate = req
	return &kgiv1alpha1.EvaluateResponse{Phase: s.phase, Message: "ok"}, nil
}

type recordingPlannerServer struct {
	kpiv1alpha1.UnimplementedPlannerServiceServer
	targets []*kpiv1alpha1.PlannedTarget
	plan    *kpiv1alpha1.PlanRequest
}

func (s *recordingPlannerServer) GetCapabilities(context.Context, *kpiv1alpha1.GetCapabilitiesRequest) (*kpiv1alpha1.GetCapabilitiesResponse, error) {
	return &kpiv1alpha1.GetCapabilitiesResponse{ContractVersion: "v1alpha1", PluginVersion: "test", Capabilities: []string{"filter", "score"}}, nil
}

func (s *recordingPlannerServer) Plan(_ context.Context, req *kpiv1alpha1.PlanRequest) (*kpiv1alpha1.PlanResponse, error) {
	s.plan = req
	return &kpiv1alpha1.PlanResponse{Targets: s.targets}, nil
}

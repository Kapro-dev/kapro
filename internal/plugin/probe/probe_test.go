package probe

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaprometrics "kapro.io/kapro/internal/metrics"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"
)

func TestProbeActuatorCapabilities(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(server, fakeActuatorServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeActuator,
			Name:     "argo/pull",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
			Timeout:  "1s",
		},
	})

	if !result.Ready {
		t.Fatalf("Ready = false, reason=%s message=%s", result.Reason, result.Message)
	}
	if result.Version != "actuator-test" {
		t.Fatalf("Version = %q", result.Version)
	}
	if result.ContractVersion != ContractVersion() {
		t.Fatalf("ContractVersion = %q", result.ContractVersion)
	}
	if len(result.Capabilities) != 2 {
		t.Fatalf("Capabilities = %v", result.Capabilities)
	}
}

func TestProbeGateCapabilities(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kgiv1alpha1.RegisterGateServiceServer(server, fakeGateServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeGate,
			Name:     "slo",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
			Timeout:  "1s",
		},
	})

	if !result.Ready {
		t.Fatalf("Ready = false, reason=%s message=%s", result.Reason, result.Message)
	}
	if result.Version != "gate-test" {
		t.Fatalf("Version = %q", result.Version)
	}
	if len(result.Capabilities) != 1 {
		t.Fatalf("Capabilities = %v", result.Capabilities)
	}
}

func TestProbePlannerCapabilities(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kpiv1alpha1.RegisterPlannerServiceServer(server, fakePlannerServer{capabilities: []string{"filter", "score"}})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypePlanner,
			Name:     "capacity",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
			Timeout:  "1s",
		},
	})

	if !result.Ready {
		t.Fatalf("Ready = false, reason=%s message=%s", result.Reason, result.Message)
	}
	if result.Version != "planner-test" {
		t.Fatalf("Version = %q", result.Version)
	}
	if len(result.Capabilities) != 2 {
		t.Fatalf("Capabilities = %v", result.Capabilities)
	}
}

func TestProbePlannerMissingCapability(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kpiv1alpha1.RegisterPlannerServiceServer(server, fakePlannerServer{capabilities: []string{"reserve"}})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypePlanner,
			Name:     "capacity",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
			Endpoint: "bufnet",
			Timeout:  "1s",
		},
	})

	if result.Ready {
		t.Fatal("Ready = true")
	}
	if result.Reason != "MissingCapability" {
		t.Fatalf("Reason = %q", result.Reason)
	}
	if !strings.Contains(result.Message, "filter, score, order, or defer") {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestProbeValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		spec kaprov1alpha2.PluginSpec
		want string
	}{
		{
			name: "unsupported type",
			spec: kaprov1alpha2.PluginSpec{Type: "other", Protocol: kaprov1alpha2.PluginProtocolGRPC, Endpoint: "dns:///plugin:9090"},
			want: "UnsupportedType",
		},
		{
			name: "unsupported protocol",
			spec: kaprov1alpha2.PluginSpec{Type: kaprov1alpha2.PluginTypeGate, Protocol: "http", Endpoint: "dns:///plugin:9090"},
			want: "UnsupportedProtocol",
		},
		{
			name: "missing endpoint",
			spec: kaprov1alpha2.PluginSpec{Type: kaprov1alpha2.PluginTypeGate, Protocol: kaprov1alpha2.PluginProtocolGRPC},
			want: "InvalidEndpoint",
		},
		{
			name: "invalid timeout",
			spec: kaprov1alpha2.PluginSpec{Type: kaprov1alpha2.PluginTypeGate, Protocol: kaprov1alpha2.PluginProtocolGRPC, Endpoint: "dns:///plugin:9090", Timeout: "soon"},
			want: "InvalidTimeout",
		},
		{
			name: "tls requires secret client",
			spec: kaprov1alpha2.PluginSpec{
				Type:         kaprov1alpha2.PluginTypeGate,
				Protocol:     kaprov1alpha2.PluginProtocolGRPC,
				Endpoint:     "dns:///plugin:9090",
				TLSSecretRef: &corev1.SecretReference{Name: "plugin-tls", Namespace: "kapro-system"},
			},
			want: "TLSInvalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Prober{}.Probe(context.Background(), kaprov1alpha2.Plugin{Spec: tt.spec})
			if result.Ready {
				t.Fatal("Ready = true")
			}
			if result.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.want)
			}
		})
	}
}

func TestProbeContractVersionPolicy(t *testing.T) {
	tests := []struct {
		name            string
		contractVersion string
		wantReady       bool
		wantReason      string
		wantMessage     string
	}{
		{
			name:            "supported",
			contractVersion: ContractVersion(),
			wantReady:       true,
			wantReason:      "ProbeSucceeded",
		},
		{
			name:            "unsupported",
			contractVersion: "v2",
			wantReason:      "UnsupportedContractVersion",
			wantMessage:     "unsupported KAI contract version \"v2\"",
		},
		{
			name:        "missing",
			wantReason:  "MissingContractVersion",
			wantMessage: "did not report KAI contract version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener := bufconn.Listen(1024 * 1024)
			server := grpc.NewServer()
			kaiv1alpha1.RegisterActuatorServiceServer(server, fakeActuatorServer{
				contractVersion:     tt.contractVersion,
				omitContractVersion: tt.contractVersion == "",
			})
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()

			result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha2.Plugin{
				Spec: kaprov1alpha2.PluginSpec{
					Type:     kaprov1alpha2.PluginTypeActuator,
					Name:     "argo/pull",
					Protocol: kaprov1alpha2.PluginProtocolGRPC,
					Endpoint: "bufnet",
					Timeout:  "1s",
				},
			})

			if result.Ready != tt.wantReady {
				t.Fatalf("Ready = %v, want %v; reason=%s message=%s", result.Ready, tt.wantReady, result.Reason, result.Message)
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.wantReason)
			}
			if result.ContractVersion != tt.contractVersion {
				t.Fatalf("ContractVersion = %q, want %q", result.ContractVersion, tt.contractVersion)
			}
			if tt.wantMessage != "" && !strings.Contains(result.Message, tt.wantMessage) {
				t.Fatalf("Message = %q, want to contain %q", result.Message, tt.wantMessage)
			}
		})
	}
}

func TestProbeObservesReadinessMetrics(t *testing.T) {
	reg := kaprov1alpha2.Plugin{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-probe-registration"},
		Spec: kaprov1alpha2.PluginSpec{
			Type:     kaprov1alpha2.PluginTypeGate,
			Name:     "metrics/probe",
			Protocol: kaprov1alpha2.PluginProtocolGRPC,
		},
	}
	counter := kaprometrics.PluginProbeResults.WithLabelValues("gate", "error", "InvalidEndpoint")
	before := testutil.ToFloat64(counter)
	readiness := kaprometrics.PluginProbeReady.WithLabelValues("gate", "metrics/probe")
	readiness.Set(1)

	result := Prober{}.Probe(context.Background(), reg)
	if result.Ready {
		t.Fatal("Ready = true")
	}
	if !strings.Contains(result.Message, "metrics-probe-registration") || !strings.Contains(result.Message, "endpoint is required") {
		t.Fatalf("Message = %q", result.Message)
	}
	if got := testutil.ToFloat64(counter); got != before+1 {
		t.Fatalf("probe result counter = %v, want %v", got, before+1)
	}
	if got := testutil.ToFloat64(readiness); got != 0 {
		t.Fatalf("probe readiness gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(kaprometrics.PluginProbeReady.WithLabelValues("gate", "metrics-probe-registration")); got != 0 {
		t.Fatalf("object-name readiness gauge = %v, want deleted/zero", got)
	}
}

func bufDialOptions(listener *bufconn.Listener) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	}
}

type fakeActuatorServer struct {
	kaiv1alpha1.UnimplementedActuatorServiceServer
	contractVersion     string
	omitContractVersion bool
}

func (s fakeActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	version := ContractVersion()
	if s.contractVersion != "" {
		version = s.contractVersion
	}
	if s.omitContractVersion {
		version = ""
	}
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: version,
		PluginVersion:   "actuator-test",
		Capabilities:    []string{"apply", "rollback"},
	}, nil
}

type fakeGateServer struct {
	kgiv1alpha1.UnimplementedGateServiceServer
}

func (fakeGateServer) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return &kgiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: ContractVersion(),
		PluginVersion:   "gate-test",
		Capabilities:    []string{"evaluate"},
	}, nil
}

type fakePlannerServer struct {
	kpiv1alpha1.UnimplementedPlannerServiceServer
	capabilities []string
}

func (s fakePlannerServer) GetCapabilities(context.Context, *kpiv1alpha1.GetCapabilitiesRequest) (*kpiv1alpha1.GetCapabilitiesResponse, error) {
	capabilities := s.capabilities
	if capabilities == nil {
		capabilities = []string{"filter", "score"}
	}
	return &kpiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: ContractVersion(),
		PluginVersion:   "planner-test",
		Capabilities:    capabilities,
	}, nil
}

package probe

import (
	"context"
	"net"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
)

func TestProbeActuatorCapabilities(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(server, fakeActuatorServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha1.PluginRegistration{
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "argo/pull",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
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

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha1.PluginRegistration{
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeGate,
			Name:     "slo",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
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

func TestProbeValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		spec kaprov1alpha1.PluginRegistrationSpec
		want string
	}{
		{
			name: "unsupported type",
			spec: kaprov1alpha1.PluginRegistrationSpec{Type: "other", Protocol: kaprov1alpha1.PluginProtocolGRPC, Endpoint: "dns:///plugin:9090"},
			want: "UnsupportedType",
		},
		{
			name: "unsupported protocol",
			spec: kaprov1alpha1.PluginRegistrationSpec{Type: kaprov1alpha1.PluginTypeGate, Protocol: "http", Endpoint: "dns:///plugin:9090"},
			want: "UnsupportedProtocol",
		},
		{
			name: "missing endpoint",
			spec: kaprov1alpha1.PluginRegistrationSpec{Type: kaprov1alpha1.PluginTypeGate, Protocol: kaprov1alpha1.PluginProtocolGRPC},
			want: "InvalidEndpoint",
		},
		{
			name: "invalid timeout",
			spec: kaprov1alpha1.PluginRegistrationSpec{Type: kaprov1alpha1.PluginTypeGate, Protocol: kaprov1alpha1.PluginProtocolGRPC, Endpoint: "dns:///plugin:9090", Timeout: "soon"},
			want: "InvalidTimeout",
		},
		{
			name: "tls requires secret client",
			spec: kaprov1alpha1.PluginRegistrationSpec{
				Type:         kaprov1alpha1.PluginTypeGate,
				Protocol:     kaprov1alpha1.PluginProtocolGRPC,
				Endpoint:     "dns:///plugin:9090",
				TLSSecretRef: &corev1.SecretReference{Name: "plugin-tls", Namespace: "kapro-system"},
			},
			want: "TLSInvalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Prober{}.Probe(context.Background(), kaprov1alpha1.PluginRegistration{Spec: tt.spec})
			if result.Ready {
				t.Fatal("Ready = true")
			}
			if result.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.want)
			}
		})
	}
}

func TestProbeRejectsContractMismatch(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(server, fakeActuatorServer{contractVersion: "v2"})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	result := Prober{DialOptions: bufDialOptions(listener)}.Probe(context.Background(), kaprov1alpha1.PluginRegistration{
		Spec: kaprov1alpha1.PluginRegistrationSpec{
			Type:     kaprov1alpha1.PluginTypeActuator,
			Name:     "argo/pull",
			Protocol: kaprov1alpha1.PluginProtocolGRPC,
			Endpoint: "bufnet",
			Timeout:  "1s",
		},
	})

	if result.Ready {
		t.Fatal("Ready = true")
	}
	if result.Reason != "ContractMismatch" {
		t.Fatalf("Reason = %q", result.Reason)
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
	contractVersion string
}

func (s fakeActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	version := ContractVersion()
	if s.contractVersion != "" {
		version = s.contractVersion
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

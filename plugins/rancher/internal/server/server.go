// Package server implements the gRPC server side of the Rancher plugin.
// It listens for ProviderService calls from the Kapro core operator,
// resolves Rancher bearer tokens from K8s Secrets, and delegates to
// the Rancher Connector.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	rancherprovider "kapro.io/plugins/rancher/internal/provider"
)

// connectRequest mirrors ProviderService.Connect proto request (JSON wire).
type connectRequest struct {
	EnvironmentName string `json:"environment_name"`
	Token           string `json:"token"` // resolved by caller from K8s Secret
	// Full environment spec inlined for self-contained decoding.
	ServerURL string `json:"server_url"`
	ClusterID string `json:"cluster_id"`
}

// connectResponse mirrors ProviderService.ConnectResponse (JSON wire).
// Returns the kubeconfig in base64-encoded form so it round-trips cleanly over JSON.
type connectResponse struct {
	// Kubeconfig is the raw kubeconfig YAML returned by Rancher.
	Kubeconfig string `json:"kubeconfig"`
}

// reachableRequest mirrors ProviderService.IsReachable proto request.
type reachableRequest struct {
	EnvironmentName string `json:"environment_name"`
	Token           string `json:"token"`
	ServerURL       string `json:"server_url"`
	ClusterID       string `json:"cluster_id"`
}

// reachableResponse mirrors ProviderService.IsReachableResponse.
type reachableResponse struct {
	Reachable bool   `json:"reachable"`
	Reason    string `json:"reason,omitempty"`
}

const (
	providerServiceConnect     = "/kapro.v1alpha1.ProviderService/Connect"
	providerServiceIsReachable = "/kapro.v1alpha1.ProviderService/IsReachable"
	pluginServiceName          = "kapro.plugin"
)

// Server is the gRPC service handler for the Rancher plugin.
type Server struct {
	connector *rancherprovider.Connector
}

// New creates a Server backed by the given Connector.
func New(c *rancherprovider.Connector) *Server {
	return &Server{connector: c}
}

// Start starts the gRPC server on the given listener and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context, lis net.Listener) error {
	grpcSrv := grpc.NewServer()

	// Health service — Kapro operator probes this.
	healthSvc := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSvc)
	healthSvc.SetServingStatus(pluginServiceName, healthpb.HealthCheckResponse_SERVING)

	// Reflection — useful for grpcurl debugging.
	reflection.Register(grpcSrv)

	// Manual routing: no codegen, JSON wire format.
	grpcSrv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "kapro.v1alpha1.ProviderService",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Connect", Handler: s.handleConnect},
			{MethodName: "IsReachable", Handler: s.handleIsReachable},
		},
		Streams: nil,
	}, s)

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcSrv.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		grpcSrv.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleConnect(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
	var reqBytes []byte
	if err := dec(&reqBytes); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode connect request: %v", err)
	}
	var req connectRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal connect request: %v", err)
	}
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	env := buildEnv(req.EnvironmentName, req.ServerURL, req.ClusterID)
	ctx = rancherprovider.WithToken(ctx, req.Token)

	cfg, err := s.connector.Connect(ctx, env)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rancher connect: %v", err)
	}

	// Synthesise a minimal kubeconfig from the rest.Config.
	// The full YAML comes from generateKubeconfig internally; cfg.Host is enough
	// for the operator to verify reachability. We return the raw host here.
	resp := connectResponse{Kubeconfig: cfg.Host}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal connect response: %v", err)
	}
	return out, nil
}

func (s *Server) handleIsReachable(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
	var reqBytes []byte
	if err := dec(&reqBytes); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode reachable request: %v", err)
	}
	var req reachableRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal reachable request: %v", err)
	}

	env := buildEnv(req.EnvironmentName, req.ServerURL, req.ClusterID)
	ctx = rancherprovider.WithToken(ctx, req.Token)

	reachable, err := s.connector.IsReachable(ctx, env)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rancher isReachable: %v", err)
	}

	reason := ""
	if !reachable {
		reason = "cluster is not in active state or Rancher is unreachable"
	}
	out, err := json.Marshal(reachableResponse{Reachable: reachable, Reason: reason})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal reachable response: %v", err)
	}
	return out, nil
}

// buildEnv constructs a minimal Environment from the wire request params.
func buildEnv(name, serverURL, clusterID string) *kaprov1alpha1.Environment {
	env := &kaprov1alpha1.Environment{}
	env.Name = name
	env.Spec.Provider = &kaprov1alpha1.ProviderSpec{
		Rancher: &kaprov1alpha1.RancherProviderSpec{
			ServerURL: serverURL,
			ClusterID: clusterID,
		},
	}
	return env
}

// socketPath returns the Unix socket path for this plugin (env-overridable).
func SocketPath() string {
	if p := os.Getenv("KAPRO_PLUGIN_SOCKET"); p != "" {
		return p
	}
	return "/tmp/kapro-rancher.sock"
}

// ListenUnix creates a Unix domain socket listener at SocketPath().
func ListenUnix() (net.Listener, error) {
	sock := SocketPath()
	_ = os.Remove(sock) // clean up stale socket
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", sock, err)
	}
	return lis, nil
}

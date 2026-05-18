// Package provider abstracts how Kapro connects to spoke clusters.
//
// Provider kinds:
//   - kubeconfig: static kubeconfig file (any cloud, kind, on-prem)
//   - gcp-basic:  GKE Workload Identity + direct GKE API endpoint
//                 (single-project / VPC-peered private GKE)
//   - gcp-fleet:  GKE Fleet API for discovery + Connect Gateway for access.
//                 Topology-agnostic — works across any project, VPC, or
//                 region without peering. Connect Gateway is an
//                 implementation detail of this provider, not a selectable
//                 kind on its own (see gcp_connect_gateway.go helpers).
//
// Future providers (stubs in v0.5): eks, aks-arc, rhacm, capi.
//
// The provider interface is used by the actuator to reach spoke clusters
// and by the CLI to register clusters.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"
)

// ClusterInfo represents a discovered or registered cluster.
type ClusterInfo struct {
	Name     string
	Labels   map[string]string
	Project  string
	Location string
	// Endpoint is the API server URL (for gcp-basic) or Connect Gateway URL (for gcp-fleet).
	Endpoint string
	// Provider is the mode that discovered this cluster.
	Provider string
}

// Provider abstracts spoke cluster access.
type Provider interface {
	// Name returns the provider identifier (kubeconfig, gcp-basic, gcp-fleet).
	Name() string

	// GenerateKubeConfig creates a kubeconfig YAML for the given cluster.
	// For kubeconfig mode: reads from the file path.
	// For gcp-basic: generates kubeconfig with GKE API endpoint + exec auth.
	// For gcp-fleet: generates kubeconfig with Connect Gateway URL + exec auth.
	GenerateKubeConfig(ctx context.Context, clusterName string) ([]byte, error)

	// ListClusters discovers clusters from the backend.
	// Only gcp-fleet implements real discovery.
	// kubeconfig and gcp-basic return an error (manual registration only).
	ListClusters(ctx context.Context) ([]ClusterInfo, error)
}

// Detect auto-detects the best available provider.
func Detect() string {
	// Check for GKE Fleet API access.
	if hasFleetAccess() {
		return "gcp-fleet"
	}
	// Check for Workload Identity (running on GKE).
	if hasWorkloadIdentity() {
		return "gcp-basic"
	}
	return "kubeconfig"
}

func hasFleetAccess() bool {
	// Check if GCP default credentials are available and can list Fleet memberships.
	_, err := google.FindDefaultCredentials(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	return err == nil
}

func hasWorkloadIdentity() bool {
	// Check GKE metadata server for Workload Identity.
	client := &http.Client{Timeout: 1 * time.Second}
	req, _ := http.NewRequest("GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/email", nil)
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == 200
}

// New creates a provider by name.
func New(name string, opts Options) (Provider, error) {
	switch name {
	case "kubeconfig":
		return &KubeconfigProvider{KubeconfigPath: opts.KubeconfigPath}, nil
	case "gcp-basic", "gcp":
		return &GCPBasicProvider{Project: opts.Project, Location: opts.Location}, nil
	case "gcp-fleet":
		return &GCPFleetProvider{Project: opts.Project}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (supported: kubeconfig, gcp-basic, gcp-fleet)", name)
	}
}

// Options for provider creation.
type Options struct {
	KubeconfigPath string
	Project        string
	Location       string
	ClusterName    string
}

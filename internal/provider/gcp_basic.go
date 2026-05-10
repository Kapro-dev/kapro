package provider

import (
	"context"
	"fmt"
	"strings"
)

// GCPBasicProvider uses GKE API endpoint + Workload Identity.
// No Fleet API or Connect Gateway needed — just direct GKE cluster access.
// Auth is via gke-gcloud-auth-plugin (Workload Identity, auto-refreshing).
type GCPBasicProvider struct {
	Project  string
	Location string
}

var _ Provider = (*GCPBasicProvider)(nil)

func (p *GCPBasicProvider) Name() string { return "gcp-basic" }

func (p *GCPBasicProvider) GenerateKubeConfig(_ context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-basic provider")
	}

	location := p.Location
	if location == "" {
		// Try to detect from cluster name or default.
		location = "europe-west3"
	}

	// Get the GKE cluster endpoint.
	endpoint, err := execOutput("gcloud", "container", "clusters", "describe", clusterName,
		"--project", p.Project,
		"--region", location,
		"--format=value(endpoint)",
	)
	if err != nil {
		return nil, fmt.Errorf("get GKE endpoint for %s: %w", clusterName, err)
	}
	endpoint = strings.TrimSpace(endpoint)

	// Get the CA certificate.
	ca, err := execOutput("gcloud", "container", "clusters", "describe", clusterName,
		"--project", p.Project,
		"--region", location,
		"--format=value(masterAuth.clusterCaCertificate)",
	)
	if err != nil {
		return nil, fmt.Errorf("get GKE CA for %s: %w", clusterName, err)
	}
	ca = strings.TrimSpace(ca)

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
  - cluster:
      server: "https://%s"
      certificate-authority-data: %s
    name: %s
contexts:
  - context:
      cluster: %s
      user: %s
    name: %s
current-context: %s
users:
  - name: %s
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1beta1
        command: gke-gcloud-auth-plugin
        installHint: Install gke-gcloud-auth-plugin for use with kubectl by following https://cloud.google.com/kubernetes-engine/docs/how-to/cluster-access-for-kubectl
        provideClusterInfo: true
`, endpoint, ca, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)

	return []byte(kubeconfig), nil
}

func (p *GCPBasicProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	return nil, fmt.Errorf("gcp-basic provider does not support cluster discovery — use gcp-fleet or kapro cluster add")
}

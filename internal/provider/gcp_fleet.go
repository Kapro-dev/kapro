package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GCPFleetProvider uses GKE Fleet API for auto-discovery + Connect Gateway for access.
// Zero secrets — auth via Workload Identity through Connect Gateway proxy.
// This is the recommended mode for GKE at scale.
type GCPFleetProvider struct {
	Project string
}

var _ Provider = (*GCPFleetProvider)(nil)

func (p *GCPFleetProvider) Name() string { return "gcp-fleet" }

func (p *GCPFleetProvider) GenerateKubeConfig(_ context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-fleet provider")
	}

	// Connect Gateway URL — no cluster endpoint needed, Google proxies it.
	gatewayURL := fmt.Sprintf(
		"https://connectgateway.googleapis.com/v1/projects/%s/locations/global/memberships/%s",
		p.Project, clusterName,
	)

	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
  - cluster:
      server: "%s"
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
        installHint: Install gke-gcloud-auth-plugin
        provideClusterInfo: true
`, gatewayURL, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName)

	return []byte(kubeconfig), nil
}

// ListClusters discovers all Fleet memberships in the project.
func (p *GCPFleetProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required")
	}

	// List Fleet memberships as JSON.
	out, err := execOutput("gcloud", "container", "fleet", "memberships", "list",
		"--project", p.Project,
		"--format=json(name,labels,endpoint.gkeCluster.resourceLink)",
	)
	if err != nil {
		return nil, fmt.Errorf("list Fleet memberships: %w", err)
	}

	var memberships []struct {
		Name     string            `json:"name"`
		Labels   map[string]string `json:"labels"`
		Endpoint struct {
			GKECluster struct {
				ResourceLink string `json:"resourceLink"`
			} `json:"gkeCluster"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(out), &memberships); err != nil {
		return nil, fmt.Errorf("parse Fleet memberships: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(memberships))
	for _, m := range memberships {
		// Extract short name from full resource path.
		// projects/PROJECT/locations/global/memberships/NAME → NAME
		name := m.Name
		if parts := strings.Split(m.Name, "/"); len(parts) > 0 {
			name = parts[len(parts)-1]
		}

		// Extract location from resource link.
		// //container.googleapis.com/projects/P/locations/L/clusters/C
		location := ""
		if rl := m.Endpoint.GKECluster.ResourceLink; rl != "" {
			parts := strings.Split(rl, "/")
			for i, p := range parts {
				if p == "locations" && i+1 < len(parts) {
					location = parts[i+1]
					break
				}
			}
		}

		clusters = append(clusters, ClusterInfo{
			Name:     name,
			Labels:   m.Labels,
			Project:  p.Project,
			Location: location,
			Endpoint: fmt.Sprintf("https://connectgateway.googleapis.com/v1/projects/%s/locations/global/memberships/%s",
				p.Project, name),
			Provider: "gcp-fleet",
		})
	}

	return clusters, nil
}

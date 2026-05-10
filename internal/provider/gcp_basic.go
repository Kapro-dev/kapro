package provider

import (
	"context"
	"fmt"
	"strings"
)

// GCPBasicProvider uses GKE DNS endpoint + Workload Identity.
// No Fleet API or Connect Gateway needed — just direct GKE cluster access.
// Generates kubeconfig with a GCP access token (for Flux helm-controller).
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
		location = "europe-west3"
	}

	// Get the GKE DNS endpoint — no CA cert needed, uses public PKI.
	endpoint, err := execOutput("gcloud", "container", "clusters", "describe", clusterName,
		"--project", p.Project,
		"--location", location,
		"--format=value(controlPlaneEndpointsConfig.dnsEndpointConfig.endpoint)",
	)
	if err != nil || strings.TrimSpace(endpoint) == "" {
		// Fallback to private endpoint + CA if DNS endpoint not available.
		return p.generateWithPrivateEndpoint(clusterName, location)
	}
	endpoint = "https://" + strings.TrimSpace(endpoint)

	token, err := getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	return []byte(gcpKubeconfig(clusterName, endpoint, "", token)), nil
}

// generateWithPrivateEndpoint falls back to private IP + CA cert.
func (p *GCPBasicProvider) generateWithPrivateEndpoint(clusterName, location string) ([]byte, error) {
	endpoint, err := execOutput("gcloud", "container", "clusters", "describe", clusterName,
		"--project", p.Project,
		"--location", location,
		"--format=value(endpoint)",
	)
	if err != nil {
		return nil, fmt.Errorf("get GKE endpoint for %s: %w", clusterName, err)
	}
	endpoint = "https://" + strings.TrimSpace(endpoint)

	ca, err := execOutput("gcloud", "container", "clusters", "describe", clusterName,
		"--project", p.Project,
		"--location", location,
		"--format=value(masterAuth.clusterCaCertificate)",
	)
	if err != nil {
		return nil, fmt.Errorf("get GKE CA for %s: %w", clusterName, err)
	}

	token, err := getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	return []byte(gcpKubeconfig(clusterName, endpoint, strings.TrimSpace(ca), token)), nil
}

func (p *GCPBasicProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	return nil, fmt.Errorf("gcp-basic provider does not support cluster discovery — use gcp-fleet or kapro cluster add")
}

// getAccessToken gets a GCP access token using the default credentials.
// On GKE with WI, this returns the GSA's token. Locally, it uses ADC.
func getAccessToken() (string, error) {
	token, err := execOutput("gcloud", "auth", "print-access-token")
	if err != nil {
		return "", fmt.Errorf("gcloud auth print-access-token: %w", err)
	}
	return strings.TrimSpace(token), nil
}

// gcpKubeconfig generates a kubeconfig YAML with a bearer token.
// If caData is empty, no certificate-authority-data is set (DNS endpoint uses public PKI).
func gcpKubeconfig(clusterName, server, caData, token string) string {
	caLine := ""
	if caData != "" {
		caLine = fmt.Sprintf("\n      certificate-authority-data: %s", caData)
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
  - cluster:
      server: %s%s
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
      token: %s
`, server, caLine, clusterName, clusterName, clusterName, clusterName, clusterName, clusterName, token)
}

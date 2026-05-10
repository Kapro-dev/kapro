package provider

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// GCPBasicProvider uses GKE DNS endpoint + Workload Identity.
// No gcloud CLI dependency — uses Go GCP SDK for cluster info and oauth2 for auth.
// Token is cached and auto-refreshed (~5min before expiry).
type GCPBasicProvider struct {
	Project  string
	Location string
}

var _ Provider = (*GCPBasicProvider)(nil)

func (p *GCPBasicProvider) Name() string { return "gcp-basic" }

func (p *GCPBasicProvider) GenerateKubeConfig(ctx context.Context, clusterName string) ([]byte, error) {
	if p.Project == "" {
		return nil, fmt.Errorf("GCP project is required for gcp-basic provider")
	}

	location := p.Location
	if location == "" {
		location = "europe-west3"
	}

	// Get cluster info via Go GKE API.
	endpoint, ca, err := getClusterEndpoint(ctx, p.Project, location, clusterName)
	if err != nil {
		return nil, err
	}

	// Get access token (cached, auto-refreshed via WI or ADC).
	token, err := getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	return []byte(gcpKubeconfig(clusterName, endpoint, ca, token)), nil
}

func (p *GCPBasicProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	return nil, fmt.Errorf("gcp-basic provider does not support cluster discovery — use gcp-fleet")
}

// --- GCP API helpers (zero gcloud subprocess calls) ---

var (
	tokenSourceOnce sync.Once
	cachedTS        oauth2.TokenSource
	tokenSourceErr  error
)

// initTokenSource initializes the shared GCP token source (once).
// Tries ADC/WI first. If the token can't be fetched (expired ADC, no WI),
// falls back to gcloud CLI.
func initTokenSource(ctx context.Context) {
	tokenSourceOnce.Do(func() {
		ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			cachedTS = &gcloudFallbackTokenSource{}
			return
		}
		// Verify the token actually works — ADC can exist but be expired/revoked.
		if _, err := ts.Token(); err != nil {
			cachedTS = &gcloudFallbackTokenSource{}
			return
		}
		cachedTS = oauth2.ReuseTokenSource(nil, ts)
	})
}

// getAccessToken returns a GCP access token.
// Priority: ADC/WI (Go SDK, cached) → gcloud CLI fallback (for local dev with expired ADC).
// On GKE with WI: gets token from metadata server via Go SDK. Zero gcloud dependency.
// Locally: tries ADC first, falls back to gcloud auth print-access-token.
// Token is cached and auto-refreshed — one token serves ALL clusters in the same project.
func getAccessToken(ctx context.Context) (string, error) {
	initTokenSource(ctx)

	tok, err := cachedTS.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// gcloudFallbackTokenSource uses gcloud CLI when ADC is not available (local dev).
type gcloudFallbackTokenSource struct{}

func (g *gcloudFallbackTokenSource) Token() (*oauth2.Token, error) {
	out, err := exec.Command("gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return nil, fmt.Errorf("gcloud auth print-access-token: %w", err)
	}
	return &oauth2.Token{AccessToken: strings.TrimSpace(string(out))}, nil
}

// GCPTokenSource returns the shared token source for GCP API calls.
// Used by gcp_fleet.go to pass to the Fleet API client.
func GCPTokenSource(ctx context.Context) oauth2.TokenSource {
	initTokenSource(ctx)
	return cachedTS
}

// getClusterEndpoint returns the DNS endpoint (preferred) or private endpoint + CA.
func getClusterEndpoint(ctx context.Context, project, location, clusterName string) (endpoint, ca string, err error) {
	c, err := container.NewClusterManagerClient(ctx, option.WithTokenSource(GCPTokenSource(ctx)))
	if err != nil {
		return "", "", fmt.Errorf("create GKE client: %w", err)
	}
	defer c.Close()

	name := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, location, clusterName)
	cluster, err := c.GetCluster(ctx, &containerpb.GetClusterRequest{Name: name})
	if err != nil {
		return "", "", fmt.Errorf("get cluster %s: %w", clusterName, err)
	}

	// Prefer DNS endpoint (no CA cert needed, uses public PKI).
	if cfg := cluster.GetControlPlaneEndpointsConfig(); cfg != nil {
		if dns := cfg.GetDnsEndpointConfig(); dns != nil && dns.GetEndpoint() != "" {
			return "https://" + dns.GetEndpoint(), "", nil
		}
	}

	// Fallback to private endpoint + CA.
	return "https://" + cluster.GetEndpoint(), cluster.GetMasterAuth().GetClusterCaCertificate(), nil
}

// gcpKubeconfig generates a kubeconfig YAML with a bearer token.
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

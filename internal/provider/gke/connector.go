// Package gke implements KCI-Connect (pkg/provider.Connector) for GKE clusters.
//
// # How it works
//
// The hub Kubernetes pod uses Application Default Credentials (ADC) — specifically
// GKE Workload Identity — to authenticate to spoke cluster API servers without
// any static credentials. No Secrets, no kubeconfigs stored in CRDs.
//
// # Per-request flow
//
//  1. ADC token source (initialized once) exchanges the pod's GKE SA projection
//     for a short-lived Google OAuth2 access token automatically.
//  2. Connector.Connect() fetches the spoke cluster's API endpoint and CA cert
//     from the GKE Container API (cached 1 h per cluster).
//  3. Returns a *rest.Config with oauth2.Transport as the round-tripper so every
//     request carries a fresh Bearer token (auto-refreshed before expiry).
//
// # GCP IAM required
//
// Hub GSA (bound to the hub KSA via iam.gke.io/gcp-service-account annotation):
//   - roles/container.clusterViewer  on each spoke project  (Container API describe)
//   - roles/container.developer      on each spoke cluster   (K8s API calls via direct endpoint)
//
// Spoke K8s RBAC required:
//   - A ClusterRoleBinding granting the hub GSA identity (user: HUB_GSA@HUB_PROJECT.iam.gserviceaccount.com)
//     the permissions Kapro needs (read/patch OCIRepository, read nodes, etc).
//
// # Network requirements
//
// Direct-endpoint mode (the default) requires the hub cluster to have network
// reachability to the spoke's control-plane IP.  For private spoke clusters
// across GCP projects, use VPC peering from the hub VPC to each spoke VPC, or
// a Shared VPC topology.
//
// Connect Gateway support (GKE Fleet, no VPC peering needed) is tracked for v0.4.
package gke

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgprovider "kapro.io/kapro/pkg/provider"
)

const (
	// cloudPlatformScope grants read access to GKE Container API and full K8s API access.
	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

	containerAPIBase = "https://container.googleapis.com/v1"

	// endpointCacheTTL is how long a cluster's endpoint + CA are considered fresh.
	// GKE cluster endpoints are stable; 1 h is conservative.
	endpointCacheTTL = time.Hour

	// reachabilityTimeout is the deadline for the lightweight /api liveness probe.
	reachabilityTimeout = 10 * time.Second
)

// compile-time check: Connector satisfies the KCI Connector interface.
var _ pkgprovider.Connector = &Connector{}

// Connector is the GKE KCI implementation.
// Create via NewConnector(); the zero value is valid but inefficient
// (token source and cache are lazily initialized on first Connect call).
type Connector struct {
	mu            sync.Mutex
	tokenSource   oauth2.TokenSource       // ADC, lazily initialised
	endpointCache map[string]*clusterEntry // key = gkeKey(spec)
}

type clusterEntry struct {
	endpoint  string    // bare IP or hostname, without scheme
	caData    []byte    // PEM-encoded cluster CA certificate
	expiresAt time.Time // evict after this time
}

// NewConnector returns a GKE Connector ready for use.
func NewConnector() *Connector {
	return &Connector{
		endpointCache: make(map[string]*clusterEntry),
	}
}

// Connect returns a *rest.Config that allows the hub to call the spoke's
// Kubernetes API server directly using GKE Workload Identity.
//
// The returned config uses oauth2.Transport so every outbound request
// carries a fresh Bearer token — there is no stale-token risk for long
// reconcile loops.
func (c *Connector) Connect(ctx context.Context, cluster *kaprov1alpha1.MemberCluster) (*rest.Config, error) {
	if cluster == nil {
		return nil, fmt.Errorf("GKEConnector: cluster is nil")
	}
	if cluster.Spec.Provider == nil || cluster.Spec.Provider.GKE == nil {
		return nil, fmt.Errorf("GKEConnector: cluster %q missing spec.provider.gke", cluster.Name)
	}
	spec := cluster.Spec.Provider.GKE

	ts, err := c.getTokenSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("GKEConnector: token source for cluster %q: %w", cluster.Name, err)
	}

	entry, err := c.getClusterEntry(ctx, spec, ts)
	if err != nil {
		return nil, fmt.Errorf("GKEConnector: describe cluster %q (%s/%s/%s): %w",
			cluster.Name, spec.Project, spec.Location, spec.ClusterName, err)
	}

	cfg := &rest.Config{
		Host: "https://" + entry.endpoint,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: entry.caData,
		},
		// WrapTransport injects a fresh Bearer token on every request.
		// The oauth2 library refreshes the token automatically when it
		// approaches expiry — no single-token-in-flight risk.
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			return &oauth2.Transport{Source: ts, Base: rt}
		},
	}

	ctrl.Log.WithName("gke-connector").V(1).Info("built spoke rest.Config",
		"cluster", cluster.Name, "endpoint", entry.endpoint)

	return cfg, nil
}

// IsReachable returns true when the spoke cluster's API server responds to a
// lightweight liveness probe (GET /api). Returns (false, nil) — not an error —
// when the server is temporarily unreachable so the caller retries later.
func (c *Connector) IsReachable(ctx context.Context, cluster *kaprov1alpha1.MemberCluster) (bool, error) {
	cfg, err := c.Connect(ctx, cluster)
	if err != nil {
		return false, fmt.Errorf("GKEConnector.IsReachable: %w", err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, reachabilityTimeout)
	defer cancel()

	// Build a plain HTTP client using the same token transport as Connect().
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return false, fmt.Errorf("GKEConnector.IsReachable: build http client: %w", err)
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, cfg.Host+"/api", nil)
	if err != nil {
		return false, fmt.Errorf("GKEConnector.IsReachable: build request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// Network error → temporarily unreachable, not a hard failure.
		ctrl.Log.WithName("gke-connector").V(1).Info("spoke unreachable",
			"cluster", cluster.Name, "err", err)
		return false, nil
	}
	defer resp.Body.Close()

	// 4xx from kube-apiserver means we reached it (auth error is expected
	// until RBAC is configured); 5xx means it's up but unhealthy.
	return resp.StatusCode < 500, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────────────

// getTokenSource returns the lazily-initialised ADC TokenSource.
// On GKE with Workload Identity, ADC uses the pod's projected SA token.
// For local development, it uses GOOGLE_APPLICATION_CREDENTIALS or gcloud creds.
func (c *Connector) getTokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tokenSource != nil {
		return c.tokenSource, nil
	}

	ts, err := google.DefaultTokenSource(ctx, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("ADC: %w", err)
	}

	// Wrap in ReuseTokenSource so the underlying token is cached and only
	// refreshed when it is within 10 s of expiry.
	c.tokenSource = oauth2.ReuseTokenSource(nil, ts)
	return c.tokenSource, nil
}

// getClusterEntry returns cached cluster info or fetches it from the Container API.
// Cache is per (project, location, clusterName); TTL = endpointCacheTTL.
// On a Container API or TLS error the stale entry (if any) is evicted so the
// next call triggers a fresh fetch.
func (c *Connector) getClusterEntry(
	ctx context.Context,
	spec *kaprov1alpha1.GKEProviderSpec,
	ts oauth2.TokenSource,
) (*clusterEntry, error) {
	key := gkeKey(spec)

	c.mu.Lock()
	if e, ok := c.endpointCache[key]; ok && time.Now().Before(e.expiresAt) {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	// Fetch outside the lock — Container API calls can be slow.
	entry, err := fetchClusterInfo(ctx, spec, ts)
	if err != nil {
		c.mu.Lock()
		delete(c.endpointCache, key) // evict stale entry on error
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	if c.endpointCache == nil {
		c.endpointCache = make(map[string]*clusterEntry)
	}
	c.endpointCache[key] = entry
	c.mu.Unlock()

	return entry, nil
}

// fetchClusterInfo calls the GKE Container API to get the cluster endpoint + CA.
func fetchClusterInfo(ctx context.Context, spec *kaprov1alpha1.GKEProviderSpec, ts oauth2.TokenSource) (*clusterEntry, error) {
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/locations/%s/clusters/%s",
		containerAPIBase, spec.Project, spec.Location, spec.ClusterName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build Container API request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Container API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Container API returned %d for cluster %s/%s/%s",
			resp.StatusCode, spec.Project, spec.Location, spec.ClusterName)
	}

	var result struct {
		Endpoint   string `json:"endpoint"`
		MasterAuth struct {
			ClusterCaCertificate string `json:"clusterCaCertificate"`
		} `json:"masterAuth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Container API response: %w", err)
	}
	if result.Endpoint == "" {
		return nil, fmt.Errorf("Container API returned empty endpoint for cluster %s/%s/%s",
			spec.Project, spec.Location, spec.ClusterName)
	}

	// The Container API returns the CA cert as base64(DER).
	// rest.TLSClientConfig.CAData expects PEM bytes.
	caData, err := derToPEM(result.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("decode cluster CA for %s/%s/%s: %w",
			spec.Project, spec.Location, spec.ClusterName, err)
	}

	return &clusterEntry{
		endpoint:  result.Endpoint,
		caData:    caData,
		expiresAt: time.Now().Add(endpointCacheTTL),
	}, nil
}

// derToPEM converts a base64-encoded DER certificate (as returned by the GKE
// Container API's masterAuth.clusterCaCertificate field) to PEM bytes.
func derToPEM(b64DER string) ([]byte, error) {
	derBytes, err := base64.StdEncoding.DecodeString(b64DER)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	}), nil
}

// gkeKey returns a stable cache key for a GKEProviderSpec.
func gkeKey(spec *kaprov1alpha1.GKEProviderSpec) string {
	return spec.Project + "/" + spec.Location + "/" + spec.ClusterName
}

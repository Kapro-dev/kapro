// Package provider implements KCI (Kapro Cluster Interface) for Rancher-managed
// clusters via the Rancher v3 Norman REST API.
//
// Auth: bearer token from a K8s Secret named in RancherProviderSpec.TokenSecretRef.
// No Rancher SDK — raw HTTP to /v3/clusters/<id>/generateKubeconfig.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kci "kapro.io/kapro/pkg/provider"
)

// Connector implements kci.Connector for Rancher.
type Connector struct {
	// HTTPClient is used for all Rancher API calls.
	// Defaults to a 15s timeout client when nil.
	HTTPClient *http.Client
}

var _ kci.Connector = (*Connector)(nil)

// Connect returns a *rest.Config for the cluster described by env.
// The bearer token is resolved by the plugin gRPC server before calling this
// method, and injected via WithToken(ctx, token).
func (c *Connector) Connect(ctx context.Context, env *kaprov1alpha1.Environment) (*rest.Config, error) {
	serverURL, clusterID, token, err := resolveParams(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("rancher connector: %w", err)
	}

	kubeconfig, err := c.generateKubeconfig(ctx, serverURL, clusterID, token)
	if err != nil {
		return nil, fmt.Errorf("rancher connector: cluster %s: %w", clusterID, err)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("rancher connector: parse kubeconfig cluster %s: %w", clusterID, err)
	}
	return cfg, nil
}

// IsReachable returns true when the Rancher cluster is in "active" state.
func (c *Connector) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	serverURL, clusterID, token, err := resolveParams(ctx, env)
	if err != nil {
		return false, fmt.Errorf("rancher connector: %w", err)
	}

	url := fmt.Sprintf("%s/v3/clusters/%s", strings.TrimRight(serverURL, "/"), clusterID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, nil // network error → not reachable; don't propagate
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	var cluster struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cluster); err != nil {
		return false, nil
	}
	return cluster.State == "active", nil
}

// generateKubeconfig calls POST /v3/clusters/<id>?action=generateKubeconfig.
func (c *Connector) generateKubeconfig(ctx context.Context, serverURL, clusterID, token string) (string, error) {
	url := fmt.Sprintf("%s/v3/clusters/%s?action=generateKubeconfig",
		strings.TrimRight(serverURL, "/"), clusterID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Rancher API %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("unmarshal generateKubeconfig response: %w", err)
	}
	if result.Config == "" {
		return "", fmt.Errorf("empty kubeconfig in response")
	}
	return result.Config, nil
}

func (c *Connector) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// ctxTokenKey is the context key carrying the Rancher bearer token.
type ctxTokenKey struct{}

// WithToken returns ctx with the Rancher bearer token attached.
// Called by the plugin gRPC server after reading the K8s Secret.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxTokenKey{}, token)
}

// resolveParams extracts and validates the three params needed for every Rancher call.
func resolveParams(ctx context.Context, env *kaprov1alpha1.Environment) (serverURL, clusterID, token string, err error) {
	if env.Spec.Provider == nil || env.Spec.Provider.Rancher == nil {
		return "", "", "", fmt.Errorf("environment %s: spec.provider.rancher is nil", env.Name)
	}
	r := env.Spec.Provider.Rancher
	if r.ServerURL == "" {
		return "", "", "", fmt.Errorf("environment %s: spec.provider.rancher.serverURL is empty", env.Name)
	}
	if r.ClusterID == "" {
		return "", "", "", fmt.Errorf("environment %s: spec.provider.rancher.clusterID is empty", env.Name)
	}
	t, ok := ctx.Value(ctxTokenKey{}).(string)
	if !ok || t == "" {
		return "", "", "", fmt.Errorf("environment %s: bearer token missing — call WithToken(ctx, token) before Connect", env.Name)
	}
	return r.ServerURL, r.ClusterID, t, nil
}

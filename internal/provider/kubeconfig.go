package provider

import (
	"context"
	"fmt"
	"os"
)

// KubeconfigProvider uses a static kubeconfig file.
// Works with any Kubernetes cluster: kind, EKS, AKS, GKE, on-prem.
type KubeconfigProvider struct {
	KubeconfigPath string
}

var _ Provider = (*KubeconfigProvider)(nil)

func (p *KubeconfigProvider) Name() string { return "kubeconfig" }

func (p *KubeconfigProvider) GenerateKubeConfig(_ context.Context, _ string) ([]byte, error) {
	if p.KubeconfigPath == "" {
		return nil, fmt.Errorf("kubeconfig path is required")
	}
	data, err := os.ReadFile(p.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig %s: %w", p.KubeconfigPath, err)
	}
	return data, nil
}

func (p *KubeconfigProvider) ListClusters(_ context.Context) ([]ClusterInfo, error) {
	return nil, fmt.Errorf("kubeconfig provider does not support cluster discovery — use kapro cluster add")
}

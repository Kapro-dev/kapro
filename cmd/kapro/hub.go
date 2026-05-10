package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/bootstrap"
	"kapro.io/kapro/internal/provider"
)

func newHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Manage the Kapro hub cluster",
	}
	cmd.AddCommand(newHubInitCmd())
	return cmd
}

func newHubInitCmd() *cobra.Command {
	var (
		kubeconfig  string
		project     string
		clusterName string
		location    string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the hub cluster",
		Long: `Installs everything the hub needs to run Kapro:

  1. flux-system namespace
  2. flux-operator (CRDs + controller)
  3. FluxInstance (source, kustomize, helm controllers)
  4. Kapro CRDs

After init, deploy the kapro-operator via Helm or run locally with KAPRO_DEV_MODE=1.

Examples:
  # GCP — resolve cluster via SDK (no kubeconfig file needed)
  kapro hub init --project my-project --cluster kapro-hub --location europe-west1-b

  # Kubeconfig — any cluster (kind, EKS, AKS, on-prem)
  kapro hub init --kubeconfig /path/to/hub.yaml

  # Current kubectl context
  kapro hub init`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHubInit(cmd.Context(), kubeconfig, project, clusterName, location)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to hub kubeconfig (any cluster)")
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID")
	cmd.Flags().StringVar(&clusterName, "cluster", "", "GKE cluster name")
	cmd.Flags().StringVar(&location, "location", "", "GKE region/zone (e.g. europe-west1-b)")
	return cmd
}

func runHubInit(ctx context.Context, kubeconfigPath, project, clusterName, location string) error {
	c, err := resolveHubClient(ctx, kubeconfigPath, project, clusterName, location)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}

	target := "current context"
	if clusterName != "" {
		target = fmt.Sprintf("%s/%s/%s", project, location, clusterName)
	} else if kubeconfigPath != "" {
		target = kubeconfigPath
	}
	fmt.Fprintf(os.Stderr, "Bootstrapping hub cluster (%s)...\n", target)

	// 1. Ensure flux-system namespace.
	fmt.Fprintln(os.Stderr, "  [1/4] Creating flux-system namespace...")
	if err := bootstrap.EnsureNamespace(ctx, c, "flux-system"); err != nil {
		return fmt.Errorf("create namespace: %w", err)
	}

	// 2. Install flux-operator.
	fmt.Fprintf(os.Stderr, "  [2/4] Installing flux-operator %s...\n", bootstrap.FluxOperatorVersion)
	if err := bootstrap.InstallFluxOperator(ctx, c); err != nil {
		return fmt.Errorf("install flux-operator: %w", err)
	}

	// 3. Create FluxInstance.
	fmt.Fprintln(os.Stderr, "  [3/4] Creating FluxInstance (source + kustomize + helm controllers)...")
	if err := bootstrap.InstallFluxInstance(ctx, c); err != nil {
		return fmt.Errorf("create FluxInstance: %w", err)
	}

	// 4. Install Kapro CRDs.
	fmt.Fprintln(os.Stderr, "  [4/4] Installing Kapro CRDs...")
	if err := bootstrap.InstallKaproCRDs(ctx, c); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: %v (CRDs may be installed via Helm chart)\n", err)
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Hub cluster bootstrapped.")
	fmt.Fprintln(os.Stderr, "Next: deploy kapro-operator via Helm or run locally with KAPRO_DEV_MODE=1")
	return nil
}

// resolveHubClient builds a controller-runtime client for the hub cluster.
// Priority: --project/--cluster/--location (GCP SDK) → --kubeconfig (file) → current context.
func resolveHubClient(ctx context.Context, kubeconfigPath, project, clusterName, location string) (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)

	// GCP mode: resolve cluster via SDK, generate kubeconfig in memory.
	if project != "" && clusterName != "" {
		// Auto-detect location from Fleet API if not provided.
		if location == "" {
			fleetProvider := &provider.GCPFleetProvider{Project: project}
			kubeconfigData, err := fleetProvider.GenerateKubeConfig(ctx, clusterName)
			if err != nil {
				return nil, fmt.Errorf("auto-detect cluster location via Fleet API: %w (use --location to specify manually)", err)
			}
			restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
			if err != nil {
				return nil, fmt.Errorf("parse kubeconfig: %w", err)
			}
			return client.New(restConfig, client.Options{Scheme: scheme})
		}
		p := &provider.GCPBasicProvider{
			Project:  project,
			Location: location,
		}
		kubeconfigData, err := p.GenerateKubeConfig(ctx, clusterName)
		if err != nil {
			return nil, fmt.Errorf("resolve GKE cluster %s/%s/%s: %w", project, location, clusterName, err)
		}
		restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
		if err != nil {
			return nil, fmt.Errorf("parse kubeconfig: %w", err)
		}
		return client.New(restConfig, client.Options{Scheme: scheme})
	}

	// Kubeconfig file or current context.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{})

	cfg, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

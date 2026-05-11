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
	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	"google.golang.org/api/option"

	"kapro.io/kapro/internal/bootstrap"
	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/internal/gcputil"
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
	// Interactive mode: no flags → scan projects → pick cluster.
	if kubeconfigPath == "" && project == "" {
		var err error
		project, err = gcputil.SelectProject(ctx)
		if err != nil {
			return fmt.Errorf("select project: %w", err)
		}
	}
	if kubeconfigPath == "" && clusterName == "" && project != "" {
		var err error
		clusterName, location, err = gcputil.SelectCluster(ctx, project)
		if err != nil {
			return fmt.Errorf("select cluster: %w", err)
		}
	}

	// Auto-detect location if needed (before connecting).
	// Always use GKE Container API (returns exact zone like europe-west1-b),
	// not Fleet API (returns region like europe-west1).
	if project != "" && clusterName != "" && location == "" {
		detected, err := detectClusterLocation(ctx, project, clusterName)
		if err != nil {
			return fmt.Errorf("auto-detect location: %w", err)
		}
		location = detected
	}

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
	fmt.Fprintf(os.Stderr, "\n  Bootstrapping hub cluster (%s)\n\n", target)

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Creating flux-system namespace", func() error {
			return bootstrap.EnsureNamespace(ctx, c, "flux-system")
		}},
		{fmt.Sprintf("Installing flux-operator %s", bootstrap.FluxOperatorVersion), func() error {
			return bootstrap.InstallFluxOperator(ctx, c)
		}},
		{"Creating FluxInstance (source + kustomize + helm controllers)", func() error {
			return bootstrap.InstallFluxInstance(ctx, c)
		}},
		{"Installing Kapro CRDs", func() error {
			return bootstrap.InstallKaproCRDs(ctx, c)
		}},
	}

	// GCP-only steps.
	if project != "" && clusterName != "" {
		steps = append(steps, struct {
			name string
			fn   func() error
		}{"Registering hub in GKE Fleet", func() error {
			return bootstrap.RegisterFleetMembership(ctx, project, clusterName, location)
		}})
		steps = append(steps, struct {
			name string
			fn   func() error
		}{"Creating centralized GAR registry", func() error {
			info, err := bootstrap.EnsureGARRegistry(ctx, project, location, "kapro-registry")
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "\n    Registry: oci://%s\n", info.URL)
			return nil
		}})
	}

	for i, step := range steps {
		sp := cli.NewSpinner(fmt.Sprintf("[%d/%d] %s", i+1, len(steps), step.name))
		sp.Start()
		if err := step.fn(); err != nil {
			sp.StopFail(fmt.Sprintf("[%d/%d] %s: %v", i+1, len(steps), step.name, err))
			// Non-fatal for CRDs and Fleet — continue.
			if i < 3 {
				return err
			}
			continue
		}
		sp.StopSuccess(fmt.Sprintf("[%d/%d] %s", i+1, len(steps), step.name))
	}

	fmt.Fprintln(os.Stderr)
	cli.Success("Hub cluster bootstrapped")
	cli.Muted("Next: deploy kapro-operator via Helm or run locally with KAPRO_DEV_MODE=1")
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

// detectClusterLocation finds a GKE cluster's location by listing all clusters
// in the project. Tries Fleet API first (faster, label-aware), then falls back
// to the GKE Container API (lists all locations).
func detectClusterLocation(ctx context.Context, project, clusterName string) (string, error) {
	// GKE Container API first — returns exact zone (europe-west1-b), not region.
	// Fleet API returns region (europe-west1) which doesn't work for kubeconfig generation.
	ts := provider.GCPTokenSource(ctx)
	clusterClient, err := container.NewClusterManagerClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return "", fmt.Errorf("create GKE client: %w", err)
	}
	defer clusterClient.Close()

	parent := fmt.Sprintf("projects/%s/locations/-", project)
	resp, err := clusterClient.ListClusters(ctx, &containerpb.ListClustersRequest{Parent: parent})
	if err != nil {
		return "", fmt.Errorf("list GKE clusters: %w", err)
	}
	for _, cluster := range resp.GetClusters() {
		if cluster.GetName() == clusterName {
			return cluster.GetLocation(), nil
		}
	}

	return "", fmt.Errorf("cluster %q not found in project %s", clusterName, project)
}

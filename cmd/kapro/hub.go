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
	var kubeconfig string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the hub cluster",
		Long: `Installs everything the hub needs to run Kapro:

  1. flux-system namespace
  2. flux-operator (CRDs + controller)
  3. FluxInstance (source, kustomize, helm controllers)
  4. Kapro CRDs

After init, deploy the kapro-operator via Helm or kubectl.

Examples:
  kapro hub init
  kapro hub init --kubeconfig /path/to/hub.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHubInit(cmd.Context(), kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to hub kubeconfig")
	return cmd
}

func runHubInit(ctx context.Context, kubeconfigPath string) error {
	c, err := buildClientForHub(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Bootstrapping hub cluster...")

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
		// Non-fatal — CRDs might come from the Helm chart instead.
		fmt.Fprintf(os.Stderr, "  warning: %v (CRDs may be installed via Helm chart)\n", err)
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Hub cluster bootstrapped successfully.")
	fmt.Fprintln(os.Stderr, "Next: deploy kapro-operator via Helm or run locally with KAPRO_DEV_MODE=1")
	return nil
}

func buildClientForHub(kubeconfigPath string) (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)

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

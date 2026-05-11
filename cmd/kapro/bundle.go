package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/bundle"
	kaproconfig "kapro.io/kapro/internal/config"
)

func newBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Generate and push OCI bundles for spoke clusters",
		Long: `Bundle commands generate spoke-side Flux manifests (HelmReleases,
HelmRepositories, wave Kustomizations) from a KaproApp spec and push
them as OCI artifacts to a container registry.

Used by CI pipelines to prepare bundles before triggering a Release.`,
	}
	cmd.AddCommand(newBundleGenerateCmd())
	return cmd
}

func newBundleGenerateCmd() *cobra.Command {
	var (
		appName    string
		bundleName string
		version    string
		registry   string
		outputDir  string
		push       bool
		kubeconfig string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate spoke bundle from KaproApp spec",
		Long: `Reads a KaproApp CR from the hub cluster and generates an OCI bundle
containing per-wave directories with HelmReleases and HelmRepositories.

With --push, also pushes the bundle to the OCI registry.

Examples:
  # Generate to local directory (dry-run)
  kapro bundle generate --app hello-spoke-app --version 2.0.0 --output /tmp/bundle

  # Generate and push to GAR
  kapro bundle generate --app hello-spoke-app --version 2.0.0 \
    --registry oci://europe-west1-docker.pkg.dev/project/repo --push

  # In CI pipeline
  kapro bundle generate --app hello-spoke-app --name hello-spoke --version ${VERSION} \
    --registry ${OCI_REGISTRY} --push \
    --kubeconfig ${HUB_KUBECONFIG}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := bundleName
			if name == "" {
				name = appName
			}
			return runBundleGenerate(cmd.Context(), appName, name, version, registry, outputDir, push, kubeconfig)
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "KaproApp CR name (required)")
	cmd.Flags().StringVar(&bundleName, "name", "", "Bundle name prefix (default: same as --app, should match Kapro CR name)")
	cmd.Flags().StringVar(&version, "version", "", "Bundle version / OCI tag (required)")
	cmd.Flags().StringVar(&registry, "registry", "", "OCI registry URL (required for --push)")
	cmd.Flags().StringVar(&outputDir, "output", "", "Output directory (default: temp dir, printed to stdout)")
	cmd.Flags().BoolVar(&push, "push", false, "Push bundle to OCI registry after generating")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to hub kubeconfig (default: in-cluster or ~/.kube/config)")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func runBundleGenerate(ctx context.Context, appName, bundleName, version, registry, outputDir string, push bool, kubeconfigPath string) error {
	if registry == "" {
		cfg, _ := kaproconfig.Load()
		registry = cfg.Registry("default")
	}
	if push && registry == "" {
		return fmt.Errorf("--registry is required when using --push")
	}

	// Build client to read KaproApp from hub.
	hubClient, err := buildHubClient(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}

	// Read KaproApp.
	var app kaprov1alpha1.KaproApp
	if err := hubClient.Get(ctx, client.ObjectKey{Name: appName}, &app); err != nil {
		return fmt.Errorf("get KaproApp %q: %w", appName, err)
	}

	fmt.Fprintf(os.Stderr, "Read KaproApp %q: %d components, %d registries\n",
		appName, len(app.Spec.Components), len(app.Spec.Registries))

	// Validate.
	if err := bundle.Validate(&app); err != nil {
		return fmt.Errorf("validation failed:\n%w", err)
	}

	// Generate bundle.
	req := bundle.BundleRequest{
		KaproName: bundleName,
		App:       &app,
		Version:   version,
		Registry:  registry,
	}
	manifests := bundle.Generate(req)

	// Write to output directory.
	dir := outputDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "kapro-bundle-*")
		if err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
	}

	for relPath, content := range manifests {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return fmt.Errorf("create dir for %s: %w", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	fmt.Fprintf(os.Stderr, "Generated %d files in %s\n", len(manifests), dir)

	// List generated files.
	for relPath := range manifests {
		fmt.Println(filepath.Join(dir, relPath))
	}

	// Push if requested.
	if push {
		ociURL, err := bundle.Push(ctx, dir, req)
		if err != nil {
			return fmt.Errorf("push bundle: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Pushed: %s\n", ociURL)
	}

	return nil
}

func buildHubClient(kubeconfigPath string) (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	cfg, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/bundle"
	kaproconfig "kapro.io/kapro/internal/config"
)

func newSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Work with Kapro source units",
		Long: `Source commands package Kapro source units into deployable artifacts
for pull-mode spokes when a substrate needs an OCI artifact.

Kapro promotes revisions. The selected substrate owns local sync and rollout.`,
	}
	cmd.AddCommand(newSourcePackageCmd())
	cmd.AddCommand(newSourceApplyCmd())
	return cmd
}

func newSourcePackageCmd() *cobra.Command {
	var (
		deliveryUnitRef string
		sourceRef       string
		fleetName       string
		version         string
		registry        string
		outputDir       string
		push            bool
		kubeconfig      string
	)

	cmd := &cobra.Command{
		Use:   "package",
		Short: "Package Kapro source units for pull-mode spokes",
		Long: `Reads source units from a DeliveryUnit, Fleet, or advanced Source CR and
packages them into an OCI artifact containing per-wave directories with
HelmReleases and HelmRepositories.

With --push, also pushes the artifact to the OCI registry.

Examples:
  # Package to local directory (dry-run)
  kapro source package --fleet checkout --version 2.0.0 --output /tmp/kapro-source

  # Package and push to GAR
  kapro source package --delivery-unit checkout --fleet checkout --version 2.0.0 \
    --registry oci://europe-west1-docker.pkg.dev/project/repo --push

  # Advanced: package a reusable Source CR
  kapro source package --source checkout --version ${VERSION} \
    --registry ${OCI_REGISTRY} --push \
    --kubeconfig ${HUB_KUBECONFIG}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := fleetName
			if name == "" {
				name = deliveryUnitRef
			}
			if name == "" {
				name = sourceRef
			}
			return runSourcePackage(cmd.Context(), deliveryUnitRef, sourceRef, name, version, registry, outputDir, push, kubeconfig)
		},
	}

	cmd.Flags().StringVar(&deliveryUnitRef, "delivery-unit", "", "DeliveryUnit CR name to package")
	cmd.Flags().StringVar(&sourceRef, "source", "", "Advanced Source CR name")
	cmd.Flags().StringVar(&fleetName, "fleet", "", "Fleet artifact name; when --source is omitted, also the Fleet CR name")
	cmd.Flags().StringVar(&version, "version", "", "Artifact version / OCI tag (required)")
	cmd.Flags().StringVar(&registry, "registry", "", "OCI registry URL (required for --push)")
	cmd.Flags().StringVar(&outputDir, "output", "", "Output directory (default: temp dir, printed to stdout)")
	cmd.Flags().BoolVar(&push, "push", false, "Push artifact to OCI registry after packaging")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to hub kubeconfig (default: in-cluster or ~/.kube/config)")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func runSourcePackage(ctx context.Context, deliveryUnitRef, sourceRef, fleetName, version, registry, outputDir string, push bool, kubeconfigPath string) error {
	if deliveryUnitRef == "" && sourceRef == "" && fleetName == "" {
		return fmt.Errorf("one of --delivery-unit, --fleet, or --source is required")
	}
	if registry == "" {
		cfg, _ := kaproconfig.Load()
		registry = cfg.Registry("default")
	}
	if push && registry == "" {
		return fmt.Errorf("--registry is required when using --push")
	}

	// Build client to read DeliveryUnit/Source catalog from hub.
	hubClient, err := buildHubClient(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}

	app, err := readPackageSource(ctx, hubClient, deliveryUnitRef, sourceRef, fleetName)
	if err != nil {
		return err
	}
	if fleetName == "" {
		fleetName = app.Name
	}

	fmt.Fprintf(os.Stderr, "Read source units from %q: %d units, %d registries\n",
		app.Name, len(app.Spec.Units), len(app.Spec.Registries))

	// Validate.
	if err := bundle.Validate(app); err != nil {
		return fmt.Errorf("validation failed:\n%w", err)
	}

	// Generate artifact contents.
	req := bundle.BundleRequest{
		KaproName: fleetName,
		Source:    app,
		Version:   version,
		Registry:  registry,
	}
	manifests := bundle.Generate(req)

	// Write to output directory.
	dir := outputDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "kapro-source-*")
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
			return fmt.Errorf("push artifact: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Pushed: %s\n", ociURL)
	}

	return nil
}

func readPackageSource(ctx context.Context, hubClient client.Client, deliveryUnitRef, sourceRef, fleetName string) (*kaprov1alpha1.Source, error) {
	if deliveryUnitRef != "" {
		var unit kaprov1alpha1.DeliveryUnit
		if err := hubClient.Get(ctx, client.ObjectKey{Name: deliveryUnitRef}, &unit); err != nil {
			return nil, fmt.Errorf("get DeliveryUnit %q: %w", deliveryUnitRef, err)
		}
		return &kaprov1alpha1.Source{
			ObjectMeta: metav1.ObjectMeta{Name: unit.Name, Labels: unit.Labels, Annotations: unit.Annotations},
			Spec:       unit.Spec.Source,
		}, nil
	}
	if sourceRef != "" {
		var source kaprov1alpha1.Source
		if err := hubClient.Get(ctx, client.ObjectKey{Name: sourceRef}, &source); err != nil {
			return nil, fmt.Errorf("get Source %q: %w", sourceRef, err)
		}
		return &source, nil
	}

	var fleet kaprov1alpha1.Fleet
	if err := hubClient.Get(ctx, client.ObjectKey{Name: fleetName}, &fleet); err != nil {
		return nil, fmt.Errorf("get fleet %q: %w", fleetName, err)
	}
	if fleet.Spec.Source == nil {
		if fleet.Spec.SourceRef != "" {
			return nil, fmt.Errorf("fleet %q references source %q; pass --source %s", fleetName, fleet.Spec.SourceRef, fleet.Spec.SourceRef)
		}
		var unit kaprov1alpha1.DeliveryUnit
		if err := hubClient.Get(ctx, client.ObjectKey{Name: fleetName}, &unit); err == nil {
			return &kaprov1alpha1.Source{
				ObjectMeta: metav1.ObjectMeta{Name: unit.Name, Labels: unit.Labels, Annotations: unit.Annotations},
				Spec:       unit.Spec.Source,
			}, nil
		}
		return nil, fmt.Errorf("fleet %q has neither legacy spec.source nor spec.sourceRef set; pass --delivery-unit %s for the public-preview authoring path", fleetName, fleetName)
	}
	return &kaprov1alpha1.Source{
		ObjectMeta: fleet.ObjectMeta,
		Spec:       *fleet.Spec.Source,
	}, nil
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

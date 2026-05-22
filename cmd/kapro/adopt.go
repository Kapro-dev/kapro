package main

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/spf13/cobra"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

type adoptAdapterOptions struct {
	Adapter      string
	BackendName  string
	Namespace    string
	Selector     string
	SyncInterval string
	Apply        bool
	DryRun       string
	Kubeconfig   string
}

func newAdoptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Generate brownfield adoption mappings",
		Long: `Adoption commands generate observe-first Kapro mappings from
existing backend-native GitOps repositories: a read-only Backend, Source units,
and discovery reports. They do not mutate live backend objects; switching a
Backend to Adopt and applying Git changes are separate, explicit steps.`,
	}
	cmd.AddCommand(newAdoptArgoCmd())
	cmd.AddCommand(newAdoptFluxCmd())
	cmd.AddCommand(newAdoptAdapterCmd("argo-cd", "argo-cd", "argo", "argocd"))
	return cmd
}

func newAdoptArgoCmd() *cobra.Command {
	opts := argoDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	cmd := &cobra.Command{
		Use:   "argo [repo]",
		Short: "Generate Kapro adoption files for an existing Argo CD repo",
		Long: `Scans an existing Argo CD Git repository using git ls-files and
generates Backend, Source, and reviewable Git adoption mapping
files. Output starts in observe mode so the generated graph can be reviewed
before any write permissions are granted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runArgoDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "argo", "Backend and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "argocd", "Argo CD namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: argocd, apps, clusters, environments, flux)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newAdoptFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	adapterOpts := adoptAdapterOptions{Adapter: "flux", BackendName: "flux", Namespace: "flux-system", Selector: "kapro.io/import=true", SyncInterval: "5m"}
	cmd := &cobra.Command{
		Use:   "flux [repo]",
		Short: "Generate Kapro adoption files for an existing Flux repo",
		Long: `Scans an existing Flux Git repository using git ls-files and
generates Backend, Source, and reviewable Git adoption mapping
files. Output starts in observe mode so the generated graph can be reviewed
before any write permissions are granted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if adapterOpts.Apply {
				adapterOpts.BackendName = opts.Name
				adapterOpts.Namespace = opts.Namespace
				adapterOpts.Selector = opts.Selector
				return runAdoptAdapter(context.Background(), adapterOpts)
			}
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runFluxDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "Backend and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	cmd.Flags().BoolVar(&adapterOpts.Apply, "apply", false, "Create or update Backend and AdapterPolicy in the current cluster instead of writing files")
	cmd.Flags().StringVar(&adapterOpts.DryRun, "dry-run", "", "Set to client to validate the live --apply writes without persisting")
	cmd.Flags().StringVar(&adapterOpts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&adapterOpts.SyncInterval, "sync-interval", adapterOpts.SyncInterval, "AdapterPolicy discovery sync interval")
	return cmd
}

func newAdoptAdapterCmd(use, adapterName, backendName, namespace string) *cobra.Command {
	opts := adoptAdapterOptions{
		Adapter:      adapterName,
		BackendName:  backendName,
		Namespace:    namespace,
		Selector:     "kapro.io/import=true",
		SyncInterval: "5m",
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: fmt.Sprintf("Create continuous %s adapter adoption resources", adapterName),
		RunE: func(_ *cobra.Command, _ []string) error {
			if !opts.Apply {
				return fmt.Errorf("%s adoption writes live resources; pass --apply to create Backend and AdapterPolicy", adapterName)
			}
			return runAdoptAdapter(context.Background(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.BackendName, "name", opts.BackendName, "Backend and AdapterPolicy base name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", opts.Namespace, "Backend-native control-plane namespace")
	cmd.Flags().StringVar(&opts.Selector, "label-selector", opts.Selector, "Label selector for adopted backend objects")
	cmd.Flags().StringVar(&opts.SyncInterval, "sync-interval", opts.SyncInterval, "AdapterPolicy discovery sync interval")
	cmd.Flags().BoolVar(&opts.Apply, "apply", false, "Create or update Backend and AdapterPolicy in the current cluster")
	cmd.Flags().StringVar(&opts.DryRun, "dry-run", "", "Set to client to validate live Backend and AdapterPolicy writes without persisting")
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runAdoptAdapter(ctx context.Context, opts adoptAdapterOptions) error {
	if opts.DryRun != "" && opts.DryRun != "client" {
		return fmt.Errorf("--dry-run must be empty or client")
	}
	if _, err := time.ParseDuration(opts.SyncInterval); err != nil {
		return fmt.Errorf("parse --sync-interval: %w", err)
	}
	matchLabels, err := parseSelector(opts.Selector)
	if err != nil {
		return err
	}
	c, err := buildClient(opts.Kubeconfig)
	if err != nil {
		return err
	}
	driver := kaprov1alpha2.BackendDriverFlux
	if opts.Adapter == "argo-cd" {
		driver = kaprov1alpha2.BackendDriverArgo
	}
	backend := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: opts.BackendName},
		Spec: kaprov1alpha2.BackendSpec{
			Driver:  driver,
			Adapter: opts.Adapter,
			Runtime: kaprov1alpha2.BackendRuntimeBoth,
			Discovery: &kaprov1alpha2.BackendDiscoverySpec{
				Enabled:          true,
				ManagementPolicy: "Observe",
				Selector:         &metav1.LabelSelector{MatchLabels: matchLabels},
			},
			Parameters: map[string]string{"namespace": opts.Namespace},
		},
	}
	policy := &kaprov1alpha2.AdapterPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: opts.BackendName + "-adopt"},
		Spec: kaprov1alpha2.AdapterPolicySpec{
			Adapter:      opts.Adapter,
			BackendRef:   opts.BackendName,
			SyncInterval: opts.SyncInterval,
		},
	}
	dryRun := opts.DryRun == "client"
	if err := createOrUpdateObject(ctx, c, backend, dryRun); err != nil {
		return err
	}
	if err := createOrUpdateObject(ctx, c, policy, dryRun); err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("Validated Backend %s and AdapterPolicy %s with client-side dry-run\n", backend.Name, policy.Name)
		return nil
	}
	fmt.Printf("Created/updated Backend %s and AdapterPolicy %s\n", backend.Name, policy.Name)
	return nil
}

func createOrUpdateObject(ctx context.Context, c client.Client, obj client.Object, dryRun bool) error {
	createOpts := []client.CreateOption{}
	patchOpts := []client.PatchOption{}
	if dryRun {
		createOpts = append(createOpts, client.DryRunAll)
		patchOpts = append(patchOpts, client.DryRunAll)
	}
	if err := c.Create(ctx, obj, createOpts...); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		// Update via merge patch against the live object: a full
		// Update() would clobber labels/annotations the operator (or
		// another tool) set after the initial adoption. Patch sends
		// only the fields we care about.
		current := obj.DeepCopyObject().(client.Object)
		if getErr := c.Get(ctx, client.ObjectKeyFromObject(obj), current); getErr != nil {
			return getErr
		}
		return c.Patch(ctx, obj, client.MergeFrom(current), patchOpts...)
	}
	return nil
}

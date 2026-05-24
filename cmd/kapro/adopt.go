package main

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/spf13/cobra"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type adoptAdapterOptions struct {
	Adapter       string
	SubstrateName string
	Namespace     string
	Selector      string
	SyncInterval  string
	Apply         bool
	DryRun        string
	Kubeconfig    string
}

func newAdoptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Generate existing GitOps adoption mappings",
		Long: `Adoption commands generate observe-first Kapro mappings from
existing substrate-native GitOps repositories: a read-only Substrate, Source units,
and discovery reports. They do not mutate live substrate objects; switching a
Substrate to Adopt and applying Git changes are separate, explicit steps.`,
	}
	cmd.AddCommand(newAdoptArgoCmd())
	cmd.AddCommand(newAdoptFluxCmd())
	return cmd
}

func newAdoptArgoCmd() *cobra.Command {
	opts := argoDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	adapterOpts := adoptAdapterOptions{Adapter: "argo", SubstrateName: "argo", Namespace: "argocd", Selector: "kapro.io/import=true", SyncInterval: "5m"}
	cmd := &cobra.Command{
		Use:   "argo [repo]",
		Short: "Generate Kapro adoption files for an existing Argo CD repo",
		Long: `Scans an existing Argo CD Git repository using git ls-files and
generates Substrate, Source, and reviewable Git adoption mapping
files. Output starts in observe mode so the generated graph can be reviewed
before any write permissions are granted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if adapterOpts.Apply {
				adapterOpts.SubstrateName = opts.Name
				adapterOpts.Namespace = opts.Namespace
				adapterOpts.Selector = opts.Selector
				return runAdoptAdapter(context.Background(), adapterOpts)
			}
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runArgoDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "argo", "Substrate and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "argocd", "Argo CD namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported substrate objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: argocd, apps, clusters, environments, flux)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	cmd.Flags().BoolVar(&adapterOpts.Apply, "apply", false, "Create or update Substrate and AdapterPolicy in the current cluster instead of writing files")
	cmd.Flags().StringVar(&adapterOpts.DryRun, "dry-run", "", "Set to client to validate the live --apply writes without persisting")
	cmd.Flags().StringVar(&adapterOpts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&adapterOpts.SyncInterval, "sync-interval", adapterOpts.SyncInterval, "AdapterPolicy discovery sync interval")
	return cmd
}

func newAdoptFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	adapterOpts := adoptAdapterOptions{Adapter: "flux", SubstrateName: "flux", Namespace: "flux-system", Selector: "kapro.io/import=true", SyncInterval: "5m"}
	cmd := &cobra.Command{
		Use:   "flux [repo]",
		Short: "Generate Kapro adoption files for an existing Flux repo",
		Long: `Scans an existing Flux Git repository using git ls-files and
generates Substrate, Source, and reviewable Git adoption mapping
files. Output starts in observe mode so the generated graph can be reviewed
before any write permissions are granted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if adapterOpts.Apply {
				adapterOpts.SubstrateName = opts.Name
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
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "Substrate and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported substrate objects")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	cmd.Flags().BoolVar(&adapterOpts.Apply, "apply", false, "Create or update Substrate and AdapterPolicy in the current cluster instead of writing files")
	cmd.Flags().StringVar(&adapterOpts.DryRun, "dry-run", "", "Set to client to validate the live --apply writes without persisting")
	cmd.Flags().StringVar(&adapterOpts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&adapterOpts.SyncInterval, "sync-interval", adapterOpts.SyncInterval, "AdapterPolicy discovery sync interval")
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
	substrateKind := "flux"
	if opts.Adapter == "argo" {
		substrateKind = "argo"
	}
	substrate := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName},
		Spec: kaprov1alpha1.SubstrateSpec{
			Substrate: &kaprov1alpha1.SubstrateImplementationSpec{
				Kind:     substrateKind,
				Actuator: opts.Adapter,
			},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
			Discovery: &kaprov1alpha1.SubstrateDiscoverySpec{
				Enabled:          true,
				ManagementPolicy: "Observe",
				Selector:         &metav1.LabelSelector{MatchLabels: matchLabels},
			},
			Parameters: map[string]string{"namespace": opts.Namespace},
		},
	}
	policy := &kaprov1alpha1.AdapterPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName + "-adopt"},
		Spec: kaprov1alpha1.AdapterPolicySpec{
			Adapter:      opts.Adapter,
			SubstrateRef: opts.SubstrateName,
			SyncInterval: opts.SyncInterval,
		},
	}
	dryRun := opts.DryRun == "client"
	if err := createOrUpdateObject(ctx, c, substrate, dryRun); err != nil {
		return err
	}
	if err := createOrUpdateObject(ctx, c, policy, dryRun); err != nil {
		return err
	}
	if dryRun {
		fmt.Printf("Validated Substrate %s and AdapterPolicy %s with client-side dry-run\n", substrate.Name, policy.Name)
		return nil
	}
	fmt.Printf("Created/updated Substrate %s and AdapterPolicy %s\n", substrate.Name, policy.Name)
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
		preserveObjectMetadata(current, obj)
		return c.Patch(ctx, obj, client.MergeFrom(current), patchOpts...)
	}
	return nil
}

func preserveObjectMetadata(current, desired client.Object) {
	desired.SetResourceVersion(current.GetResourceVersion())
	desired.SetUID(current.GetUID())
	desired.SetCreationTimestamp(current.GetCreationTimestamp())
	desired.SetGeneration(current.GetGeneration())
	desired.SetManagedFields(current.GetManagedFields())
	desired.SetFinalizers(current.GetFinalizers())
	desired.SetOwnerReferences(current.GetOwnerReferences())
	desired.SetLabels(mergeStringMaps(current.GetLabels(), desired.GetLabels()))
	desired.SetAnnotations(mergeStringMaps(current.GetAnnotations(), desired.GetAnnotations()))
}

func mergeStringMaps(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

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
	argocdsubstratev1alpha1 "kapro.io/kapro/api/substrate/argocd/v1alpha1"
	fluxsubstratev1alpha1 "kapro.io/kapro/api/substrate/flux/v1alpha1"
)

type importSubstrateOptions struct {
	SubstrateKind string
	SubstrateName string
	Namespace     string
	Selector      string
	SyncInterval  string
	Adopt         bool
	Apply         bool
	DryRun        string
	Kubeconfig    string
}

func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import existing GitOps repositories into Kapro",
		Long: `Import commands generate observe-first Kapro mappings from existing GitOps
repositories: a read-only Substrate, typed substrate config, DeliveryUnit source
mappings, and discovery reports. They do not mutate live substrate objects unless --adopt
is set; --adopt switches the generated or live Substrate management policy to
Adopt after review.`,
	}
	cmd.AddCommand(newImportArgoCmd())
	cmd.AddCommand(newImportFluxCmd())
	return cmd
}

func newImportArgoCmd() *cobra.Command {
	opts := argoDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	substrateOpts := importSubstrateOptions{SubstrateKind: "argo", SubstrateName: "argo", Namespace: "argocd", Selector: "kapro.io/import=true", SyncInterval: "5m"}
	cmd := &cobra.Command{
		Use:   "argo [repo]",
		Short: "Import an existing Argo CD repo into Kapro",
		Long: `Scans an existing Argo CD Git repository using git ls-files and
generates SubstrateClass, typed substrate config, Substrate, DeliveryUnit source
mappings, and reviewable Git adoption mapping files. Output starts in observe
mode so the generated graph can be reviewed before any write permissions are
granted. Pass --adopt only after review to generate or apply an Adopt-mode
Substrate.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if substrateOpts.Apply {
				substrateOpts.SubstrateName = opts.Name
				substrateOpts.Namespace = opts.Namespace
				substrateOpts.Selector = opts.Selector
				substrateOpts.Adopt = opts.Adopt
				return runImportSubstrate(context.Background(), substrateOpts)
			}
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runArgoDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "argo", "Substrate and DeliveryUnit name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "argocd", "Argo CD namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported substrate objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: argocd, apps, clusters, environments, flux)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum source mapping units to generate (0 = unlimited)")
	addAdoptModeFlags(cmd, &opts.Adopt)
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	cmd.Flags().BoolVar(&substrateOpts.Apply, "apply", false, "Create or update substrate class, config, Substrate, and SubstrateDiscoveryPolicy in the current cluster instead of writing files")
	cmd.Flags().StringVar(&substrateOpts.DryRun, "dry-run", "", "Set to client to validate the live --apply writes without persisting")
	cmd.Flags().StringVar(&substrateOpts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&substrateOpts.SyncInterval, "sync-interval", substrateOpts.SyncInterval, "SubstrateDiscoveryPolicy discovery sync interval")
	return cmd
}

func newImportFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	substrateOpts := importSubstrateOptions{SubstrateKind: "flux", SubstrateName: "flux", Namespace: "flux-system", Selector: "kapro.io/import=true", SyncInterval: "5m"}
	cmd := &cobra.Command{
		Use:   "flux [repo]",
		Short: "Import an existing Flux repo into Kapro",
		Long: `Scans an existing Flux Git repository using git ls-files and
generates SubstrateClass, typed substrate config, Substrate, DeliveryUnit source
mappings, and reviewable Git adoption mapping files. Output starts in observe
mode so the generated graph can be reviewed before any write permissions are
granted. Pass --adopt only after review to generate or apply an Adopt-mode
Substrate.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if substrateOpts.Apply {
				substrateOpts.SubstrateName = opts.Name
				substrateOpts.Namespace = opts.Namespace
				substrateOpts.Selector = opts.Selector
				substrateOpts.Adopt = opts.Adopt
				return runImportSubstrate(context.Background(), substrateOpts)
			}
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runFluxDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "Substrate and DeliveryUnit name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported substrate objects")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum source mapping units to generate (0 = unlimited)")
	addAdoptModeFlags(cmd, &opts.Adopt)
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	cmd.Flags().BoolVar(&substrateOpts.Apply, "apply", false, "Create or update substrate class, config, Substrate, and SubstrateDiscoveryPolicy in the current cluster instead of writing files")
	cmd.Flags().StringVar(&substrateOpts.DryRun, "dry-run", "", "Set to client to validate the live --apply writes without persisting")
	cmd.Flags().StringVar(&substrateOpts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&substrateOpts.SyncInterval, "sync-interval", substrateOpts.SyncInterval, "SubstrateDiscoveryPolicy discovery sync interval")
	return cmd
}

func addAdoptModeFlags(cmd *cobra.Command, target *bool) {
	cmd.Flags().BoolVar(target, "adopt", false, "Switch generated or applied discovery to Adopt mode after review")
	cmd.Flags().BoolVar(target, "take", false, "Deprecated alias for --adopt")
	_ = cmd.Flags().MarkHidden("take")
}

func runImportSubstrate(ctx context.Context, opts importSubstrateOptions) error {
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
	if opts.SubstrateKind == "argo" {
		substrateKind = "argo"
	}
	managementPolicy := "Observe"
	if opts.Adopt {
		managementPolicy = "Adopt"
	}
	dryRun := opts.DryRun == "client"
	for _, obj := range importSubstrateObjects(opts, substrateKind, managementPolicy, matchLabels) {
		if err := createOrUpdateObject(ctx, c, obj, dryRun); err != nil {
			return err
		}
	}
	if dryRun {
		fmt.Printf("Validated SubstrateClass %s, typed config %s, Substrate %s, and SubstrateDiscoveryPolicy %s with client-side dry-run\n", substrateKind, opts.SubstrateName, opts.SubstrateName, opts.SubstrateName+"-import")
		return nil
	}
	fmt.Printf("Created/updated SubstrateClass %s, typed config %s, Substrate %s, and SubstrateDiscoveryPolicy %s\n", substrateKind, opts.SubstrateName, opts.SubstrateName, opts.SubstrateName+"-import")
	return nil
}

func importSubstrateObjects(opts importSubstrateOptions, substrateKind, managementPolicy string, matchLabels map[string]string) []client.Object {
	config, configRef := importSubstrateConfig(opts)
	return []client.Object{
		&kaprov1alpha1.SubstrateClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: substrateKind,
				Labels: map[string]string{
					"kapro.io/family": "gitops",
					"kapro.io/ledger": "git",
				},
			},
			Spec: kaprov1alpha1.SubstrateClassSpec{
				ControllerName: "kapro.io/" + substrateKind,
				ExecutionModes: &kaprov1alpha1.SubstrateClassExecutionModesSpec{
					Default: kaprov1alpha1.ExecutionModeHubPush,
				},
			},
		},
		config,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName},
			Spec: kaprov1alpha1.SubstrateSpec{
				ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: substrateKind},
				ConfigRef: configRef,
				Execution: &kaprov1alpha1.SubstrateExecutionSpec{
					Mode: kaprov1alpha1.ExecutionModeHubPush,
				},
				Discovery: &kaprov1alpha1.SubstrateDiscoverySpec{
					Enabled:          true,
					ManagementPolicy: managementPolicy,
					Selector:         &metav1.LabelSelector{MatchLabels: matchLabels},
				},
			},
		},
		&kaprov1alpha1.SubstrateDiscoveryPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName + "-import"},
			Spec: kaprov1alpha1.SubstrateDiscoveryPolicySpec{
				SubstrateRef: opts.SubstrateName,
				ExpectedKind: opts.SubstrateKind,
				SyncInterval: opts.SyncInterval,
			},
		},
	}
}

func importSubstrateConfig(opts importSubstrateOptions) (client.Object, *kaprov1alpha1.SubstrateObjectReference) {
	if opts.SubstrateKind == "argo" {
		return &argocdsubstratev1alpha1.ArgoCDSubstrateConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: argoCDSubstrateConfigAPIVersion,
					Kind:       "ArgoCDSubstrateConfig",
				},
				ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName},
				Spec:       argocdsubstratev1alpha1.ArgoCDSubstrateConfigSpec{Namespace: opts.Namespace},
			}, &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: argoCDSubstrateConfigAPIVersion,
				Kind:       "ArgoCDSubstrateConfig",
				Name:       opts.SubstrateName,
			}
	}
	return &fluxsubstratev1alpha1.FluxSubstrateConfig{
			TypeMeta: metav1.TypeMeta{
				APIVersion: fluxSubstrateConfigAPIVersion,
				Kind:       "FluxSubstrateConfig",
			},
			ObjectMeta: metav1.ObjectMeta{Name: opts.SubstrateName},
			Spec:       fluxsubstratev1alpha1.FluxSubstrateConfigSpec{Namespace: opts.Namespace},
		}, &kaprov1alpha1.SubstrateObjectReference{
			APIVersion: fluxSubstrateConfigAPIVersion,
			Kind:       "FluxSubstrateConfig",
			Name:       opts.SubstrateName,
		}
}

func discoverSubstrateFileSuffix(adopt bool) string {
	if adopt {
		return "-adopt"
	}
	return "-observe"
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

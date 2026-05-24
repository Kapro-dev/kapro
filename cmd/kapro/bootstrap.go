package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Choose and generate the first Kapro adoption path",
		Long: `Bootstrap is the guided entrypoint for adopting Kapro.

Use greenfield when Kapro should create a new promotion repository shape.
Use adopt when Argo CD or Flux already owns delivery and Kapro should start in
observe-first mode with reviewable mappings.`,
	}
	cmd.AddCommand(newBootstrapGuideCmd())
	cmd.AddCommand(newBootstrapGreenfieldCmd())
	cmd.AddCommand(newBootstrapBrownfieldCmd())
	cmd.AddCommand(newBootstrapBackendCmd("argo"))
	cmd.AddCommand(newBootstrapBackendCmd("flux"))
	cmd.AddCommand(newBootstrapBackendCmd("oci"))
	return cmd
}

func newBootstrapGuideCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "guide",
		Short: "Print the recommended adoption decision tree",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			printBootstrapGuide(os.Stdout)
			return nil
		},
	}
}

func printBootstrapGuide(out io.Writer) {
	fmt.Fprintln(out, `Kapro adoption paths:

1. Try Kapro in a new Flux pull-mode repo
   kapro quickstart flux ./promotion-repo --name checkout

2. Try Kapro in a new Argo CD repo
   kapro quickstart argo ./promotion-repo --name checkout

3. Existing Argo CD repository
   kapro adopt argo . --out ./kapro-connect --name checkout

4. Existing Flux repository
   kapro adopt flux . --out ./kapro-connect --name checkout

5. Outbound-only clusters without Flux or Argo CD
   kapro quickstart oci ./promotion-repo --name checkout

Safe default:
  existing GitOps adoption starts in Observe mode. Review generated Backend,
  Source, and discovery reports before switching any Backend to Adopt.

Delivery modes in plain language:
  pull: each cluster pulls desired state from inside its own network boundary.
  push: the hub tells a backend such as Argo CD what version to promote.`)
}

func newBootstrapBackendCmd(backend string) *cobra.Command {
	var opts scaffoldOptions
	defaultMode := "pull"
	if backend == "argo" {
		defaultMode = "push"
	}
	existingHint := "For an existing GitOps repository, use:\n  kapro adopt " + backend + " . --out ./kapro-connect --name checkout"
	if backend == "oci" {
		existingHint = "OCI is for spoke-side pull delivery. Use this when you do not want Argo CD or Flux on spokes."
	}
	cmd := &cobra.Command{
		Use:   backend + " [directory]",
		Short: fmt.Sprintf("Generate a new %s-backed promotion repo", backend),
		Long: fmt.Sprintf(`Generate a new Kapro promotion repository for %s.

This is a shorter, adoption-friendly alias for:
  kapro bootstrap greenfield [directory] --backend %s

Use this when you are starting fresh. %s`, backend, backend, existingHint),
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Path = "."
			if len(args) > 0 {
				opts.Path = args[0]
			}
			opts.Backend = backend
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Mode, "mode", defaultMode, "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace (default: argocd for argo, flux-system for flux, kapro-system for oci)")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none for repo-only setup")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newBootstrapGreenfieldCmd() *cobra.Command {
	var opts scaffoldOptions
	cmd := &cobra.Command{
		Use:   "greenfield [directory]",
		Short: "Generate a new promotion lifecycle repo",
		Long: `Generate Backend, Fleet, Plan, Promotion, and backend-native starter
files for a new promotion lifecycle repository.

This is a friendly wrapper around kapro init. It defaults to Flux pull mode
because that is the safest first path for platform teams that want spokes to
pull from inside their network boundary.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Path = "."
			if len(args) > 0 {
				opts.Path = args[0]
			}
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Backend, "backend", "flux", "Delivery backend: argo, flux, or oci")
	cmd.Flags().StringVar(&opts.Mode, "mode", "pull", "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace (default: argocd for argo, flux-system for flux, kapro-system for oci)")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none for repo-only setup")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newBootstrapBrownfieldCmd() *cobra.Command {
	var opts bootstrapBrownfieldOptions
	cmd := &cobra.Command{
		Use:   "brownfield <argo|flux> [repo]",
		Short: "Generate observe-first mappings for an existing GitOps repo",
		Long: `Generate observe-first Backend, Source, and discovery review files
for an existing Argo CD or Flux repository.

The command does not mutate live backend objects and does not push Git changes.
Review the generated files before granting Adopt permissions or running
kapro source apply.`,
		Deprecated: "use 'kapro adopt argo' or 'kapro adopt flux' instead",
		Args:       cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Backend = strings.ToLower(args[0])
			opts.RepoPath = "."
			if len(args) > 1 {
				opts.RepoPath = args[1]
			}
			return runBootstrapBrownfield(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Backend and Source name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace (default: argocd for argo, flux-system for flux)")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote Argo repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache when supported")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum Source units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

type bootstrapBrownfieldOptions struct {
	Backend      string
	RepoPath     string
	OutPath      string
	Name         string
	Namespace    string
	Selector     string
	Revision     string
	PathPrefixes []string
	ScanAll      bool
	Cache        bool
	MaxFiles     int
	MaxUnits     int
	Force        bool
}

func runBootstrapBrownfield(opts bootstrapBrownfieldOptions) error {
	switch opts.Backend {
	case "argo":
		namespace := opts.Namespace
		if namespace == "" {
			namespace = "argocd"
		}
		return runArgoDiscover(argoDiscoverOptions{
			RepoPath:     opts.RepoPath,
			OutPath:      opts.OutPath,
			Name:         opts.Name,
			Namespace:    namespace,
			Selector:     opts.Selector,
			Revision:     opts.Revision,
			PathPrefixes: opts.PathPrefixes,
			ScanAll:      opts.ScanAll,
			Cache:        opts.Cache,
			MaxFiles:     opts.MaxFiles,
			MaxUnits:     opts.MaxUnits,
			Force:        opts.Force,
		})
	case "flux":
		namespace := opts.Namespace
		if namespace == "" {
			namespace = "flux-system"
		}
		return runFluxDiscover(fluxDiscoverOptions{
			RepoPath:     opts.RepoPath,
			OutPath:      opts.OutPath,
			Name:         opts.Name,
			Namespace:    namespace,
			Selector:     opts.Selector,
			PathPrefixes: opts.PathPrefixes,
			ScanAll:      opts.ScanAll,
			MaxFiles:     opts.MaxFiles,
			MaxUnits:     opts.MaxUnits,
			Force:        opts.Force,
		})
	default:
		return fmt.Errorf("backend must be argo or flux")
	}
}

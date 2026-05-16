package main

import "github.com/spf13/cobra"

func newAdoptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Generate brownfield adoption mappings",
		Long: `Adoption commands generate observe-first Kapro mappings from
existing backend-native GitOps repositories. They do not mutate live backend
objects; switching a BackendProfile to Adopt and applying Git changes are
separate, explicit steps.`,
	}
	cmd.AddCommand(newAdoptArgoCmd())
	cmd.AddCommand(newAdoptFluxCmd())
	return cmd
}

func newAdoptArgoCmd() *cobra.Command {
	opts := argoDiscoverOptions{Cache: true, MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	cmd := &cobra.Command{
		Use:   "argo [repo]",
		Short: "Generate Kapro adoption files for an existing Argo CD repo",
		Long: `Scans an existing Argo CD Git repository using git ls-files and
generates BackendProfile, PromotionSource, and reviewable Git adoption mapping
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
	cmd.Flags().StringVar(&opts.Name, "name", "argo", "BackendProfile and PromotionSource name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "argocd", "Argo CD namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringVar(&opts.Revision, "revision", "", "Git branch/tag/SHA when discovering a remote repository URL")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: argocd, apps, clusters, environments, flux)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().BoolVar(&opts.Cache, "cache", true, "Reuse discovery cache for unchanged Git blobs")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum promotion units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newAdoptFluxCmd() *cobra.Command {
	opts := fluxDiscoverOptions{MaxFiles: defaultArgoDiscoveryMaxFiles, MaxUnits: defaultArgoDiscoveryMaxUnits}
	cmd := &cobra.Command{
		Use:   "flux [repo]",
		Short: "Generate Kapro adoption files for an existing Flux repo",
		Long: `Scans an existing Flux Git repository using git ls-files and
generates BackendProfile, PromotionSource, and reviewable Git adoption mapping
files. Output starts in observe mode so the generated graph can be reviewed
before any write permissions are granted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.RepoPath = "."
			if len(args) > 0 {
				opts.RepoPath = args[0]
			}
			return runFluxDiscover(opts)
		},
	}
	cmd.Flags().StringVar(&opts.OutPath, "out", "kapro-connect", "Output directory for generated Kapro files")
	cmd.Flags().StringVar(&opts.Name, "name", "flux", "BackendProfile and PromotionSource name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "flux-system", "Flux namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().StringSliceVar(&opts.PathPrefixes, "path-prefix", nil, "Repo path prefix to scan (repeatable; default: common Flux/GitOps paths)")
	cmd.Flags().BoolVar(&opts.ScanAll, "scan-all", false, "Scan all tracked YAML/JSON files instead of GitOps path prefixes")
	cmd.Flags().IntVar(&opts.MaxFiles, "max-files", defaultArgoDiscoveryMaxFiles, "Maximum tracked YAML/JSON candidate files to parse (0 = unlimited)")
	cmd.Flags().IntVar(&opts.MaxUnits, "max-units", defaultArgoDiscoveryMaxUnits, "Maximum promotion units to generate (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

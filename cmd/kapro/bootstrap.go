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
Use import when Argo CD or Flux already owns delivery and Kapro should start in
observe-first mode with reviewable mappings.`,
	}
	cmd.AddCommand(newBootstrapGuideCmd())
	cmd.AddCommand(newBootstrapGenerateCmd())
	cmd.AddCommand(newBootstrapGreenfieldCmd())
	cmd.AddCommand(newBootstrapSubstrateCmd("direct"))
	cmd.AddCommand(newBootstrapSubstrateCmd("argo"))
	cmd.AddCommand(newBootstrapSubstrateCmd("flux"))
	cmd.AddCommand(newBootstrapSubstrateCmd("oci"))
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

func newBootstrapGenerateCmd() *cobra.Command {
	var opts scaffoldOptions
	profile := "direct"
	cmd := &cobra.Command{
		Use:   "generate [directory]",
		Short: "Generate a 0.6 profile repo",
		Long: `Generate a Kapro public-preview profile repository.

Profiles:
  direct  Kubernetes direct apply with raw YAML and no OCI registry requirement
  argo    Argo CD remains the reconciler; Kapro promotes Argo-managed intent
  flux    Flux remains the reconciler; Kapro promotes Flux-managed intent
  oci     Spokes pull OCI artifacts directly without Argo CD or Flux`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Path = "."
			if len(args) > 0 {
				opts.Path = args[0]
			}
			if err := applyBootstrapGenerateProfile(&opts, profile); err != nil {
				return err
			}
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&profile, "profile", profile, "Bootstrap profile: direct, argo, flux, or oci")
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Mode, "mode", "", "Delivery mode: push or pull (defaults per profile)")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for GitOps bundle examples")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Workload/substrate namespace")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none for repo-only setup")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func applyBootstrapGenerateProfile(opts *scaffoldOptions, profile string) error {
	normalized := strings.ToLower(strings.TrimSpace(profile))
	switch normalized {
	case "direct":
		opts.Profile = "direct"
		opts.Substrate = "direct"
		if opts.Mode == "" {
			opts.Mode = "push"
		}
		if opts.Namespace == "" {
			opts.Namespace = "default"
		}
	case "argocd", "argo":
		opts.Profile = "argo"
		opts.Substrate = "argo"
		if opts.Mode == "" {
			opts.Mode = "push"
		}
		if opts.Namespace == "" {
			opts.Namespace = "argocd"
		}
	case "flux":
		opts.Profile = "flux"
		opts.Substrate = "flux"
		if opts.Mode == "" {
			opts.Mode = "pull"
		}
		if opts.Namespace == "" {
			opts.Namespace = "flux-system"
		}
	case "oci":
		opts.Profile = "oci"
		opts.Substrate = "oci"
		if opts.Mode == "" {
			opts.Mode = "pull"
		}
		if opts.Namespace == "" {
			opts.Namespace = "kapro-system"
		}
	default:
		return fmt.Errorf("--profile must be direct, argo, flux, or oci")
	}
	if opts.Name == "" {
		opts.Name = "checkout"
	}
	if opts.Registry == "" {
		opts.Registry = "oci://registry.example.com/platform"
	}
	if opts.Clusters == "" {
		opts.Clusters = "canary-eu:canary,prod-eu:production"
	}
	if opts.Team == "" {
		opts.Team = "platform"
	}
	opts.UseSubstrateClass = true
	return nil
}

func printBootstrapGuide(out io.Writer) {
	fmt.Fprintln(out, `Kapro adoption paths:

1. Try Kapro with direct Kubernetes apply
   kapro create direct ./promotion-repo --name checkout

2. Try Kapro in a new Flux pull-mode repo
   kapro create flux ./promotion-repo --name checkout

3. Try Kapro in a new Argo CD repo
   kapro create argo ./promotion-repo --name checkout

4. Existing Argo CD repository
   kapro import argo . --out ./kapro-connect --name checkout

5. Existing Flux repository
   kapro import flux . --out ./kapro-connect --name checkout

6. Outbound-only clusters that must pull OCI artifacts
   kapro create oci ./promotion-repo --name checkout

Lower-level generator:
  kapro bootstrap generate ./promotion-repo --profile direct|argo|flux|oci --name checkout

Safe default:
  existing GitOps adoption starts in Observe mode. Review generated Substrate,
  Source, and discovery reports before switching any Substrate to Adopt.

Delivery modes in plain language:
  pull: each cluster pulls desired state from inside its own network boundary.
  push: the hub tells a substrate such as Argo CD what version to promote.`)
}

func newBootstrapSubstrateCmd(substrate string) *cobra.Command {
	var opts scaffoldOptions
	defaultMode := "pull"
	if substrate == "argo" || substrate == "direct" {
		defaultMode = "push"
	}
	existingHint := "For an existing GitOps repository, use:\n  kapro import " + substrate + " . --out ./kapro-connect --name checkout"
	if substrate == "direct" {
		existingHint = "Direct is the smallest greenfield path. It does not require Argo CD, Flux, or an OCI registry."
	}
	if substrate == "oci" {
		existingHint = "OCI is the spoke-side pull helper. Use it when you do not want Argo CD or Flux on spokes."
	}
	cmd := &cobra.Command{
		Use:   substrate + " [directory]",
		Short: fmt.Sprintf("Generate a new %s-backed promotion repo", substrate),
		Long: fmt.Sprintf(`Generate a new Kapro promotion repository for %s.

This is a shorter, adoption-friendly alias for:
  kapro bootstrap generate [directory] --profile %s

The 0.6 public-preview profile matrix is exposed through:
  kapro bootstrap generate [directory] --profile direct|argo|flux|oci

Use this command when you are starting fresh with an existing substrate-specific
helper. %s`, substrate, substrate, existingHint),
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Path = "."
			if len(args) > 0 {
				opts.Path = args[0]
			}
			opts.Substrate = substrate
			opts.Profile = substrate
			opts.UseSubstrateClass = true
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Mode, "mode", defaultMode, "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Substrate namespace (default: argocd for argo, flux-system for flux, kapro-system for oci)")
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
		Long: `Generate Substrate, Fleet, Plan, Promotion, and substrate-native starter
files for a new promotion lifecycle repository.

This is a friendly wrapper around kapro init. It defaults to direct push mode
because that is the smallest first path and does not require Flux, Argo CD, or
an OCI registry.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Path = "."
			if len(args) > 0 {
				opts.Path = args[0]
			}
			opts.UseSubstrateClass = true
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Substrate, "substrate", "direct", "Substrate profile: direct, argo, flux, or oci")
	cmd.Flags().StringVar(&opts.Mode, "mode", "push", "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Substrate namespace (default: argocd for argo, flux-system for flux, kapro-system for oci)")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none for repo-only setup")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

type bootstrapExistingGitOpsOptions struct {
	Substrate    string
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

func runBootstrapExistingGitOps(opts bootstrapExistingGitOpsOptions) error {
	switch opts.Substrate {
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
		return fmt.Errorf("substrate must be argo or flux")
	}
}

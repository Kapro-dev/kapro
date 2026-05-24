package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/cli"
)

func newQuickstartCmd() *cobra.Command {
	var opts scaffoldOptions
	cmd := &cobra.Command{
		Use:   "quickstart [flux|argo|oci|demo] [directory]",
		Short: "Start the fastest Kapro path for a new user",
		Long: `Create a runnable starter repo or local demo with adoption-first defaults.

Use Flux when you want clusters to pull desired state from inside their own
network boundary. Use Argo when Argo CD owns Applications and the hub promotes
versions by updating Argo-managed intent. Use OCI when spokes should pull OCI
artifacts without Flux or Argo CD.

Examples:
  kapro quickstart
  kapro quickstart flux ./promotion-repo --name checkout
  kapro quickstart argo ./promotion-repo --name checkout
  kapro quickstart oci ./promotion-repo --name checkout
  kapro quickstart demo`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			backend := "flux"
			dir := "./kapro-quickstart"
			if len(args) > 0 {
				backend = strings.ToLower(args[0])
			}
			if backend == "demo" {
				return runDemo(cmd.Context())
			}
			if len(args) > 1 {
				dir = args[1]
			}
			opts.Path = dir
			opts.Backend = backend
			if opts.Mode == "" {
				opts.Mode = quickstartDefaultMode(backend)
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
			cli.Header("Kapro quickstart")
			cli.Info(quickstartBackendSummary(backend, opts.Mode))
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Mode, "mode", "", "Delivery mode: push or pull (defaults per backend)")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func quickstartDefaultMode(backend string) string {
	if backend == "argo" {
		return "push"
	}
	return "pull"
}

func quickstartBackendSummary(backend, mode string) string {
	switch backend {
	case "argo":
		return "Argo CD remains the application reconciler; Kapro promotes versions and records the decision path."
	case "flux":
		return "Flux remains the cluster reconciler; Kapro coordinates promotion waves and gates."
	case "oci":
		return "Kapro spokes pull OCI artifacts directly; no Argo CD or Flux installation is required on spokes."
	default:
		return fmt.Sprintf("%s backend using %s delivery", backend, mode)
	}
}

type sampleLayout struct {
	Description string
	Options     scaffoldOptions
}

func newSampleCmd() *cobra.Command {
	var (
		backendOverride string
		modeOverride    string
		force           bool
	)
	cmd := &cobra.Command{
		Use:   "sample <layout> [directory]",
		Short: "Generate opinionated Kapro sample repos",
		Long: `Generate a complete sample repo for common adoption layouts.

Layouts:
  single-cluster   one target cluster for a minimal proof of concept
  dev-stage-prod   three-stage promotion path for normal platform teams
  multi-region     canary plus production regions
  argo-app-of-apps Argo CD-shaped repo for app-of-apps teams
  flux-monorepo    Flux-shaped monorepo sample for Kustomizations or HelmReleases`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			layout, ok := sampleLayouts()[args[0]]
			if !ok {
				return fmt.Errorf("unknown sample layout %q (available: %s)", args[0], strings.Join(sampleLayoutNames(), ", "))
			}
			opts := layout.Options
			if backendOverride != "" {
				opts.Backend = strings.ToLower(backendOverride)
			}
			if modeOverride != "" {
				opts.Mode = strings.ToLower(modeOverride)
			}
			if len(args) > 1 {
				opts.Path = args[1]
			}
			opts.Force = force
			cli.Header("Kapro sample: " + args[0])
			cli.Info(layout.Description)
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&backendOverride, "backend", "", "Override backend: argo, flux, or oci")
	cmd.Flags().StringVar(&modeOverride, "mode", "", "Override delivery mode: push or pull")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing generated files")
	return cmd
}

func sampleLayouts() map[string]sampleLayout {
	return map[string]sampleLayout{
		"single-cluster": {
			Description: "Minimal one-cluster path for learning the object model.",
			Options:     sampleOptions("./kapro-sample-single", "checkout", "flux", "pull", "dev:canary"),
		},
		"dev-stage-prod": {
			Description: "Classic dev to staging to production shape using two Kapro stages.",
			Options:     sampleOptions("./kapro-sample-dev-stage-prod", "checkout", "flux", "pull", "dev:canary,stage:canary,prod:production"),
		},
		"multi-region": {
			Description: "Canary plus two production regions for fleet rollout practice.",
			Options:     sampleOptions("./kapro-sample-multi-region", "checkout", "flux", "pull", "canary-eu:canary,prod-eu-west:production,prod-us-east:production"),
		},
		"argo-app-of-apps": {
			Description: "Argo CD-oriented sample for teams that already organize delivery around Applications.",
			Options:     sampleOptions("./kapro-sample-argo", "checkout", "argo", "push", "canary-eu:canary,prod-eu:production"),
		},
		"flux-monorepo": {
			Description: "Flux-oriented monorepo sample for platform teams promoting through Kustomizations or HelmReleases.",
			Options:     sampleOptions("./kapro-sample-flux", "checkout", "flux", "pull", "canary-eu:canary,prod-eu:production"),
		},
	}
}

func sampleOptions(path, name, backend, mode, clusters string) scaffoldOptions {
	return scaffoldOptions{
		Path:     path,
		Name:     name,
		Backend:  backend,
		Mode:     mode,
		Registry: "oci://registry.example.com/platform",
		Clusters: clusters,
		Team:     "platform",
	}
}

func sampleLayoutNames() []string {
	names := make([]string, 0, len(sampleLayouts()))
	for name := range sampleLayouts() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newExplainCmd() *cobra.Command {
	cmd := newWhyCmd()
	cmd.Use = "explain <promotionrun>"
	cmd.Short = "Human-readable alias for `kapro why`"
	cmd.Long = `Explain why a promotion is waiting, blocked, failed, skipped, or complete.

This is an adoption-friendly alias for kapro why. It reads DecisionTrace
records and summarizes the gates, stages, targets, and delivery evidence that
explain the current promotion state.`
	return cmd
}

func printAdoptionFooter(path string) {
	fmt.Fprintf(os.Stderr, "\nAdoption tip: run `kapro doctor` after installing the chart, then `kubectl apply --recursive -f %s`.\n", path)
}

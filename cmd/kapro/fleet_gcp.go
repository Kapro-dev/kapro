package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	kaproconfig "kapro.io/kapro/internal/config"
	"kapro.io/kapro/internal/gcputil"
)

func newFleetMgmtCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Fleet membership management",
		Long: `Manage GKE Fleet memberships — the clusters Kapro delivers to.

If a cluster isn't in Fleet, Kapro doesn't know about it.

Examples:
  kapro fleet list
  kapro fleet register my-cluster`,
	}
	cmd.AddCommand(newFleetListMgmtCmd())
	cmd.AddCommand(newFleetSyncCmd())
	return cmd
}

func newFleetSyncCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Auto-discover Fleet clusters and add them as spokes",
		Long: `Reads all Fleet memberships and creates MemberCluster CRDs +
kubeconfig Secrets for each. Installs Flux on spokes that don't have it.

This is the bulk-onboarding command for existing Fleet clusters.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClusterSync(cmd.Context(), project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project (reads from config if omitted)")
	return cmd
}

// --- fleet list ---

func newFleetListMgmtCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Fleet memberships",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetList(cmd.Context(), project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project (reads from config if omitted)")
	return cmd
}

func runFleetList(ctx context.Context, project string) error {
	if project == "" {
		cfg, _ := kaproconfig.Load()
		project = cfg.Hub.Project
	}
	if project == "" {
		var err error
		project, err = gcputil.SelectProject(ctx)
		if err != nil {
			return err
		}
	}

	members, err := gcputil.ListFleetMembers(ctx, project)
	if err != nil {
		return err
	}

	if len(members) == 0 {
		fmt.Fprintf(os.Stderr, "No Fleet memberships found in project %s\n", project)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tLOCATION\tPROJECT\tCLUSTER\tLABELS")
	for _, m := range members {
		labels := formatLabels(m.Labels)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.Name, m.Location, m.Project, m.Cluster, labels)
	}
	return w.Flush()
}


func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/bootstrap"
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
	cmd.AddCommand(newFleetRegisterCmd())
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

// --- fleet register ---

func newFleetRegisterCmd() *cobra.Command {
	var (
		project  string
		location string
	)
	cmd := &cobra.Command{
		Use:   "register <cluster-name>",
		Short: "Register a GKE cluster in Fleet",
		Long: `Registers a GKE cluster as a Fleet membership.
Idempotent — skips if already registered.

If cluster name is omitted, interactively selects from available clusters.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := ""
			if len(args) > 0 {
				clusterName = args[0]
			}
			return runFleetRegister(ctx(cmd), project, clusterName, location)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project (reads from config if omitted)")
	cmd.Flags().StringVar(&location, "location", "", "GKE cluster location (auto-detected if omitted)")
	return cmd
}

func ctx(cmd *cobra.Command) context.Context {
	return cmd.Context()
}

func runFleetRegister(ctx context.Context, project, clusterName, location string) error {
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
	if clusterName == "" {
		var err error
		clusterName, location, err = gcputil.SelectCluster(ctx, project)
		if err != nil {
			return err
		}
	}
	if location == "" {
		detected, err := detectClusterLocation(ctx, project, clusterName)
		if err != nil {
			return fmt.Errorf("detect location: %w", err)
		}
		location = detected
	}

	fmt.Fprintf(os.Stderr, "Registering %s/%s/%s in Fleet...\n", project, location, clusterName)
	if err := bootstrap.RegisterFleetMembership(ctx, project, clusterName, location); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✔ Fleet membership registered: %s\n", clusterName)
	return nil
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

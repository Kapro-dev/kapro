package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/bootstrap"
	"kapro.io/kapro/internal/gcputil"
)

func newGCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gcp",
		Short: "GCP resource discovery and Fleet management",
		Long: `Manage GKE clusters and Fleet memberships via Go SDK.
No gcloud dependency — uses ADC/Workload Identity.

Examples:
  kapro gcp list --project my-project
  kapro gcp clusters --project my-project
  kapro gcp register --project my-project my-cluster`,
	}
	cmd.AddCommand(newGCPProjectsCmd())
	cmd.AddCommand(newFleetListCmd())
	cmd.AddCommand(newFleetClustersCmd())
	cmd.AddCommand(newFleetRegisterCmd())
	return cmd
}

// --- gcp projects ---

func newGCPProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List accessible GCP projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			projects, err := gcputil.ListProjects(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT_ID\tNAME")
			for _, p := range projects {
				fmt.Fprintf(w, "%s\t%s\n", p.ID, p.Name)
			}
			return w.Flush()
		},
	}
}

// --- fleet list ---

func newFleetListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Fleet memberships",
		Long: `Lists all GKE Fleet memberships in a project.
Shows membership name, location, GKE cluster, and labels.

If --project is omitted, interactively selects a project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetList(cmd.Context(), project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (interactive if omitted)")
	return cmd
}

func runFleetList(ctx context.Context, project string) error {
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

// --- fleet clusters ---

func newFleetClustersCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "clusters",
		Short: "List all GKE clusters in a project",
		Long: `Lists all GKE clusters (Fleet-enrolled or not) in a project.
Shows name, location, status, version, and node count.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFleetClusters(cmd.Context(), project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (interactive if omitted)")
	return cmd
}

func runFleetClusters(ctx context.Context, project string) error {
	if project == "" {
		var err error
		project, err = gcputil.SelectProject(ctx)
		if err != nil {
			return err
		}
	}

	clusters, err := gcputil.ListClusters(ctx, project)
	if err != nil {
		return err
	}

	if len(clusters) == 0 {
		fmt.Fprintf(os.Stderr, "No GKE clusters found in project %s\n", project)
		return nil
	}

	// Check which clusters are in Fleet.
	fleetMembers, _ := gcputil.ListFleetMembers(ctx, project)
	fleetSet := map[string]bool{}
	for _, m := range fleetMembers {
		fleetSet[m.Cluster] = true
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tLOCATION\tSTATUS\tVERSION\tNODES\tFLEET")
	for _, c := range clusters {
		fleet := "-"
		if fleetSet[c.Name] {
			fleet = "yes"
		}
		mode := ""
		if c.Autopilot {
			mode = " (AP)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d%s\t%s\n",
			c.Name, c.Location, c.Status, c.Version, c.NodeCount, mode, fleet)
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

If --project is omitted, interactively selects a project.
If the cluster name is omitted, interactively selects from available clusters.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := ""
			if len(args) > 0 {
				clusterName = args[0]
			}
			return runFleetRegister(cmd.Context(), project, clusterName, location)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID")
	cmd.Flags().StringVar(&location, "location", "", "GKE cluster location (auto-detected if omitted)")
	return cmd
}

func runFleetRegister(ctx context.Context, project, clusterName, location string) error {
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
	fmt.Fprintf(os.Stderr, "Fleet membership registered: %s\n", clusterName)
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

package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"github.com/spf13/cobra"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

func newStatusCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status [fleet-name]",
		Short: "Live fleet delivery status",
		Long: `Shows real-time delivery status for all clusters in a Fleet.

Displays per-cluster: version, delivery phase, wave progress,
convergence status, and last heartbeat. Color-coded by health.

Examples:
  kapro status                  # all fleets
  kapro status hello-spoke      # specific Fleet
  kapro status -o json          # machine-readable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fleetName := ""
			if len(args) > 0 {
				fleetName = args[0]
			}
			return runStatus(cmd.Context(), fleetName, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runStatus(ctx context.Context, fleetName, kubeconfigPath string) error {
	sp := cli.NewSpinner("Loading fleet status")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect")
		return err
	}

	// Load Fleet CRs.
	var fleets kaprov1alpha1.FleetList
	if err := c.List(ctx, &fleets); err != nil {
		sp.StopFail("Failed to list fleets")
		return err
	}

	// Load Clusters.
	var allClusters kaprov1alpha1.ClusterList
	if err := c.List(ctx, &allClusters); err != nil {
		sp.StopFail("Failed to list clusters")
		return err
	}

	// Load active PromotionRuns.
	var promotionruns kaproruntimev1alpha1.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		sp.StopFail("Failed to list promotionruns")
		return err
	}

	// Load Targets.
	var targets kaproruntimev1alpha1.TargetList
	if err := c.List(ctx, &targets); err != nil {
		sp.StopFail("Failed to list targets")
		return err
	}
	sp.Stop()

	if cli.IsJSON() {
		return cli.JSON(map[string]any{
			"fleets":        fleets.Items,
			"clusters":      allClusters.Items,
			"promotionruns": promotionruns.Items,
			"targets":       targets.Items,
		})
	}

	// Filter by fleetName if specified.
	var filteredFleets []kaprov1alpha1.Fleet
	for _, fleet := range fleets.Items {
		if fleetName == "" || fleet.Name == fleetName {
			filteredFleets = append(filteredFleets, fleet)
		}
	}

	if len(filteredFleets) == 0 {
		if fleetName != "" {
			cli.Warn(fmt.Sprintf("Fleet %q not found", fleetName))
		} else {
			cli.Muted("No fleets found")
		}
		return nil
	}

	for _, fleet := range filteredFleets {
		renderFleetStatus(fleet, allClusters.Items, promotionruns.Items, targets.Items)
	}

	return nil
}

func renderFleetStatus(fleet kaprov1alpha1.Fleet, allClusters []kaprov1alpha1.Cluster, promotionruns []kaproruntimev1alpha1.PromotionRun, targets []kaproruntimev1alpha1.Target) {
	cli.Header(fmt.Sprintf("fleet/%s", fleet.Name))

	// Summary line.
	mode := string(fleet.Spec.Delivery.Mode)
	if mode == "" {
		mode = "pull"
	}
	cli.KV("Source", fleet.Spec.SourceRef)
	cli.KV("Mode", mode)
	cli.KV("Substrate", fleet.Spec.Delivery.SubstrateRef)
	cli.KV("Version", fleet.Status.Version)
	cli.KV("Clusters", fmt.Sprintf("%d total, %d converged",
		fleet.Status.ClusterCount, fleet.Status.ConvergedCount))

	// Active promotionrun.
	activePromotionRun := findActivePromotionRun(promotionruns)
	if activePromotionRun != nil {
		cli.KV("PromotionRun", fmt.Sprintf("%s → %s (%s)",
			activePromotionRun.Name, activePromotionRun.Spec.Version,
			cli.Theme.PhaseProgressing.Render(string(activePromotionRun.Status.Phase))))
	}

	// Cluster table.
	fmt.Fprintln(cli.Out)
	tbl := cli.NewTable("CLUSTER", "VERSION", "PHASE", "HEALTH", "SUBSTRATE", "HEARTBEAT")

	// Collect clusters that belong to this Fleet.
	fleetClusters := map[string]bool{}
	for _, cluster := range fleet.Spec.Clusters {
		fleetClusters[cluster.Name] = true
	}

	var rows []clusterRow
	for _, mc := range allClusters {
		if !fleetClusters[mc.Name] {
			continue
		}
		rows = append(rows, clusterRow{
			name:      mc.Name,
			version:   mc.Status.Version,
			phase:     string(mc.Status.Phase),
			healthy:   mc.Status.Health.AllWorkloadsReady,
			substrate: mc.Spec.Delivery.RegistryKey(),
			heartbeat: mc.Status.LastHeartbeat,
			ready:     mc.Status.Health.ReadyWorkloads,
			total:     mc.Status.Health.TotalWorkloads,
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	for _, r := range rows {
		version := r.version
		if version == "" {
			version = "-"
		}

		phase := colorPhase(r.phase)
		health := colorHealth(r.healthy, r.ready, r.total)

		heartbeat := "-"
		if r.heartbeat != "" {
			if t, err := time.Parse(time.RFC3339, r.heartbeat); err == nil {
				heartbeat = cli.Age(t) + " ago"
			}
		}

		tbl.AddRow(r.name, version, phase, health, r.substrate, heartbeat)
	}
	tbl.Render()

	// Pending approvals for this Fleet.
	pendingApprovals := 0
	for _, t := range targets {
		if t.Status.Phase == kaprov1alpha1.TargetPhaseWaitingApproval {
			pendingApprovals++
		}
	}
	if pendingApprovals > 0 {
		fmt.Fprintln(cli.Out)
		cli.Warn(fmt.Sprintf("%d targets waiting for approval — run: kapro approve <promotionrun>/<target>", pendingApprovals))
	}
}

type clusterRow struct {
	name      string
	version   string
	phase     string
	healthy   bool
	substrate string
	heartbeat string
	ready     int
	total     int
}

func colorPhase(phase string) string {
	switch kaprov1alpha1.ClusterPhase(phase) {
	case kaprov1alpha1.ClusterPhaseConverged:
		return cli.Theme.PhaseComplete.Render("✔ Converged")
	case kaprov1alpha1.ClusterPhaseConverging:
		return cli.Theme.PhaseProgressing.Render("⠿ Converging")
	case kaprov1alpha1.ClusterPhaseFailed:
		return cli.Theme.PhaseFailed.Render("✗ Failed")
	default:
		if phase == "" {
			return cli.Theme.Muted.Render("— Pending")
		}
		return cli.Theme.PhasePending.Render(phase)
	}
}

func colorHealth(healthy bool, ready, total int) string {
	if healthy {
		return cli.Theme.PhaseComplete.Render(fmt.Sprintf("✔ %d/%d", ready, total))
	}
	if ready > 0 {
		return cli.Theme.PhaseWaiting.Render(fmt.Sprintf("⠿ %d/%d", ready, total))
	}
	if total > 0 {
		return cli.Theme.PhaseFailed.Render(fmt.Sprintf("✗ 0/%d", total))
	}
	return cli.Theme.Muted.Render("—")
}

func findActivePromotionRun(promotionruns []kaproruntimev1alpha1.PromotionRun) *kaproruntimev1alpha1.PromotionRun {
	for i := range promotionruns {
		if promotionruns[i].Status.Phase == kaprov1alpha1.PromotionRunPhaseProgressing {
			return &promotionruns[i]
		}
	}
	return nil
}

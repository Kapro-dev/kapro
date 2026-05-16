package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

func newStatusCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status [kapro-name]",
		Short: "Live fleet delivery status",
		Long: `Shows real-time delivery status for all clusters in a Kapro fleet.

Displays per-cluster: version, delivery phase, wave progress,
convergence status, and last heartbeat. Color-coded by health.

Examples:
  kapro status                  # all Kapro instances
  kapro status hello-spoke      # specific Kapro
  kapro status -o json          # machine-readable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			kaproName := ""
			if len(args) > 0 {
				kaproName = args[0]
			}
			return runStatus(cmd.Context(), kaproName, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runStatus(ctx context.Context, kaproName, kubeconfigPath string) error {
	sp := cli.NewSpinner("Loading fleet status")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect")
		return err
	}

	// Load Kapro CRs.
	var kapros kaprov1alpha1.KaproList
	if err := c.List(ctx, &kapros); err != nil {
		sp.StopFail("Failed to list Kapro instances")
		return err
	}

	// Load FleetClusters.
	var allClusters kaprov1alpha1.FleetClusterList
	if err := c.List(ctx, &allClusters); err != nil {
		sp.StopFail("Failed to list clusters")
		return err
	}

	// Load active PromotionRuns.
	var promotionruns kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		sp.StopFail("Failed to list promotionruns")
		return err
	}

	// Load PromotionTargets.
	var targets kaprov1alpha1.PromotionTargetList
	if err := c.List(ctx, &targets); err != nil {
		sp.StopFail("Failed to list targets")
		return err
	}
	sp.Stop()

	if cli.IsJSON() {
		return cli.JSON(map[string]any{
			"kapros":        kapros.Items,
			"clusters":      allClusters.Items,
			"promotionruns": promotionruns.Items,
			"targets":       targets.Items,
		})
	}

	// Filter by kaproName if specified.
	var filteredKapros []kaprov1alpha1.Kapro
	for _, k := range kapros.Items {
		if kaproName == "" || k.Name == kaproName {
			filteredKapros = append(filteredKapros, k)
		}
	}

	if len(filteredKapros) == 0 {
		if kaproName != "" {
			cli.Warn(fmt.Sprintf("Kapro %q not found", kaproName))
		} else {
			cli.Muted("No Kapro instances found")
		}
		return nil
	}

	for _, kapro := range filteredKapros {
		renderKaproStatus(kapro, allClusters.Items, promotionruns.Items, targets.Items)
	}

	return nil
}

func renderKaproStatus(kapro kaprov1alpha1.Kapro, allClusters []kaprov1alpha1.FleetCluster, promotionruns []kaprov1alpha1.PromotionRun, targets []kaprov1alpha1.PromotionTarget) {
	cli.Header(fmt.Sprintf("kapro/%s", kapro.Name))

	// Summary line.
	mode := string(kapro.Spec.Delivery.Mode)
	if mode == "" {
		mode = "pull"
	}
	cli.KV("Source", kapro.Spec.SourceRef)
	cli.KV("Mode", mode)
	cli.KV("Backend", kapro.Spec.Delivery.BackendRef)
	cli.KV("Version", kapro.Status.Version)
	cli.KV("Clusters", fmt.Sprintf("%d total, %d converged",
		kapro.Status.ClusterCount, kapro.Status.ConvergedCount))

	// Active promotionrun.
	activePromotionRun := findActivePromotionRun(promotionruns)
	if activePromotionRun != nil {
		cli.KV("PromotionRun", fmt.Sprintf("%s → %s (%s)",
			activePromotionRun.Name, activePromotionRun.Spec.Version,
			cli.Theme.PhaseProgressing.Render(string(activePromotionRun.Status.Phase))))
	}

	// Cluster table.
	fmt.Fprintln(cli.Out)
	tbl := cli.NewTable("CLUSTER", "VERSION", "PHASE", "HEALTH", "BACKEND", "HEARTBEAT")

	// Collect clusters that belong to this Kapro.
	kaproClusters := map[string]bool{}
	for _, cluster := range kapro.Spec.Clusters {
		kaproClusters[cluster.Name] = true
	}

	var rows []clusterRow
	for _, mc := range allClusters {
		if !kaproClusters[mc.Name] {
			continue
		}
		rows = append(rows, clusterRow{
			name:      mc.Name,
			version:   mc.Status.Version,
			phase:     string(mc.Status.Phase),
			healthy:   mc.Status.Health.AllWorkloadsReady,
			backend:   mc.Spec.Delivery.RegistryKey(),
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

		tbl.AddRow(r.name, version, phase, health, r.backend, heartbeat)
	}
	tbl.Render()

	// Pending approvals for this Kapro.
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
	backend   string
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

func findActivePromotionRun(promotionruns []kaprov1alpha1.PromotionRun) *kaprov1alpha1.PromotionRun {
	for i := range promotionruns {
		if promotionruns[i].Status.Phase == kaprov1alpha1.PromotionRunPhaseProgressing {
			return &promotionruns[i]
		}
	}
	return nil
}

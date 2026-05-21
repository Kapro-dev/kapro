package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

func newGetPromotionCmd() *cobra.Command {
	var (
		kubeconfig  string
		eventsLimit int
	)
	cmd := &cobra.Command{
		Use:   "promotion <name>",
		Short: "Show a Promotion summary",
		Long: `Show a Promotion summary with active attempt progress, lifecycle handler
results, and recent events.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetPromotion(cmd.Context(), args[0], kubeconfig, eventsLimit)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().IntVar(&eventsLimit, "events", 5, "Max recent Events to show")
	return cmd
}

func runGetPromotion(ctx context.Context, name, kubeconfigPath string, eventsLimit int) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return runGetPromotionWithClient(ctx, c, name, eventsLimit)
}

func runGetPromotionWithClient(ctx context.Context, c client.Client, name string, eventsLimit int) error {
	diag, err := collectDiag(ctx, c, name, eventsLimit)
	if err != nil {
		return err
	}
	if cli.IsJSON() {
		return cli.JSON(diag)
	}
	renderPromotionSummary(diag)
	return nil
}

func renderPromotionSummary(diag *promotionDiag) {
	p := diag.Promotion
	cli.Header(fmt.Sprintf("promotion/%s", p.Name))
	cli.KV("Phase", styledPromotionPhase(p.Status.Phase))
	cli.KV("Fleet", stringOrUnset(p.Spec.FleetRef))
	cli.KV("Version", stringOrUnset(promotionDisplayVersion(p)))
	if p.Status.ActiveAttemptRef != nil {
		cli.KV("Active Attempt", p.Status.ActiveAttemptRef.Name)
	} else {
		cli.KV("Active Attempt", "(none)")
	}
	cli.KV("Attempts", fmt.Sprintf("%d", len(p.Status.Attempts)))
	cli.KV("Age", cli.Age(p.CreationTimestamp.Time))

	if activeRun := activePromotionRun(diag); activeRun != nil && activeRun.Status.Summary != nil {
		s := activeRun.Status.Summary
		cli.Header("Active attempt progress")
		tbl := cli.NewTable("RUN", "PHASE", "SYNCED", "FAILED", "PENDING", "TOTAL")
		tbl.AddRow(activeRun.Name, string(activeRun.Status.Phase),
			fmt.Sprintf("%d", s.SyncedTargets),
			fmt.Sprintf("%d", s.FailedTargets),
			fmt.Sprintf("%d", s.PendingTargets),
			fmt.Sprintf("%d", s.TotalTargets),
		)
		tbl.Render()
	}

	renderLifecycleHandlerResults(p.Status.LifecycleHandlerResults)
	renderPromotionSummaryTargets(diag)
	renderEvents(diag.Events)
}

func renderPromotionSummaryTargets(diag *promotionDiag) {
	if len(diag.Targets) == 0 {
		return
	}
	if diag.Promotion.Status.ActiveAttemptRef != nil {
		renderTargets(diag.Targets)
		return
	}
	cli.Header("Latest attempt targets")
	tbl := cli.NewTable("TARGET", "STAGE", "PLAN", "PHASE", "VERSION", "AGE")
	for _, t := range diag.Targets {
		tbl.AddRow(
			t.Spec.Target, t.Spec.Stage, t.Spec.Plan,
			string(t.Status.Phase), truncate(t.Spec.Version, 22),
			cli.Age(t.CreationTimestamp.Time),
		)
	}
	tbl.Render()
}

func activePromotionRun(diag *promotionDiag) *kaprov1alpha2.PromotionRun {
	if diag.Promotion.Status.ActiveAttemptRef == nil {
		return nil
	}
	name := diag.Promotion.Status.ActiveAttemptRef.Name
	for i := range diag.Runs {
		if diag.Runs[i].Name == name {
			return &diag.Runs[i]
		}
	}
	return nil
}

func renderLifecycleHandlerResults(results []kaprov1alpha2.PromotionLifecycleHandlerResult) {
	if len(results) == 0 {
		return
	}
	cli.Header("Lifecycle handlers")
	tbl := cli.NewTable("NAME", "PHASE", "KIND", "RESULT", "ATTEMPTS", "MESSAGE", "AGE")
	for _, r := range results {
		tbl.AddRow(
			r.Name,
			string(r.Phase),
			r.Kind,
			r.Result,
			fmt.Sprintf("%d", r.Attempts),
			truncate(r.Message, 50),
			cli.Age(r.FiredAt.Time),
		)
	}
	tbl.Render()
}

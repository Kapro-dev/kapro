package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/cli"
)

func newReconstructCmd() *cobra.Command {
	var kubeconfig string
	var atRaw string
	cmd := &cobra.Command{
		Use:   "reconstruct <promotionrun>",
		Short: "Reconstruct PromotionRun decisions from DecisionTrace records",
		Long: `Reconstruct the latest known controller decisions for a PromotionRun
at a point in time by replaying DecisionTrace records up to --at.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := time.Parse(time.RFC3339, atRaw)
			if err != nil {
				return fmt.Errorf("parse --at as RFC3339 timestamp: %w", err)
			}
			return runReconstruct(cmd.Context(), kubeconfig, args[0], at)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&atRaw, "at", "", "RFC3339 timestamp to reconstruct at")
	_ = cmd.MarkFlagRequired("at")
	return cmd
}

func runReconstruct(ctx context.Context, kubeconfigPath, promotionRun string, at time.Time) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return runReconstructWithClient(ctx, c, promotionRun, at)
}

func runReconstructWithClient(ctx context.Context, c client.Client, promotionRun string, at time.Time) error {
	report, err := collectReconstruction(ctx, c, promotionRun, at)
	if err != nil {
		return err
	}
	if cli.IsJSON() {
		return cli.JSON(report)
	}
	renderReconstruction(report)
	return nil
}

type reconstructReport struct {
	PromotionRun string                `json:"promotionRun"`
	At           string                `json:"at"`
	TraceCount   int                   `json:"traceCount"`
	Decisions    []reconstructDecision `json:"decisions"`
	Timeline     []whyTrace            `json:"timeline"`
}

type reconstructDecision struct {
	Scope     string `json:"scope"`
	Time      string `json:"time"`
	EventType string `json:"eventType"`
	Phase     string `json:"phase,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
	Message   string `json:"message,omitempty"`
}

func collectReconstruction(ctx context.Context, c client.Client, promotionRun string, at time.Time) (*reconstructReport, error) {
	why, err := collectWhy(ctx, c, promotionRun)
	if err != nil {
		return nil, err
	}
	timeline := make([]whyTrace, 0, len(why.Traces))
	for _, trace := range why.Traces {
		traceTime, err := time.Parse(time.RFC3339, trace.Time)
		if err != nil {
			return nil, fmt.Errorf("parse DecisionTrace %q time %q: %w", trace.Name, trace.Time, err)
		}
		if !traceTime.After(at) {
			timeline = append(timeline, trace)
		}
	}
	decisions, err := latestDecisionsByScope(timeline)
	if err != nil {
		return nil, err
	}
	return &reconstructReport{
		PromotionRun: promotionRun,
		At:           at.Format(time.RFC3339),
		TraceCount:   len(timeline),
		Decisions:    decisions,
		Timeline:     timeline,
	}, nil
}

func latestDecisionsByScope(timeline []whyTrace) ([]reconstructDecision, error) {
	latest := map[string]reconstructDecision{}
	latestAt := map[string]time.Time{}
	for _, trace := range timeline {
		scope := reconstructScope(trace)
		traceTime, err := time.Parse(time.RFC3339, trace.Time)
		if err != nil {
			return nil, fmt.Errorf("parse DecisionTrace %q time %q: %w", trace.Name, trace.Time, err)
		}
		if current, ok := latestAt[scope]; ok && !traceTime.After(current) {
			continue
		}
		latestAt[scope] = traceTime
		latest[scope] = reconstructDecision{
			Scope:     scope,
			Time:      trace.Time,
			EventType: string(trace.EventType),
			Phase:     trace.Phase,
			Reason:    trace.Reason,
			Source:    trace.Source,
			Message:   trace.Message,
		}
	}
	decisions := make([]reconstructDecision, 0, len(latest))
	for _, decision := range latest {
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].Scope != decisions[j].Scope {
			return decisions[i].Scope < decisions[j].Scope
		}
		return decisions[i].Time < decisions[j].Time
	})
	return decisions, nil
}

func reconstructScope(trace whyTrace) string {
	parts := []string{}
	if trace.Plan != "" {
		parts = append(parts, "plan="+trace.Plan)
	}
	if trace.Stage != "" {
		parts = append(parts, "stage="+trace.Stage)
	}
	if trace.Target != "" {
		parts = append(parts, "target="+trace.Target)
	}
	if len(parts) == 0 {
		parts = append(parts, "promotionrun")
	}
	if trace.EventType != "" {
		parts = append(parts, "type="+string(trace.EventType))
	}
	return strings.Join(parts, " ")
}

func renderReconstruction(report *reconstructReport) {
	cli.Header("Reconstruction " + report.PromotionRun + " @ " + report.At)
	if report.TraceCount == 0 {
		cli.Muted("No DecisionTrace records found at or before the requested time.")
		return
	}
	tbl := cli.NewTable("SCOPE", "TIME", "TYPE", "PHASE", "REASON", "SOURCE", "MESSAGE")
	for _, decision := range report.Decisions {
		tbl.AddRow(
			decision.Scope,
			decision.Time,
			decision.EventType,
			stringOrUnset(decision.Phase),
			stringOrUnset(decision.Reason),
			stringOrUnset(decision.Source),
			truncate(decision.Message, 96),
		)
	}
	tbl.Render()
}

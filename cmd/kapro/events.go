package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/cli"
)

func newEventsCmd() *cobra.Command {
	var (
		kubeconfig string
		promotion  string
		since      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent Kapro rollout events",
		Long: `Show recent Kapro-related Kubernetes Events.

When --promotion is set, events are scoped to that Promotion, its attempts,
and the active Target objects. This is also the fallback view when an operator
CloudEvents sink is not exposed to the CLI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEvents(cmd.Context(), kubeconfig, promotion, since)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&promotion, "promotion", "", "Promotion name to filter")
	cmd.Flags().DurationVar(&since, "since", 10*time.Minute, "Only show events newer than this duration")
	return cmd
}

func runEvents(ctx context.Context, kubeconfigPath, promotion string, since time.Duration) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return renderEventsWithClient(ctx, c, promotion, since)
}

func renderEventsWithClient(ctx context.Context, c client.Client, promotion string, since time.Duration) error {
	events, err := collectKaproEvents(ctx, c, promotion, since)
	if err != nil {
		return err
	}
	if cli.IsJSON() {
		return cli.JSON(events)
	}
	if len(events) == 0 {
		cli.Muted("No recent Kapro events found.")
		return nil
	}
	cli.Header("Kapro Events")
	tbl := cli.NewTable("AGE", "TYPE", "REASON", "OBJECT", "MESSAGE")
	for _, e := range events {
		tbl.AddRow(cli.Age(eventTime(e)), e.Type, e.Reason,
			e.InvolvedObject.Kind+"/"+e.InvolvedObject.Name, truncate(e.Message, 70))
	}
	tbl.Render()
	return nil
}

func collectKaproEvents(ctx context.Context, c client.Client, promotion string, since time.Duration) ([]corev1.Event, error) {
	var events []corev1.Event
	if promotion != "" {
		diag, err := collectDiag(ctx, c, promotion, 0)
		if err != nil {
			return nil, err
		}
		events = append(events, diag.Events...)
	} else {
		var list corev1.EventList
		if err := c.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		for _, e := range list.Items {
			if isKaproEvent(e) {
				events = append(events, e)
			}
		}
	}

	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	filtered := events[:0]
	for _, e := range events {
		if cutoff.IsZero() || eventTime(e).After(cutoff) || eventTime(e).Equal(cutoff) {
			filtered = append(filtered, e)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return eventTime(filtered[i]).After(eventTime(filtered[j]))
	})
	return filtered, nil
}

func isKaproEvent(e corev1.Event) bool {
	if !strings.HasPrefix(e.InvolvedObject.APIVersion, "kapro.io/") {
		return false
	}
	switch e.InvolvedObject.Kind {
	case "Promotion", "PromotionRun", "Target", "Fleet", "Plan", "Approval", "Trigger", "Source", "Cluster":
		return true
	}
	return false
}

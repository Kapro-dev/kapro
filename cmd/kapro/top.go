package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

func newTopCmd() *cobra.Command {
	var (
		kubeconfig    string
		namespace     string
		watch         bool
		watchInterval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Show Promotion rollout status at a glance",
		Long: `Show a compact Promotion table for live operations.

By default, kapro top renders once. Use --watch to refresh until Ctrl-C.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTop(cmd.Context(), kubeconfig, namespace, watch, watchInterval)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&namespace, "namespace", "all", "Namespace scope; use all for cluster-scoped Kapro APIs")
	cmd.Flags().BoolVar(&watch, "watch", false, "Refresh until interrupted")
	cmd.Flags().DurationVar(&watchInterval, "watch-interval", 2*time.Second, "Refresh interval when --watch is set")
	return cmd
}

func runTop(ctx context.Context, kubeconfigPath, namespace string, watch bool, interval time.Duration) error {
	if watch && cli.IsJSON() {
		return fmt.Errorf("--watch is not supported with -o json")
	}
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	if watch {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if interval <= 0 {
			interval = 2 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := renderTopWithClient(ctx, c, namespace); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				fmt.Fprint(cli.Out, "\033[H\033[2J")
			}
		}
	}
	return renderTopWithClient(ctx, c, namespace)
}

func renderTopWithClient(ctx context.Context, c client.Client, namespace string) error {
	if namespace != "" && namespace != "all" {
		return fmt.Errorf("promotions are cluster-scoped; --namespace only supports \"all\"")
	}
	var list kaprov1alpha2.PromotionList
	if err := c.List(ctx, &list); err != nil {
		return fmt.Errorf("list promotions: %w", err)
	}
	sortPromotions(list.Items)
	if cli.IsJSON() {
		return cli.JSON(list.Items)
	}

	runByName, err := activePromotionRuns(ctx, c, list.Items)
	if err != nil {
		return err
	}

	if len(list.Items) == 0 {
		cli.Muted("No promotions found.")
		return nil
	}

	tbl := cli.NewTable("NAME", "PHASE", "FLEET", "VERSION", "TARGETS", "AGE")
	for _, p := range list.Items {
		tbl.AddRow(
			p.Name,
			stringOrUnset(string(p.Status.Phase)),
			stringOrUnset(p.Spec.FleetRef),
			stringOrUnset(promotionDisplayVersion(&p)),
			promotionTargetsText(&p, runByName),
			cli.Age(p.CreationTimestamp.Time),
		)
	}
	tbl.Render()
	return nil
}

func activePromotionRuns(ctx context.Context, c client.Client, promotions []kaprov1alpha2.Promotion) (map[string]kaprov1alpha2.PromotionRun, error) {
	runByName := map[string]kaprov1alpha2.PromotionRun{}
	for _, p := range promotions {
		if p.Status.ActiveAttemptRef == nil || p.Status.ActiveAttemptRef.Name == "" {
			continue
		}
		name := p.Status.ActiveAttemptRef.Name
		if _, ok := runByName[name]; ok {
			continue
		}
		var run kaprov1alpha2.PromotionRun
		err := c.Get(ctx, client.ObjectKey{Name: name}, &run)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get active promotionrun %q: %w", name, err)
		}
		runByName[name] = run
	}
	return runByName, nil
}

func sortPromotions(items []kaprov1alpha2.Promotion) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreationTimestamp.Equal(&items[j].CreationTimestamp) {
			return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
		}
		return items[i].Name < items[j].Name
	})
}

func promotionTargetsText(p *kaprov1alpha2.Promotion, runByName map[string]kaprov1alpha2.PromotionRun) string {
	if p.Status.ActiveAttemptRef == nil {
		return "-"
	}
	run, ok := runByName[p.Status.ActiveAttemptRef.Name]
	if !ok || run.Status.Summary == nil {
		return "-"
	}
	return fmt.Sprintf("%d/%d", run.Status.Summary.SyncedTargets, run.Status.Summary.TotalTargets)
}

func promotionDisplayVersion(p *kaprov1alpha2.Promotion) string {
	if p.Status.ResolvedVersion != "" {
		return p.Status.ResolvedVersion
	}
	if p.Spec.Version != "" {
		return p.Spec.Version
	}
	if len(p.Spec.Versions) > 0 {
		return formatPromotionRunVersions(p.Spec.Versions)
	}
	return ""
}

func stringOrUnset(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

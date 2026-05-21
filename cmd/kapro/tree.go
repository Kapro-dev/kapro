package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

const (
	promotionLabelKey    = "kapro.io/promotion"
	promotionRunLabelKey = "kapro.io/promotionrun"
)

func newTreeCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "tree <promotion>",
		Short: "Show a Promotion, its attempts, and per-target runtime state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTree(cmd.Context(), kubeconfig, args[0])
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runTree(ctx context.Context, kubeconfigPath, promotionName string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return renderTreeWithClient(ctx, c, promotionName)
}

func renderTreeWithClient(ctx context.Context, c client.Client, promotionName string) error {
	tree, err := collectPromotionTree(ctx, c, promotionName)
	if err != nil {
		return err
	}
	if cli.IsJSON() {
		return cli.JSON(tree)
	}
	renderPromotionTree(tree)
	return nil
}

type promotionTree struct {
	Promotion kaprov1alpha2.Promotion `json:"promotion"`
	Runs      []promotionTreeRun      `json:"runs"`
}

type promotionTreeRun struct {
	Run     kaprov1alpha2.PromotionRun `json:"run"`
	Targets []kaprov1alpha2.Target     `json:"targets"`
}

func collectPromotionTree(ctx context.Context, c client.Client, promotionName string) (*promotionTree, error) {
	var promo kaprov1alpha2.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: promotionName}, &promo); err != nil {
		return nil, fmt.Errorf("get promotion %q: %w", promotionName, err)
	}

	var runs kaprov1alpha2.PromotionRunList
	if err := c.List(ctx, &runs, client.MatchingLabels{promotionLabelKey: promotionName}); err != nil {
		return nil, fmt.Errorf("list promotionruns: %w", err)
	}
	sortPromotionRuns(runs.Items)

	tree := &promotionTree{Promotion: promo}
	for _, r := range runs.Items {
		var targetList kaprov1alpha2.TargetList
		if err := c.List(ctx, &targetList, client.MatchingLabels{promotionRunLabelKey: r.Name}); err != nil {
			return nil, fmt.Errorf("list targets for promotionrun %q: %w", r.Name, err)
		}
		targets := targetList.Items
		sortTargets(targets)
		tree.Runs = append(tree.Runs, promotionTreeRun{Run: r, Targets: targets})
	}
	return tree, nil
}

func renderPromotionTree(tree *promotionTree) {
	p := tree.Promotion
	fmt.Fprintf(cli.Out, "Promotion/%s  %s  version=%s\n",
		p.Name, stringOrUnset(string(p.Status.Phase)), stringOrUnset(promotionDisplayVersion(&p)))
	for i, r := range tree.Runs {
		runPrefix := "|-"
		childPrefix := "| "
		if i == len(tree.Runs)-1 {
			runPrefix = "`-"
			childPrefix = "  "
		}
		reason := latestConditionReason(r.Run.Status.Conditions)
		fmt.Fprintf(cli.Out, "%s PromotionRun/%s  %s  targets=%s%s\n",
			runPrefix,
			r.Run.Name,
			stringOrUnset(string(r.Run.Status.Phase)),
			promotionRunTargetsText(&r.Run),
			formatReason(reason),
		)
		for j, t := range r.Targets {
			targetPrefix := "|-"
			if j == len(r.Targets)-1 {
				targetPrefix = "`-"
			}
			tReason := latestConditionReason(t.Status.Conditions)
			if tReason == "" {
				tReason = t.Status.Message
			}
			fmt.Fprintf(cli.Out, "%s%s Target/%s  %s  stage=%s%s\n",
				childPrefix,
				targetPrefix,
				t.Name,
				stringOrUnset(string(t.Status.Phase)),
				stringOrUnset(t.Spec.Stage),
				formatReason(tReason),
			)
		}
	}
}

func sortPromotionRuns(items []kaprov1alpha2.PromotionRun) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreationTimestamp.Equal(&items[j].CreationTimestamp) {
			return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
		}
		return items[i].Name < items[j].Name
	})
}

func sortTargets(items []kaprov1alpha2.Target) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Spec.Stage != items[j].Spec.Stage {
			return items[i].Spec.Stage < items[j].Spec.Stage
		}
		if items[i].Spec.Target != items[j].Spec.Target {
			return items[i].Spec.Target < items[j].Spec.Target
		}
		return items[i].Name < items[j].Name
	})
}

func promotionRunTargetsText(run *kaprov1alpha2.PromotionRun) string {
	if run.Status.Summary == nil {
		return "-"
	}
	return fmt.Sprintf("%d/%d", run.Status.Summary.SyncedTargets, run.Status.Summary.TotalTargets)
}

func latestConditionReason(conds []metav1.Condition) string {
	for i := len(conds) - 1; i >= 0; i-- {
		if strings.TrimSpace(conds[i].Reason) != "" {
			return conds[i].Reason
		}
	}
	return ""
}

func formatReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "  reason=" + truncate(reason, 48)
}

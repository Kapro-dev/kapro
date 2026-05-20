package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

// `kapro suspend <promotion>` and `kapro resume <promotion>` are
// shortcut subcommands that flip Promotion.spec.suspended. They exist
// because the `kapro diag` next-action hints currently print a kubectl
// patch — anything we tell the user to type, the CLI should also do.

func newSuspendCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "suspend <promotion>",
		Short: "Pause new PromotionRun attempts for a Promotion",
		Long: `Set Promotion.spec.suspended=true.

The controller stops stamping new PromotionRun attempts and any in-flight
PromotionRun's FSM advance is halted at its current phase. Existing
PromotionTarget objects keep their state; nothing is rolled back.

Use kapro resume to unpause.

Examples:
  kapro suspend checkout-v1.2.3
  kapro suspend checkout-v1.2.3 --kubeconfig ~/.kube/staging`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuspendResume(cmd.Context(), args[0], true, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func newResumeCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "resume <promotion>",
		Short: "Resume a suspended Promotion",
		Long: `Set Promotion.spec.suspended=false.

The controller resumes stamping new PromotionRun attempts on the next
reconcile. If the Promotion was suspended mid-rollout, the active
PromotionRun's FSM picks back up where it paused.

Examples:
  kapro resume checkout-v1.2.3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuspendResume(cmd.Context(), args[0], false, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runSuspendResume(ctx context.Context, name string, suspended bool, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return suspendResumeWithClient(ctx, c, name, suspended)
}

// suspendResumeWithClient is the testable core. Returns nil on a no-op
// (already in the desired state) but still prints a friendly message so
// the user knows the CLI ran.
func suspendResumeWithClient(ctx context.Context, c client.Client, name string, suspended bool) error {
	var promo kaprov1alpha1.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &promo); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("promotion %q not found", name)
		}
		return fmt.Errorf("get promotion: %w", err)
	}

	past := "suspended"
	if !suspended {
		past = "resumed"
	}

	if promo.Spec.Suspended == suspended {
		cli.Muted(fmt.Sprintf("Promotion %q already %s; no change.", name, past))
		return nil
	}

	patch := client.MergeFrom(promo.DeepCopy())
	promo.Spec.Suspended = suspended
	if err := c.Patch(ctx, &promo, patch); err != nil {
		return fmt.Errorf("patch promotion: %w", err)
	}

	cli.Successf("Promotion %q %s.", name, past)
	if suspended {
		cli.Muted("To unpause: kapro resume " + name)
	} else {
		cli.Muted("To pause again: kapro suspend " + name)
	}
	return nil
}

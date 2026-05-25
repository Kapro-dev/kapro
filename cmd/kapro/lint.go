package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/internal/lint"
)

// `kapro lint` is a static-analysis pass for Kapro YAML manifests.
// It runs without a cluster connection so CI pipelines and pre-commit
// hooks can use it cheaply.

func newLintCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "lint <file>...",
		Short: "Statically check Kapro YAML for schema + best-practice issues",
		Long: `Lint Kapro YAML manifests without contacting a cluster.

Checks Fleet, Promotion, and Plan resources for:
  - missing required fields
  - duplicate or dangling stage references
  - dependency cycles in Plan DAGs
  - manual gates with no listed approvers
  - metric gates with neither preset nor threshold
  - duplicate Promotion scope targets
  - missing Promotion.spec.timeout (advisory)

Exit code: 0 if no errors, 1 if any ERROR (or any WARN under --strict).

Pass - to read from stdin.

Examples:
  kapro lint examples/01-quickstarts/00-flux/*.yaml
  kapro lint --strict promotion.yaml plan.yaml
  cat promotion.yaml | kapro lint -
  kapro lint -o json promotion.yaml`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLint(args, strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "Treat warnings as errors (non-zero exit)")
	return cmd
}

func runLint(paths []string, strict bool) error {
	var allIssues []lint.Issue
	for _, p := range paths {
		var (
			data []byte
			err  error
		)
		if p == "-" {
			data, err = io.ReadAll(os.Stdin)
			p = "<stdin>"
		} else {
			data, err = os.ReadFile(p)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		allIssues = append(allIssues, lint.LintFile(p, data)...)
	}

	if cli.IsJSON() {
		if err := cli.JSON(allIssues); err != nil {
			return err
		}
		if lint.HasErrors(allIssues, strict) {
			os.Exit(1)
		}
		return nil
	}

	renderLintIssues(allIssues, strict)
	if lint.HasErrors(allIssues, strict) {
		os.Exit(1)
	}
	return nil
}

func renderLintIssues(issues []lint.Issue, strict bool) {
	if len(issues) == 0 {
		cli.Success("No issues found.")
		return
	}

	var errors, warnings int
	for _, i := range issues {
		switch i.Severity {
		case lint.SeverityError:
			errors++
			cli.Error(i.String())
		case lint.SeverityWarn:
			warnings++
			if strict {
				cli.Error(i.String())
			} else {
				cli.Warn(i.String())
			}
		}
	}

	fmt.Fprintln(cli.Out)
	summary := fmt.Sprintf("%d error(s), %d warning(s)", errors, warnings)
	switch {
	case errors > 0:
		cli.Error(summary)
	case warnings > 0 && strict:
		cli.Error(summary + " (strict mode)")
	case warnings > 0:
		cli.Warn(summary)
	default:
		cli.Success(summary)
	}
}

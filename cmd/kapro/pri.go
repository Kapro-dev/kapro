package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/pkg/pri"
	pricollector "kapro.io/kapro/pkg/pri/collector"
)

func newPRICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pri",
		Short: "Validate and emit OpenPromotions PRI records",
		Long: `Validate and emit OpenPromotions PRI v0.1 records.

Kapro's PRI support is a reference implementation: it validates portable PRI
documents and can collect Kapro runtime state into PRI PromotionRun records for
other systems to consume.`,
	}
	cmd.AddCommand(newPRIValidateCmd())
	cmd.AddCommand(newPRICollectCmd())
	cmd.AddCommand(newPRIProfileCmd())
	return cmd
}

func newPRIValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <file-or-dir ...>",
		Short: "Validate PRI YAML or JSON documents",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPRIValidate(args)
		},
	}
	return cmd
}

type priValidateReport struct {
	Valid     bool           `json:"valid"`
	Documents int            `json:"documents"`
	Kinds     map[string]int `json:"kinds"`
	Message   string         `json:"message,omitempty"`
}

func runPRIValidate(paths []string) error {
	docs, err := pri.ReadDocuments(paths...)
	report := priValidateReport{Kinds: map[string]int{}}
	if err != nil {
		report.Message = err.Error()
		if cli.IsJSON() {
			_ = cli.JSON(report)
		}
		return err
	}
	report.Valid = true
	report.Documents = len(docs)
	for _, doc := range docs {
		report.Kinds[pri.KindOf(doc.Object)]++
	}
	if cli.IsJSON() {
		return cli.JSON(report)
	}
	cli.Header("PRI validation")
	cli.KV("Valid", "true")
	cli.KV("Documents", fmt.Sprintf("%d", report.Documents))
	for _, kind := range sortedKindKeys(report.Kinds) {
		cli.KV(kind, fmt.Sprintf("%d", report.Kinds[kind]))
	}
	return nil
}

func newPRICollectCmd() *cobra.Command {
	var opts priCollectOptions
	cmd := &cobra.Command{
		Use:   "collect",
		Short: "Collect live Kapro runtime state as PRI documents",
		Long: `Collect live Kapro runtime state as PRI v0.1 documents.

By default this is a safe one-shot export. Use --watch to run it as a small
collector agent that periodically rewrites an output directory or streams
documents to stdout.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPRICollect(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&opts.PromotionRun, "promotionrun", "", "PromotionRun name to collect (required)")
	cmd.Flags().StringVar(&opts.Out, "out", "-", "Output path: '-' for stdout or a directory for YAML files")
	cmd.Flags().BoolVar(&opts.Watch, "watch", false, "Run continuously as a collector agent")
	cmd.Flags().DurationVar(&opts.Interval, "interval", 30*time.Second, "Watch interval")
	_ = cmd.MarkFlagRequired("promotionrun")
	return cmd
}

type priCollectOptions struct {
	Kubeconfig   string
	PromotionRun string
	Out          string
	Watch        bool
	Interval     time.Duration
}

func runPRICollect(ctx context.Context, opts priCollectOptions) error {
	if opts.Interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	c, err := buildClient(opts.Kubeconfig)
	if err != nil {
		return err
	}
	collector := pricollector.New(c)
	collectOnce := func() error {
		bundle, err := collector.CollectPromotionRun(ctx, opts.PromotionRun)
		if err != nil {
			return err
		}
		return writePRIDocuments(opts.Out, bundle.Documents())
	}
	if !opts.Watch {
		return collectOnce()
	}
	if err := collectOnce(); err != nil {
		return err
	}
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := collectOnce(); err != nil {
				return err
			}
		}
	}
}

func newPRIProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Print Kapro's PRI binding and conformance profile",
		RunE: func(_ *cobra.Command, _ []string) error {
			return writePRIDocuments("-", []any{
				pricollector.ReferenceBinding(),
				pricollector.ReferenceConformanceProfile(),
			})
		},
	}
	return cmd
}

func writePRIDocuments(out string, docs []any) error {
	if out == "" || out == "-" {
		if cli.IsJSON() {
			return pri.EncodeJSONDocuments(os.Stdout, docs)
		}
		return pri.EncodeYAMLDocuments(os.Stdout, docs)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, doc := range docs {
		name := pri.NameOf(doc)
		kind := pri.KindOf(doc)
		if name == "" || kind == "" {
			return fmt.Errorf("cannot write unknown PRI document %T", doc)
		}
		path := filepath.Join(out, fmt.Sprintf("%s-%s.yaml", strings.ToLower(kind), name))
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := pri.EncodeYAMLDocuments(file, []any{doc}); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	if !cli.IsJSON() {
		cli.Header("PRI collector")
		cli.KV("Output", out)
		cli.KV("Documents", fmt.Sprintf("%d", len(docs)))
	}
	return nil
}

func sortedKindKeys(kinds map[string]int) []string {
	keys := make([]string, 0, len(kinds))
	for key := range kinds {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

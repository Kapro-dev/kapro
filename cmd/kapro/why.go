package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

func newWhyCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "why <promotionrun>",
		Short: "Explain a PromotionRun from DecisionTrace records",
		Long: `Explain why Kapro advanced, deferred, skipped, failed, or rolled back
parts of a PromotionRun by reading its DecisionTrace audit records.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhy(cmd.Context(), kubeconfig, args[0])
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runWhy(ctx context.Context, kubeconfigPath, promotionRun string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return runWhyWithClient(ctx, c, promotionRun)
}

func runWhyWithClient(ctx context.Context, c client.Client, promotionRun string) error {
	report, err := collectWhy(ctx, c, promotionRun)
	if err != nil {
		return err
	}
	if cli.IsJSON() {
		return cli.JSON(report)
	}
	renderWhy(report)
	return nil
}

type whyReport struct {
	PromotionRun string     `json:"promotionRun"`
	Traces       []whyTrace `json:"traces"`
}

type whyTrace struct {
	Name      string                                `json:"name"`
	Time      string                                `json:"time,omitempty"`
	EventType kaprov1alpha2.DecisionTraceEventType  `json:"eventType"`
	Source    string                                `json:"source"`
	Phase     string                                `json:"phase,omitempty"`
	Reason    string                                `json:"reason,omitempty"`
	Plan      string                                `json:"plan,omitempty"`
	Stage     string                                `json:"stage,omitempty"`
	Target    string                                `json:"target,omitempty"`
	Message   string                                `json:"message,omitempty"`
	Signed    bool                                  `json:"signed"`
	Signature *whyTraceSignature                    `json:"signature,omitempty"`
	Evidence  []kaprov1alpha2.DecisionTraceEvidence `json:"evidence,omitempty"`
}

type whyTraceSignature struct {
	Algorithm     string `json:"algorithm,omitempty"`
	KeyID         string `json:"keyID,omitempty"`
	PayloadDigest string `json:"payloadDigest,omitempty"`
	Signature     string `json:"signature,omitempty"`
	SignatureRef  string `json:"signatureRef,omitempty"`
}

func collectWhy(ctx context.Context, c client.Client, promotionRun string) (*whyReport, error) {
	var list kaprov1alpha2.DecisionTraceList
	if err := c.List(ctx, &list, client.MatchingLabels{promotionRunLabelKey: promotionRun}); err != nil {
		return nil, fmt.Errorf("list decisiontraces for promotionrun %q: %w", promotionRun, err)
	}
	sortDecisionTraces(list.Items)
	report := &whyReport{PromotionRun: promotionRun, Traces: []whyTrace{}}
	for _, trace := range list.Items {
		report.Traces = append(report.Traces, whyTraceFromDecisionTrace(trace))
	}
	return report, nil
}

func whyTraceFromDecisionTrace(trace kaprov1alpha2.DecisionTrace) whyTrace {
	status := trace.Status
	return whyTrace{
		Name:      trace.Name,
		Time:      decisionTraceTime(trace).Format(time.RFC3339),
		EventType: trace.Spec.EventType,
		Source:    trace.Spec.Source,
		Phase:     trace.Spec.Phase,
		Reason:    trace.Spec.Reason,
		Plan:      trace.Spec.Plan,
		Stage:     trace.Spec.Stage,
		Target:    trace.Spec.Target,
		Message:   trace.Spec.Message,
		Signed:    status.Signed,
		Signature: signatureDetails(status),
		Evidence:  trace.Spec.Evidence,
	}
}

func sortDecisionTraces(items []kaprov1alpha2.DecisionTrace) {
	sort.Slice(items, func(i, j int) bool {
		ti := decisionTraceTime(items[i])
		tj := decisionTraceTime(items[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return items[i].Name < items[j].Name
	})
}

func decisionTraceTime(trace kaprov1alpha2.DecisionTrace) time.Time {
	if !trace.Spec.Time.IsZero() {
		return trace.Spec.Time.Time
	}
	return trace.CreationTimestamp.Time
}

func renderWhy(report *whyReport) {
	cli.Header("Why " + report.PromotionRun)
	if len(report.Traces) == 0 {
		cli.Muted("No DecisionTrace records found.")
		return
	}
	tbl := cli.NewTable("TIME", "TRACE", "TYPE", "PHASE", "REASON", "SCOPE", "SOURCE", "SIGNED", "MESSAGE")
	for _, trace := range report.Traces {
		tbl.AddRow(
			trace.Time,
			trace.Name,
			string(trace.EventType),
			stringOrUnset(trace.Phase),
			stringOrUnset(trace.Reason),
			whyScope(trace),
			stringOrUnset(trace.Source),
			signatureText(trace),
			truncate(trace.Message, 72),
		)
	}
	tbl.Render()
}

func whyScope(trace whyTrace) string {
	var parts []string
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
		return "-"
	}
	return strings.Join(parts, " ")
}

func signatureDetails(status kaprov1alpha2.DecisionTraceStatus) *whyTraceSignature {
	if !status.Signed {
		return nil
	}
	return &whyTraceSignature{
		Algorithm:     status.SignatureAlgorithm,
		KeyID:         status.SignatureKeyID,
		PayloadDigest: status.PayloadDigest,
		Signature:     status.Signature,
		SignatureRef:  status.SignatureRef,
	}
}

func signatureText(trace whyTrace) string {
	if !trace.Signed {
		return "unsigned"
	}
	var parts []string
	if trace.Signature != nil && trace.Signature.Algorithm != "" {
		parts = append(parts, trace.Signature.Algorithm)
	}
	if trace.Signature != nil && trace.Signature.KeyID != "" {
		parts = append(parts, "key="+trace.Signature.KeyID)
	}
	if len(parts) == 0 {
		return "signed"
	}
	return strings.Join(parts, " ")
}

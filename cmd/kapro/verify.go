package main

import (
	"context"
	"errors"
	"fmt"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/internal/decisiontrace"
)

func newVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify Kapro audit and release artifacts",
	}
	cmd.AddCommand(newVerifyDecisionTraceCmd())
	return cmd
}

func newVerifyDecisionTraceCmd() *cobra.Command {
	var kubeconfig string
	var publicKeyFile string
	cmd := &cobra.Command{
		Use:   "decisiontrace <name>",
		Short: "Verify a signed DecisionTrace",
		Long: `Verify a DecisionTrace status signature against an Ed25519 public key.

The signature covers the canonical DecisionTrace spec payload only. Kubernetes
metadata and status fields are intentionally excluded from the signed payload.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerifyDecisionTrace(cmd.Context(), kubeconfig, args[0], publicKeyFile)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&publicKeyFile, "public-key", "", "PEM-encoded Ed25519 public key")
	_ = cmd.MarkFlagRequired("public-key")
	return cmd
}

func runVerifyDecisionTrace(ctx context.Context, kubeconfigPath, name, publicKeyFile string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return runVerifyDecisionTraceWithClient(ctx, c, name, publicKeyFile)
}

func runVerifyDecisionTraceWithClient(ctx context.Context, c client.Client, name, publicKeyFile string) error {
	report, err := collectDecisionTraceVerification(ctx, c, name, publicKeyFile)
	if report.Name == "" {
		report.Name = name
	}
	if cli.IsJSON() {
		if err != nil && report.Message == "" {
			report.Message = err.Error()
		}
		if jsonErr := cli.JSON(report); jsonErr != nil {
			return jsonErr
		}
		return err
	}
	if err != nil {
		if report.Message == "" {
			report.Message = err.Error()
		}
		renderDecisionTraceVerification(report)
		return err
	}
	renderDecisionTraceVerification(report)
	return nil
}

type decisionTraceVerificationReport struct {
	Name          string `json:"name"`
	Verified      bool   `json:"verified"`
	KeyID         string `json:"keyID,omitempty"`
	Algorithm     string `json:"algorithm,omitempty"`
	PayloadDigest string `json:"payloadDigest,omitempty"`
	Message       string `json:"message,omitempty"`
}

func collectDecisionTraceVerification(ctx context.Context, c client.Client, name, publicKeyFile string) (decisionTraceVerificationReport, error) {
	var trace kaproruntimev1alpha1.DecisionTrace
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &trace); err != nil {
		return decisionTraceVerificationReport{}, fmt.Errorf("get DecisionTrace %q: %w", name, err)
	}
	report := decisionTraceVerificationReport{
		Name:          trace.Name,
		KeyID:         trace.Status.SignatureKeyID,
		Algorithm:     trace.Status.SignatureAlgorithm,
		PayloadDigest: trace.Status.PayloadDigest,
	}
	if !trace.Status.Signed {
		report.Message = "DecisionTrace is unsigned"
		return report, errors.New("DecisionTrace is unsigned")
	}
	publicKey, err := decisiontrace.LoadEd25519PublicKeyFile(publicKeyFile)
	if err != nil {
		report.Message = err.Error()
		return report, err
	}
	sig := decisiontrace.Signature{
		Algorithm:     trace.Status.SignatureAlgorithm,
		KeyID:         trace.Status.SignatureKeyID,
		PayloadDigest: trace.Status.PayloadDigest,
		Signature:     trace.Status.Signature,
	}
	if err := decisiontrace.VerifyEd25519(trace.Spec, sig, publicKey); err != nil {
		report.Message = err.Error()
		return report, err
	}
	report.Verified = true
	report.Message = "DecisionTrace signature verified"
	return report, nil
}

func renderDecisionTraceVerification(report decisionTraceVerificationReport) {
	cli.Header("DecisionTrace " + report.Name)
	cli.KV("Verified", fmt.Sprintf("%t", report.Verified))
	cli.KV("Algorithm", stringOrUnset(report.Algorithm))
	cli.KV("Key ID", stringOrUnset(report.KeyID))
	cli.KV("Payload Digest", stringOrUnset(report.PayloadDigest))
	if report.Message != "" {
		cli.KV("Message", report.Message)
	}
}

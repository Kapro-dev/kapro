package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/internal/decisiontrace"
)

func TestRunVerifyDecisionTraceVerifiesSignedTrace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	trace := signedDecisionTraceForVerifyTest(t, "trace-a", privateKey)
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(trace).
		Build()

	out := withCapturedOutput(t, func() {
		if err := runVerifyDecisionTraceWithClient(context.Background(), c, "trace-a", writePublicKeyForVerifyTest(t, publicKey)); err != nil {
			t.Fatalf("runVerifyDecisionTraceWithClient: %v", err)
		}
	})

	for _, want := range []string{
		"DecisionTrace trace-a",
		"Verified",
		"true",
		"Ed25519",
		"test-key",
		"sha256:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunVerifyDecisionTraceJSONReturnsFailureReportAndError(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	trace := signedDecisionTraceForVerifyTest(t, "trace-a", privateKey)
	trace.Spec.Message = "tampered after signing"
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(trace).
		Build()
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	var runErr error
	out := withCapturedOutput(t, func() {
		runErr = runVerifyDecisionTraceWithClient(context.Background(), c, "trace-a", writePublicKeyForVerifyTest(t, publicKey))
	})
	if runErr == nil {
		t.Fatalf("expected verification error, output:\n%s", out)
	}

	var got decisionTraceVerificationReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal verification JSON: %v\nraw: %s", err, out)
	}
	if got.Verified || got.Name != "trace-a" || got.Message == "" {
		t.Fatalf("unexpected verification report: %+v", got)
	}
}

func TestRunVerifyDecisionTraceHumanFailureStillRendersReport(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	trace := signedDecisionTraceForVerifyTest(t, "trace-a", privateKey)
	trace.Spec.Message = "tampered after signing"
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(trace).
		Build()

	var runErr error
	out := withCapturedOutput(t, func() {
		runErr = runVerifyDecisionTraceWithClient(context.Background(), c, "trace-a", writePublicKeyForVerifyTest(t, publicKey))
	})
	if runErr == nil {
		t.Fatalf("expected verification error, output:\n%s", out)
	}
	for _, want := range []string{
		"DecisionTrace trace-a",
		"Verified",
		"false",
		"Ed25519",
		"test-key",
		"decision trace payload digest mismatch",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failure output missing %q:\n%s", want, out)
		}
	}
}

func TestNewVerifyDecisionTraceCmdRequiresPublicKey(t *testing.T) {
	cmd := newVerifyDecisionTraceCmd()
	cmd.SetArgs([]string{"trace-a"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `required flag(s) "public-key" not set`) {
		t.Fatalf("expected --public-key validation error, got %v", err)
	}
}

func signedDecisionTraceForVerifyTest(t *testing.T, name string, privateKey ed25519.PrivateKey) *kaproruntimev1alpha1.DecisionTrace {
	t.Helper()
	spec := kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		Plan:         "canary",
		Stage:        "prod",
		Target:       "cluster-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventGateEvaluate,
		Source:       "slo",
		Phase:        "Failed",
		Reason:       "SLOViolation",
		Message:      "error budget exhausted",
		Time:         metav1.Unix(1, 0),
	}
	signer, err := decisiontrace.NewEd25519Signer("test-key", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	sig, err := signer.SignDecisionTrace(context.Background(), spec)
	if err != nil {
		t.Fatalf("SignDecisionTrace: %v", err)
	}
	return &kaproruntimev1alpha1.DecisionTrace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
		Status: kaproruntimev1alpha1.DecisionTraceStatus{
			Signed:             true,
			SignatureAlgorithm: sig.Algorithm,
			SignatureKeyID:     sig.KeyID,
			PayloadDigest:      sig.PayloadDigest,
			Signature:          sig.Signature,
		},
	}
}

func writePublicKeyForVerifyTest(t *testing.T, publicKey ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	path := t.TempDir() + "/decisiontrace-public.pem"
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

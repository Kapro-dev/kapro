package decisiontrace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestEd25519SignerSignsAndVerifiesCanonicalPayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := NewEd25519Signer("test-key", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	spec := signingTestSpec()

	sig, err := signer.SignDecisionTrace(context.Background(), spec)
	if err != nil {
		t.Fatalf("SignDecisionTrace: %v", err)
	}
	if sig.Algorithm != signatureAlgorithmEd25519 {
		t.Fatalf("algorithm = %q", sig.Algorithm)
	}
	if sig.KeyID != "test-key" {
		t.Fatalf("key id = %q", sig.KeyID)
	}
	if sig.PayloadDigest == "" {
		t.Fatal("payload digest was empty")
	}
	if err := VerifyEd25519(spec, sig, publicKey); err != nil {
		t.Fatalf("VerifyEd25519: %v", err)
	}
}

func TestEd25519VerifyRejectsTamperedTrace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := NewEd25519Signer("test-key", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	spec := signingTestSpec()
	sig, err := signer.SignDecisionTrace(context.Background(), spec)
	if err != nil {
		t.Fatalf("SignDecisionTrace: %v", err)
	}
	spec.Message = "changed"

	if err := VerifyEd25519(spec, sig, publicKey); err == nil {
		t.Fatal("VerifyEd25519 accepted tampered payload")
	}
}

func TestCanonicalPayloadStableAcrossMapOrdering(t *testing.T) {
	specA := signingTestSpec()
	specA.Evidence = []kaprov1alpha2.DecisionTraceEvidence{{
		Type:   "gate",
		Source: "slo",
		Detail: map[string]string{"b": "2", "a": "1"},
	}}
	specB := signingTestSpec()
	specB.Evidence = []kaprov1alpha2.DecisionTraceEvidence{{
		Type:   "gate",
		Source: "slo",
		Detail: map[string]string{"a": "1", "b": "2"},
	}}

	payloadA, digestA, err := CanonicalPayload(specA)
	if err != nil {
		t.Fatalf("CanonicalPayload A: %v", err)
	}
	payloadB, digestB, err := CanonicalPayload(specB)
	if err != nil {
		t.Fatalf("CanonicalPayload B: %v", err)
	}
	if string(payloadA) != string(payloadB) {
		t.Fatalf("canonical payload differs\nA: %s\nB: %s", payloadA, payloadB)
	}
	if digestA != digestB {
		t.Fatalf("digest differs: %s != %s", digestA, digestB)
	}
}

func TestSignerFromEnvLoadsPKCS8Ed25519Key(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyFile := t.TempDir() + "/decisiontrace-key.pem"
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(SigningKeyFileEnv, keyFile)
	t.Setenv(SigningKeyIDEnv, "env-key")

	signer, err := SignerFromEnv()
	if err != nil {
		t.Fatalf("SignerFromEnv: %v", err)
	}
	if signer == nil {
		t.Fatal("SignerFromEnv returned nil signer")
	}
	sig, err := signer.SignDecisionTrace(context.Background(), signingTestSpec())
	if err != nil {
		t.Fatalf("SignDecisionTrace: %v", err)
	}
	if sig.KeyID != "env-key" {
		t.Fatalf("key id = %q", sig.KeyID)
	}
}

func TestSignerFromEnvDisabledWhenKeyFileUnset(t *testing.T) {
	t.Setenv(SigningKeyFileEnv, "")
	t.Setenv(SigningKeyIDEnv, "")

	signer, err := SignerFromEnv()
	if err != nil {
		t.Fatalf("SignerFromEnv: %v", err)
	}
	if signer != nil {
		t.Fatal("SignerFromEnv returned signer when disabled")
	}
}

func signingTestSpec() kaprov1alpha2.DecisionTraceSpec {
	return kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		Plan:         "canary",
		Stage:        "prod",
		Target:       "cluster-a",
		EventType:    kaprov1alpha2.DecisionTraceEventGateEvaluate,
		Source:       "slo",
		Phase:        "Failed",
		Reason:       "SLOViolation",
		Message:      "error budget exhausted",
		Time:         metav1.Unix(1, 0),
	}
}

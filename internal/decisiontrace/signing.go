package decisiontrace

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	SigningKeyFileEnv = "KAPRO_DECISIONTRACE_SIGNING_KEY_FILE"
	SigningKeyIDEnv   = "KAPRO_DECISIONTRACE_SIGNING_KEY_ID"

	signatureAlgorithmEd25519 = "Ed25519"
	signatureContext          = "kapro.io/DecisionTrace/v1alpha2\n"
)

// Signature is a detached signature over a canonical DecisionTrace spec.
type Signature struct {
	Algorithm     string
	KeyID         string
	PayloadDigest string
	Signature     string
}

// Signer signs canonical DecisionTrace spec payloads.
type Signer interface {
	SignDecisionTrace(ctx context.Context, spec kaprov1alpha2.DecisionTraceSpec) (Signature, error)
}

// Ed25519Signer signs DecisionTrace records with a local Ed25519 private key.
type Ed25519Signer struct {
	KeyID      string
	PrivateKey ed25519.PrivateKey
}

// NewEd25519Signer returns an Ed25519 DecisionTrace signer.
func NewEd25519Signer(keyID string, privateKey ed25519.PrivateKey) (Ed25519Signer, error) {
	if strings.TrimSpace(keyID) == "" {
		return Ed25519Signer{}, errors.New("decision trace signing key id is required")
	}
	if l := len(privateKey); l != ed25519.PrivateKeySize {
		return Ed25519Signer{}, fmt.Errorf("ed25519 private key has length %d, want %d", l, ed25519.PrivateKeySize)
	}
	return Ed25519Signer{KeyID: strings.TrimSpace(keyID), PrivateKey: privateKey}, nil
}

// SignDecisionTrace signs the canonical representation of spec.
func (s Ed25519Signer) SignDecisionTrace(_ context.Context, spec kaprov1alpha2.DecisionTraceSpec) (Signature, error) {
	payload, digest, err := CanonicalPayload(spec)
	if err != nil {
		return Signature{}, err
	}
	return Signature{
		Algorithm:     signatureAlgorithmEd25519,
		KeyID:         s.KeyID,
		PayloadDigest: digest,
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(s.PrivateKey, signingMessage(payload))),
	}, nil
}

// VerifyEd25519 verifies a detached DecisionTrace signature.
func VerifyEd25519(spec kaprov1alpha2.DecisionTraceSpec, sig Signature, publicKey ed25519.PublicKey) error {
	if sig.Algorithm != signatureAlgorithmEd25519 {
		return fmt.Errorf("unsupported decision trace signature algorithm %q", sig.Algorithm)
	}
	if l := len(publicKey); l != ed25519.PublicKeySize {
		return fmt.Errorf("ed25519 public key has length %d, want %d", l, ed25519.PublicKeySize)
	}
	payload, digest, err := CanonicalPayload(spec)
	if err != nil {
		return err
	}
	if sig.PayloadDigest != digest {
		return fmt.Errorf("decision trace payload digest mismatch")
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil {
		return fmt.Errorf("decode decision trace signature: %w", err)
	}
	if !ed25519.Verify(publicKey, signingMessage(payload), raw) {
		return errors.New("decision trace signature verification failed")
	}
	return nil
}

// CanonicalPayload returns the stable JSON payload and sha256 digest used for
// signing. It intentionally excludes Kubernetes metadata and status.
func CanonicalPayload(spec kaprov1alpha2.DecisionTraceSpec) ([]byte, string, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return nil, "", fmt.Errorf("marshal canonical decision trace payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	return payload, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func signingMessage(payload []byte) []byte {
	message := make([]byte, 0, len(signatureContext)+len(payload))
	message = append(message, signatureContext...)
	message = append(message, payload...)
	return message
}

// SignerFromEnv loads an optional local Ed25519 signer. Unset env returns nil.
func SignerFromEnv() (Signer, error) {
	keyFile := strings.TrimSpace(os.Getenv(SigningKeyFileEnv))
	if keyFile == "" {
		return nil, nil
	}
	keyID := strings.TrimSpace(os.Getenv(SigningKeyIDEnv))
	if keyID == "" {
		keyID = filepath.Base(keyFile)
	}
	privateKey, err := LoadEd25519PrivateKeyFile(keyFile)
	if err != nil {
		return nil, err
	}
	signer, err := NewEd25519Signer(keyID, privateKey)
	if err != nil {
		return nil, err
	}
	return signer, nil
}

// LoadEd25519PrivateKeyFile reads a PEM-encoded PKCS#8 Ed25519 private key.
func LoadEd25519PrivateKeyFile(path string) (ed25519.PrivateKey, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read decision trace signing key: %w", err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, errors.New("decision trace signing key must be PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse decision trace signing key: %w", err)
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	}
	return nil, fmt.Errorf("decision trace signing key must be Ed25519")
}

func statusForSignature(sig Signature) kaprov1alpha2.DecisionTraceStatus {
	return kaprov1alpha2.DecisionTraceStatus{
		Signed:             true,
		SignatureAlgorithm: sig.Algorithm,
		SignatureKeyID:     sig.KeyID,
		PayloadDigest:      sig.PayloadDigest,
		Signature:          sig.Signature,
	}
}

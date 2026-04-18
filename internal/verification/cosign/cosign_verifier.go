// Package cosign implements the verification.Verifier interface using
// sigstore/cosign v2. It is stateless and safe for concurrent use.
//
// Keyless mode (default): verifies against Sigstore's public Rekor
// transparency log and Fulcio CA.  No key management required.
//
// Key mode: verifies against a static PEM-encoded cosign public key or a
// key reference (k8s://namespace/secret, cosign://, etc.).
package cosign

import (
	"context"
	"crypto"
	"encoding/pem"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	cosignpkg "github.com/sigstore/cosign/v2/pkg/cosign"
	cosignoci "github.com/sigstore/cosign/v2/pkg/oci"
	cosignsig "github.com/sigstore/cosign/v2/pkg/signature"
	sigsig "github.com/sigstore/sigstore/pkg/signature"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/verification"
)

// Verifier implements verification.Verifier using cosign v2.
// All configuration is supplied per-request; the struct holds no state.
type Verifier struct{}

var _ verification.Verifier = &Verifier{}

// Verify verifies the signature (and, in future, attestations) for the image
// referenced by req.ImageRef.  req.ImageRef SHOULD include a digest
// (registry/repo@sha256:...) for tamper-proof verification; tag references
// are accepted but less secure.
func (v *Verifier) Verify(ctx context.Context, req verification.VerifyRequest) (verification.VerifyResult, error) {
	logger := log.FromContext(ctx)

	if req.ImageRef == "" {
		return verification.VerifyResult{Verified: false, Message: "no image reference provided"}, nil
	}

	ref, err := name.ParseReference(req.ImageRef)
	if err != nil {
		return verification.VerifyResult{
			Verified: false,
			Message:  fmt.Sprintf("invalid image reference %q: %v", req.ImageRef, err),
		}, nil
	}

	co, err := buildCheckOpts(ctx, req)
	if err != nil {
		// Infrastructure setup failure — return as hard error so the gate retries.
		return verification.VerifyResult{}, fmt.Errorf("cosign: verify: %w", err)
	}

	sigs, _, err := cosignpkg.VerifyImageSignatures(ctx, ref, co)
	if err != nil {
		logger.Error(err, "cosign: signature verification failed", "image", req.ImageRef)
		return verification.VerifyResult{
			Verified: false,
			Message:  fmt.Sprintf("cosign: verify: %v", err),
		}, nil
	}

	cert := certFromSigs(sigs)
	msg := fmt.Sprintf("verified %d signature(s) for %s", len(sigs), req.ImageRef)
	logger.Info("cosign: verification passed",
		"image", req.ImageRef,
		"signatures", len(sigs),
	)

	return verification.VerifyResult{
		Verified:    true,
		Signatures:  len(sigs),
		Certificate: cert,
		Message:     msg,
	}, nil
}

// buildCheckOpts translates a VerifyRequest into cosign CheckOpts.
func buildCheckOpts(ctx context.Context, req verification.VerifyRequest) (*cosignpkg.CheckOpts, error) {
	co := &cosignpkg.CheckOpts{
		IgnoreTlog: req.Offline,
	}

	if req.Key != nil {
		sv, err := loadVerifier(ctx, req.Key)
		if err != nil {
			return nil, fmt.Errorf("load public key: %w", err)
		}
		co.SigVerifier = sv
		return co, nil
	}

	// Keyless — Sigstore public infrastructure.
	cfg := req.Keyless
	if cfg == nil {
		cfg = &verification.KeylessConfig{}
	}
	if cfg.ExpectedIssuer != "" || cfg.ExpectedIdentity != "" {
		co.Identities = []cosignpkg.Identity{{
			Issuer:  cfg.ExpectedIssuer,
			Subject: cfg.ExpectedIdentity,
		}}
	}

	return co, nil
}

// loadVerifier returns a signature.Verifier from key material in the KeyConfig.
// It prefers PublicKey bytes over KeyRef when both are supplied.
func loadVerifier(ctx context.Context, key *verification.KeyConfig) (sigsig.Verifier, error) {
	if len(key.PublicKey) > 0 {
		sv, err := cosignsig.LoadPublicKeyRaw(key.PublicKey, crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("parse PEM public key: %w", err)
		}
		return sv, nil
	}
	if key.KeyRef != "" {
		sv, err := cosignsig.LoadPublicKey(ctx, key.KeyRef)
		if err != nil {
			return nil, fmt.Errorf("load key ref %q: %w", key.KeyRef, err)
		}
		return sv, nil
	}
	return nil, fmt.Errorf("KeyConfig requires PublicKey bytes or KeyRef to be set")
}

// certFromSigs extracts the PEM-encoded leaf certificate from the first
// signature that carries one (keyless / Fulcio-issued).  Returns "" for
// key-based signatures that carry no certificate.
func certFromSigs(sigs []cosignoci.Signature) string {
	for _, sig := range sigs {
		cert, err := sig.Cert()
		if err != nil || cert == nil {
			continue
		}
		return string(pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		}))
	}
	return ""
}

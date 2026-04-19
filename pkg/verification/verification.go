// Package verification defines KVI — the Kapro Verification Interface.
//
// KVI is the artifact signature and attestation verification contract.
// Kapro uses this during pipeline promotion to ensure only signed artifacts advance.
//
// Built-in implementations live in internal/verification/:
//   - cosign/ — sigstore/cosign v2, keyless + static-key + attestation
//
// External implementations (Notary, in-toto, TUF, enterprise PKI) register via
// Implementations can use the Verifier interface and wire in at startup.
//
// The NopVerifier in this package skips all verification — for dev/test only.
package verification

import "context"

// KeylessConfig uses Sigstore's public Rekor transparency log (no key management).
type KeylessConfig struct {
	// RekorURL is the transparency log. Default: https://rekor.sigstore.dev
	RekorURL string
	// FulcioURL is the certificate authority. Default: https://fulcio.sigstore.dev
	FulcioURL string
	// ExpectedIdentity is the OIDC identity that signed the image.
	ExpectedIdentity string
	// ExpectedIssuer is the OIDC issuer.
	ExpectedIssuer string
}

// KeyConfig uses a static cosign public key for verification.
type KeyConfig struct {
	// PublicKey is the PEM-encoded cosign public key.
	PublicKey []byte
	// KeyRef is a reference to a key: k8s://namespace/secret-name or cosign://...
	KeyRef string
}

// AttestationConfig controls attestation verification.
type AttestationConfig struct {
	// PredicateType is the in-toto predicate type to verify.
	// e.g. https://slsa.dev/provenance/v0.2
	PredicateType string
	// Policy is an optional OPA/Rego policy to evaluate against the predicate JSON.
	Policy string
}

// VerifyRequest is the input to Verify.
type VerifyRequest struct {
	// ImageRef is the full OCI reference including digest: registry/repo@sha256:...
	ImageRef string
	// Keyless uses Sigstore keyless verification (default when Key is nil).
	Keyless *KeylessConfig
	// Key uses static key verification.
	Key *KeyConfig
	// Attestations lists attestation types to verify in addition to the signature.
	Attestations []AttestationConfig
	// Offline skips network calls to Rekor (uses bundle embedded in signature).
	Offline bool
}

// VerifyResult is the output of Verify.
type VerifyResult struct {
	Verified    bool
	Signatures  int    // number of valid signatures found
	Certificate string // signer certificate PEM (keyless mode)
	Message     string
}

// Verifier is KVI: the Kapro Verification Interface.
//
// Implementations must be safe for concurrent use.
type Verifier interface {
	Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error)
}

// NopVerifier passes all verification checks without performing any real check.
// Use in development environments or when verification is explicitly disabled.
type NopVerifier struct{}

func (NopVerifier) Verify(_ context.Context, req VerifyRequest) (VerifyResult, error) {
	return VerifyResult{
		Verified: true,
		Message:  "nop: verification skipped",
	}, nil
}

// compile-time check
var _ Verifier = NopVerifier{}

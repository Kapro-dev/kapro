package gate

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/verification"
)

// VerificationGate is a Gate that verifies artifact signatures before a
// promotion is allowed to proceed.  It uses the injected Verifier
// (default: cosign v2) to check that the image referenced by the Promotion
// has a valid cryptographic signature.
//
// Policy precedence:
//  1. If req.Policy.Spec.Gate.Verification.CosignPolicy is set — use it.
//  2. Otherwise fall back to default keyless with Sigstore public infrastructure.
//
// Nil-safe: when Verifier is nil the gate passes through.
type VerificationGate struct {
	Verifier verification.Verifier
	// KeyReader fetches a static public key from a Secret.
	// Injected by the operator; nil-safe (static key mode will error if nil + Key policy set).
	KeyReader SecretKeyReader
}

// SecretKeyReader fetches a PEM-encoded cosign public key from a Kubernetes Secret.
type SecretKeyReader interface {
	ReadKey(ctx context.Context, namespace, name, key string) ([]byte, error)
}

var _ Gate = &VerificationGate{}

// Evaluate builds a VerifyRequest from the Promotion spec and calls the
// configured Verifier.  The image reference is taken from
// Promotion.Spec.Version, which must be a digest-pinned OCI ref
// (registry/repo@sha256:...).
func (g *VerificationGate) Evaluate(ctx context.Context, req Request) (Result, error) {
	logger := log.FromContext(ctx)

	if g.Verifier == nil {
		logger.Info("VerificationGate: verifier is nil — pass-through")
		return Result{Passed: true, Message: "verification skipped: no verifier configured"}, nil
	}
	if req.Promotion == nil {
		return Result{}, fmt.Errorf("verification gate: nil Promotion in request")
	}

	imageRef := req.Promotion.Spec.Version
	if imageRef == "" {
		logger.Info("VerificationGate: empty Version field — pass-through",
			"promotion", req.Promotion.Name,
		)
		return Result{Passed: true, Message: "verification skipped: no image reference in Promotion.Spec.Version"}, nil
	}

	vreq, err := g.buildVerifyRequest(ctx, req.Policy, imageRef)
	if err != nil {
		return Result{}, fmt.Errorf("verification gate: build request: %w", err)
	}

	result, err := g.Verifier.Verify(ctx, vreq)
	if err != nil {
		logger.Error(err, "VerificationGate: verifier returned error",
			"promotion", req.Promotion.Name,
			"image", imageRef,
		)
		return Result{}, fmt.Errorf("verification gate: %w", err)
	}

	if result.Verified {
		logger.Info("VerificationGate: PASS",
			"promotion", req.Promotion.Name,
			"image", imageRef,
			"signatures", result.Signatures,
		)
		return Result{Passed: true, Message: result.Message}, nil
	}

	logger.Info("VerificationGate: FAIL", "promotion", req.Promotion.Name, "image", imageRef)
	return Result{
		Passed:     false,
		Message:    fmt.Sprintf("signature verification failed: %s", result.Message),
		RetryAfter: "0",
	}, nil
}

// buildVerifyRequest constructs the VerifyRequest based on the policy's CosignPolicy.
// Falls back to default keyless when no policy is set.
func (g *VerificationGate) buildVerifyRequest(
	ctx context.Context,
	policy *kaprov1alpha1.PromotionPolicy,
	imageRef string,
) (verification.VerifyRequest, error) {
	base := verification.VerifyRequest{ImageRef: imageRef}

	// No policy or no cosign override → default keyless.
	if policy == nil ||
		policy.Spec.Gate.Verification == nil ||
		policy.Spec.Gate.Verification.CosignPolicy == nil {
		base.Keyless = &verification.KeylessConfig{}
		return base, nil
	}

	cp := policy.Spec.Gate.Verification.CosignPolicy

	if cp.Keyless != nil {
		base.Keyless = &verification.KeylessConfig{
			ExpectedIssuer:   cp.Keyless.Issuer,
			ExpectedIdentity: cp.Keyless.Subject,
			RekorURL:         cp.Keyless.RekorURL,
		}
		return base, nil
	}

	if cp.Key != nil {
		if g.KeyReader == nil {
			return base, fmt.Errorf("static key policy configured but no KeyReader injected")
		}
		ns := cp.Key.SecretRef.Namespace
		if ns == "" {
			ns = "kapro-system"
		}
		k := cp.Key.SecretRef.Key
		if k == "" {
			k = "cosign.pub"
		}
		pubKey, err := g.KeyReader.ReadKey(ctx, ns, cp.Key.SecretRef.Name, k)
		if err != nil {
			return base, fmt.Errorf("read cosign key secret %s/%s: %w", ns, cp.Key.SecretRef.Name, err)
		}
		base.Key = &verification.KeyConfig{PublicKey: pubKey}
		return base, nil
	}

	// CosignPolicy set but neither Keyless nor Key — fall back to default keyless.
	base.Keyless = &verification.KeylessConfig{}
	return base, nil
}

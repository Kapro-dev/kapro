package controller

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrremote "github.com/google/go-containerregistry/pkg/v1/remote"
	cosignlib "github.com/sigstore/cosign/v2/pkg/cosign"
	cosignoci "github.com/sigstore/cosign/v2/pkg/oci"
	ociremote "github.com/sigstore/cosign/v2/pkg/oci/remote"
	cosignsignature "github.com/sigstore/cosign/v2/pkg/signature"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// CosignReleaseTriggerVerifier verifies ReleaseTrigger OCI artifacts with cosign.
// Static public-key verification is implemented first; keyless policy is kept as
// an explicit unsupported branch so the trust-config surface can be extended.
type CosignReleaseTriggerVerifier struct {
	Client                client.Reader
	VerifyImageSignatures func(context.Context, name.Reference, *cosignlib.CheckOpts) ([]cosignoci.Signature, bool, error)
}

var _ ReleaseTriggerVerifier = &CosignReleaseTriggerVerifier{}
var _ ReleaseTriggerTrustConfigValidator = &CosignReleaseTriggerVerifier{}

func (v *CosignReleaseTriggerVerifier) ValidateTrustConfig(trigger *kaprov1alpha1.ReleaseTrigger) error {
	_, err := releaseTriggerCosignKeyRef(trigger)
	return err
}

func (v *CosignReleaseTriggerVerifier) Verify(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation) error {
	keyRef, err := releaseTriggerCosignKeyRef(trigger)
	if err != nil {
		return err
	}
	if v.Client == nil {
		return fmt.Errorf("kubernetes client is required for cosign key verification")
	}
	keyBytes, err := cosignPublicKey(ctx, v.Client, keyRef)
	if err != nil {
		return err
	}
	sigVerifier, err := cosignsignature.LoadPublicKeyRaw(keyBytes, crypto.SHA256)
	if err != nil {
		return fmt.Errorf("load cosign public key from Secret %s/%s key %q: %w", keyRef.Namespace, keyRef.Name, keyRef.Key, err)
	}

	ref, err := releaseTriggerArtifactReference(trigger, artifact)
	if err != nil {
		return err
	}
	registryClientOpts, err := releaseTriggerRegistryClientOptions(ctx, v.Client, trigger, ref.Context().RegistryStr())
	if err != nil {
		return err
	}
	opts := &cosignlib.CheckOpts{
		RegistryClientOpts: registryClientOpts,
		SigVerifier:        sigVerifier,
		ClaimVerifier:      cosignlib.SimpleClaimVerifier,
		IgnoreTlog:         true,
	}
	verify := v.VerifyImageSignatures
	if verify == nil {
		verify = cosignlib.VerifyImageSignatures
	}
	verified, _, err := verify(ctx, ref, opts)
	if err != nil {
		return fmt.Errorf("verify cosign signature for %s: %w", ref.Name(), err)
	}
	if len(verified) == 0 {
		return fmt.Errorf("verify cosign signature for %s: no valid signatures found", ref.Name())
	}
	return nil
}

func releaseTriggerCosignKeyRef(trigger *kaprov1alpha1.ReleaseTrigger) (kaprov1alpha1.CosignKeySecretRef, error) {
	if trigger.Spec.Verification == nil || trigger.Spec.Verification.CosignPolicy == nil {
		return kaprov1alpha1.CosignKeySecretRef{}, fmt.Errorf("spec.verification.cosignPolicy.key is required when source.oci.requireSignature is true")
	}
	policy := trigger.Spec.Verification.CosignPolicy
	switch {
	case policy.Key != nil && policy.Keyless != nil:
		return kaprov1alpha1.CosignKeySecretRef{}, fmt.Errorf("spec.verification.cosignPolicy must set only one of key or keyless")
	case policy.Keyless != nil:
		return kaprov1alpha1.CosignKeySecretRef{}, fmt.Errorf("spec.verification.cosignPolicy.keyless is not supported by ReleaseTrigger yet")
	case policy.Key == nil:
		return kaprov1alpha1.CosignKeySecretRef{}, fmt.Errorf("spec.verification.cosignPolicy.key is required when source.oci.requireSignature is true")
	}
	ref := policy.Key.SecretRef
	if ref.Name == "" {
		return kaprov1alpha1.CosignKeySecretRef{}, fmt.Errorf("spec.verification.cosignPolicy.key.secretRef.name is required")
	}
	if ref.Namespace == "" {
		ref.Namespace = "kapro-system"
	}
	if ref.Key == "" {
		ref.Key = "cosign.pub"
	}
	return ref, nil
}

func cosignPublicKey(ctx context.Context, c client.Reader, ref kaprov1alpha1.CosignKeySecretRef) ([]byte, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return nil, fmt.Errorf("get cosign public key Secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	keyBytes := secret.Data[ref.Key]
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("cosign public key Secret %s/%s missing data key %q", ref.Namespace, ref.Name, ref.Key)
	}
	return keyBytes, nil
}

func releaseTriggerArtifactReference(trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation) (name.Reference, error) {
	if trigger.Spec.Source.OCI == nil {
		return nil, fmt.Errorf("source.oci is required for cosign verification")
	}
	repo := strings.TrimPrefix(trigger.Spec.Source.OCI.Repository, "oci://")
	ref, err := name.ParseReference(repo + "@" + artifact.Digest)
	if err != nil {
		return nil, fmt.Errorf("parse OCI artifact reference %q: %w", repo+"@"+artifact.Digest, err)
	}
	return ref, nil
}

func releaseTriggerRegistryClientOptions(ctx context.Context, c client.Reader, trigger *kaprov1alpha1.ReleaseTrigger, registry string) ([]ociremote.Option, error) {
	if trigger.Spec.Source.OCI == nil || trigger.Spec.Source.OCI.SecretRef == nil || c == nil {
		return nil, nil
	}
	authenticator, err := releaseTriggerRegistryAuthenticator(ctx, c, *trigger.Spec.Source.OCI.SecretRef, registry)
	if err != nil {
		return nil, err
	}
	return []ociremote.Option{ociremote.WithRemoteOptions(ggcrremote.WithAuth(authenticator))}, nil
}

func releaseTriggerRegistryAuthenticator(ctx context.Context, c client.Reader, ref corev1.SecretReference, registry string) (authn.Authenticator, error) {
	if ref.Name == "" || ref.Namespace == "" {
		return nil, fmt.Errorf("OCI secretRef requires both name and namespace")
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return nil, fmt.Errorf("get OCI registry secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	if raw := secret.Data[corev1.DockerConfigJsonKey]; len(raw) > 0 {
		return authenticatorFromDockerConfig(raw, registry)
	}
	if username, password := string(secret.Data["username"]), string(secret.Data["password"]); username != "" || password != "" {
		return authn.FromConfig(authn.AuthConfig{Username: username, Password: password}), nil
	}
	if token := string(secret.Data["token"]); token != "" {
		return authn.FromConfig(authn.AuthConfig{RegistryToken: token}), nil
	}
	return nil, fmt.Errorf("OCI registry secret %s/%s has no usable credentials", ref.Namespace, ref.Name)
}

func authenticatorFromDockerConfig(raw []byte, registry string) (authn.Authenticator, error) {
	var cfg dockerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse docker config json: %w", err)
	}
	for host, entry := range cfg.Auths {
		if normalizeRegistryHost(host) != normalizeRegistryHost(registry) {
			continue
		}
		return authn.FromConfig(authn.AuthConfig{
			Username:      entry.Username,
			Password:      entry.Password,
			Auth:          entry.Auth,
			IdentityToken: entry.IdentityToken,
		}), nil
	}
	return nil, fmt.Errorf("docker config json does not contain credentials for %s", registry)
}

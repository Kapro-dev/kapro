// Package oci defines KRI — the Kapro Registry Interface.
//
// KRI is the OCI registry abstraction for artifact lifecycle operations.
// Kapro uses this to verify artifact existence, inspect metadata, and
// promote artifacts between registries (dev → staging → prod).
//
// Built-in implementations live in internal/oci/:
//   - oras/ — oras.land/oras-go/v2 with cloud Workload Identity auto-detection
//
// External implementations (Crane, Skopeo, Harbor-native, ECR-native) register via
// PluginRegistration CRD and communicate over proto/kapro/v1alpha1/oci.proto.
//
// The NopOCIService in this package returns sensible no-op responses for testing.
package oci

import "context"

// AuthConfig holds explicit credentials for non-cloud OCI registries.
// When nil, implementations should fall back to cloud Workload Identity auto-detection.
type AuthConfig struct {
	Username string
	Password string
	// Token is a bearer/access token (alternative to Username+Password).
	Token string
	// Insecure allows plain HTTP transport — dev/self-hosted registries only.
	Insecure bool
}

// ArtifactInfo is the value returned by Inspect.
type ArtifactInfo struct {
	Digest      string
	MediaType   string
	Tags        []string
	Annotations map[string]string
	SizeBytes   int64
}

// Service is KRI: the Kapro Registry Interface.
//
// Implementations must be safe for concurrent use.
type Service interface {
	// Exists reports whether the given reference (tag or digest) exists in repo.
	Exists(ctx context.Context, repo, reference string) (bool, error)

	// Inspect returns metadata for the artifact at the given reference.
	Inspect(ctx context.Context, repo, reference string) (*ArtifactInfo, error)

	// Tag applies a new tag to the artifact identified by srcDigest within the same repo.
	Tag(ctx context.Context, repo, srcDigest, newTag string) error

	// Copy copies an artifact from srcRepo/srcRef to dstRepo/dstTag.
	// Supports cross-registry promotion (dev-registry → prod-registry).
	Copy(ctx context.Context, srcRepo, srcRef, dstRepo, dstTag string) error

	// ListTags returns all tags for repo.
	ListTags(ctx context.Context, repo string) ([]string, error)
}

// NopOCIService returns safe no-op responses. Use in tests or when
// OCI inspection is not required.
type NopOCIService struct{}

func (NopOCIService) Exists(_ context.Context, _, _ string) (bool, error) { return true, nil }
func (NopOCIService) Inspect(_ context.Context, _, _ string) (*ArtifactInfo, error) {
	return &ArtifactInfo{Digest: "sha256:nop"}, nil
}
func (NopOCIService) Tag(_ context.Context, _, _, _ string) error        { return nil }
func (NopOCIService) Copy(_ context.Context, _, _, _, _ string) error    { return nil }
func (NopOCIService) ListTags(_ context.Context, _ string) ([]string, error) { return nil, nil }

// compile-time check
var _ Service = NopOCIService{}

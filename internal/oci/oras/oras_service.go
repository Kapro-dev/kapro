// Package oras provides the default OCIService backed by oras.land/oras-go/v2.
//
// Auth resolution order (per-repo, per-call):
//  1. Explicit AuthConfig (Username/Password or Token) — for JFrog, Harbor, Quay, GHCR, etc.
//  2. GCP Workload Identity (GKE) — detected via GOOGLE_APPLICATION_CREDENTIALS or GOOGLE_CLOUD_PROJECT.
//  3. Anonymous — fallback for public registries.
package oras

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"

	gcpauth "github.com/fluxcd/pkg/auth/gcp"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/oci"
)

// Service is the ORAS-backed OCIService. Zero value is usable (anonymous auth, plain HTTPS).
// Set Auth to supply static credentials; set PlainHTTP for HTTP-only registries (dev only).
type Service struct {
	// Auth holds static credentials. When nil, cloud WI auto-detection is attempted.
	Auth *oci.AuthConfig
	// PlainHTTP allows insecure HTTP transport. Never set true in production.
	PlainHTTP bool
}

// compile-time interface guard
var _ oci.Service = &Service{}

// Exists reports whether the given reference (tag or digest) exists in repo.
//
// repo must be a full repository path: registry.example.com/org/image.
func (s *Service) Exists(ctx context.Context, repo, reference string) (bool, error) {
	log := ctrllog.FromContext(ctx)

	r, err := s.newRepo(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("oci: exists: new repo %s: %w", repo, err)
	}

	_, err = r.Resolve(ctx, reference)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errdef.ErrNotFound) {
		return false, nil
	}

	// Registry may return 404-style HTTP errors wrapped differently.
	if isNotFound(err) {
		return false, nil
	}

	log.V(1).Info("oci: exists: resolve error", "repo", repo, "ref", reference, "err", err)
	return false, fmt.Errorf("oci: exists: resolve %s@%s: %w", repo, reference, err)
}

// Inspect returns metadata for the artifact at the given reference.
func (s *Service) Inspect(ctx context.Context, repo, reference string) (*oci.ArtifactInfo, error) {
	r, err := s.newRepo(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("oci: inspect: new repo %s: %w", repo, err)
	}

	desc, rc, err := r.FetchReference(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("oci: inspect: fetch %s@%s: %w", repo, reference, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("oci: inspect: read manifest body: %w", err)
	}

	// Parse annotations from the manifest JSON (best-effort: not all media types have them).
	annotations := parseAnnotations(desc.MediaType, body)

	return &oci.ArtifactInfo{
		Digest:      desc.Digest.String(),
		MediaType:   desc.MediaType,
		Annotations: annotations,
		SizeBytes:   desc.Size,
	}, nil
}

// Tag copies the artifact identified by srcDigest to newTag within the same repo.
func (s *Service) Tag(ctx context.Context, repo, srcDigest, newTag string) error {
	r, err := s.newRepo(ctx, repo)
	if err != nil {
		return fmt.Errorf("oci: tag: new repo %s: %w", repo, err)
	}

	// oras.Tag resolves srcDigest, fetches the manifest, and pushes it under newTag.
	if _, err := oraslib.Tag(ctx, r, srcDigest, newTag); err != nil {
		return fmt.Errorf("oci: tag: %s@%s → %s: %w", repo, srcDigest, newTag, err)
	}
	return nil
}

// Copy copies an artifact from srcRepo/srcRef to dstRepo/dstTag.
// Supports cross-registry promotion (different registries, orgs, or both).
func (s *Service) Copy(ctx context.Context, srcRepo, srcRef, dstRepo, dstTag string) error {
	src, err := s.newRepo(ctx, srcRepo)
	if err != nil {
		return fmt.Errorf("oci: copy: new src repo %s: %w", srcRepo, err)
	}

	dst, err := s.newRepo(ctx, dstRepo)
	if err != nil {
		return fmt.Errorf("oci: copy: new dst repo %s: %w", dstRepo, err)
	}

	if _, err := oraslib.Copy(ctx, src, srcRef, dst, dstTag, oraslib.DefaultCopyOptions); err != nil {
		return fmt.Errorf("oci: copy: %s@%s → %s:%s: %w", srcRepo, srcRef, dstRepo, dstTag, err)
	}
	return nil
}

// ListTags returns all tags for repo, in the order the registry returns them.
func (s *Service) ListTags(ctx context.Context, repo string) ([]string, error) {
	r, err := s.newRepo(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("oci: list-tags: new repo %s: %w", repo, err)
	}

	var tags []string
	if err := r.Tags(ctx, "", func(batch []string) error {
		tags = append(tags, batch...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("oci: list-tags: %s: %w", repo, err)
	}
	return tags, nil
}

// newRepo constructs an authenticated remote.Repository for the given full repository path.
func (s *Service) newRepo(ctx context.Context, repo string) (*remote.Repository, error) {
	r, err := remote.NewRepository(repo)
	if err != nil {
		return nil, err
	}

	plainHTTP := s.PlainHTTP || (s.Auth != nil && s.Auth.Insecure)
	r.PlainHTTP = plainHTTP

	r.Client = &orasauth.Client{
		Cache: orasauth.DefaultCache,
		Credential: func(ctx context.Context, hostport string) (orasauth.Credential, error) {
			return s.resolveCredential(ctx, hostport)
		},
	}
	return r, nil
}

// resolveCredential returns the best available credential for the given registry host:port.
//
// Resolution order:
//  1. Explicit static AuthConfig (Token or Username+Password).
//  2. GCP Workload Identity — attempted when GOOGLE_APPLICATION_CREDENTIALS or
//     GOOGLE_CLOUD_PROJECT is set, indicating a GCP/GKE environment.
//  3. Anonymous — orasauth.EmptyCredential.
func (s *Service) resolveCredential(ctx context.Context, hostport string) (orasauth.Credential, error) {
	log := ctrllog.FromContext(ctx)

	// 1. Static credentials take full precedence.
	if s.Auth != nil {
		if s.Auth.Token != "" {
			return orasauth.Credential{AccessToken: s.Auth.Token}, nil
		}
		if s.Auth.Username != "" {
			return orasauth.Credential{
				Username: s.Auth.Username,
				Password: s.Auth.Password,
			}, nil
		}
	}

	// 2. GCP Workload Identity (GKE IRSA-equivalent via metadata server or ADC).
	if isGCPEnvironment() {
		tok, err := gcpauth.Provider{}.NewControllerToken(ctx)
		if err == nil {
			gcpTok, ok := tok.(*gcpauth.Token)
			if ok && gcpTok.AccessToken != "" {
				log.V(1).Info("oci: using GCP WI token", "registry", hostport)
				return orasauth.Credential{AccessToken: gcpTok.AccessToken}, nil
			}
		}
		// Log but do not fail — fall through to anonymous.
		log.V(1).Info("oci: GCP WI token unavailable, falling back to anonymous", "registry", hostport, "err", err)
	}

	// 3. Anonymous.
	return orasauth.EmptyCredential, nil
}

// isGCPEnvironment returns true when the process is running in a GCP/GKE environment.
// Checks cheaply for well-known env vars before attempting any network calls.
func isGCPEnvironment() bool {
	// Application Default Credentials key file (sa key or gcloud auth).
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return true
	}
	// Set in GKE pods and Cloud Run when a project is configured.
	if os.Getenv("GOOGLE_CLOUD_PROJECT") != "" {
		return true
	}
	// Broad GCP indicator (e.g. service account email, Workload Identity annotation).
	if os.Getenv("GCLOUD_PROJECT") != "" {
		return true
	}
	return false
}

// parseAnnotations parses the OCI manifest JSON and returns its annotations map.
// Returns nil (not an error) for unsupported or empty annotations — callers should
// treat nil as an empty map.
func parseAnnotations(mediaType string, body []byte) map[string]string {
	// Both image manifests and OCI artifact manifests carry a top-level annotations field.
	// All known manifest types carry top-level annotations; index types may differ.
	// We attempt JSON unmarshal unconditionally below — the switch is a no-op guard
	// for documentation purposes only; remove duplicate constant to satisfy compiler.
	_ = mediaType

	var m struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	return m.Annotations
}

// isNotFound returns true for registry 404 / not-found style errors that ORAS
// may surface as wrapped HTTP errors rather than errdef.ErrNotFound.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "404") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "manifest unknown")
}

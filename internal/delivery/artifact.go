package delivery

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/opencontainers/go-digest"
)

// Format identifies how an artifact's contents should be rendered into
// Kubernetes objects.
type Format string

const (
	// FormatRawYAML — bare manifests, applied in lexical-order by filename.
	FormatRawYAML Format = "raw-yaml"
	// FormatHelm — a Helm chart whose Chart.yaml is at the artifact root.
	FormatHelm Format = "helm"
	// FormatKustomize — a Kustomize tree whose kustomization.yaml is at the root.
	FormatKustomize Format = "kustomize"
)

// ArtifactRef identifies one OCI artifact to pull.
//
// Repository is the bare repo path: "registry.example.com/path/repo" (no
// "oci://" scheme, no tag). Tag is mutable; Digest, when set, pins exactly
// to immutable content. When both are set, Digest wins.
//
// Authn captures the credential strategy:
//   - AuthAnonymous: no credentials sent. Use for public registries.
//   - AuthBearer:    "oauth2accesstoken" / static-token pair. Used by GCP WI
//     and ambient Azure / AWS adapters that materialise a short-lived bearer.
//   - AuthDockerConfig: read a Docker config.json from the local filesystem
//     (typical for retail-store environments with a baked-in pull secret).
type ArtifactRef struct {
	Repository string
	Tag        string
	Digest     string
	Authn      Authn
}

// Authn names the credential-resolution strategy for an ArtifactRef.
type Authn struct {
	Mode AuthMode
	// Token is consulted when Mode == AuthBearer.
	Token string
	// DockerConfigPath is consulted when Mode == AuthDockerConfig.
	DockerConfigPath string
}

// AuthMode classifies credential resolution.
type AuthMode string

const (
	AuthAnonymous    AuthMode = "anonymous"
	AuthBearer       AuthMode = "bearer"
	AuthDockerConfig AuthMode = "docker-config"
)

// String returns a human-readable form like "registry/repo:tag@sha256:abc".
// Stable: never includes credentials.
func (a ArtifactRef) String() string {
	switch {
	case a.Repository == "":
		return "<empty>"
	case a.Digest != "" && a.Tag != "":
		return fmt.Sprintf("%s:%s@%s", a.Repository, a.Tag, a.Digest)
	case a.Digest != "":
		return fmt.Sprintf("%s@%s", a.Repository, a.Digest)
	case a.Tag != "":
		return fmt.Sprintf("%s:%s", a.Repository, a.Tag)
	default:
		return a.Repository
	}
}

// PulledArtifact is the in-memory result of a successful Pull.
//
// FS is a read-only filesystem rooted at the artifact's tar layer root.
// Digest is the manifest digest of the pulled artifact (stable across pulls
// of the same content even if Tag was used to fetch it).
type PulledArtifact struct {
	FS     fs.FS
	Digest digest.Digest
	// MediaType is the layer media type (e.g.
	// "application/vnd.cncf.flux.content.v1.tar+gzip"). Provided as a hint
	// to Detect; if empty, Detect falls back to fs-structure heuristics.
	MediaType string
}

// Puller fetches OCI artifacts.
type Puller interface {
	Pull(ctx context.Context, ref ArtifactRef) (*PulledArtifact, error)
}

// Renderer turns a pulled artifact's filesystem into a list of Kubernetes
// objects ready for the two-phase apply engine.
type Renderer interface {
	Render(ctx context.Context, pa *PulledArtifact, opts RenderOptions) (RenderedManifests, error)
}

// RenderOptions controls handler-specific behaviour at render time. Empty
// values mean "use the handler default".
type RenderOptions struct {
	// HelmReleaseName overrides the Helm release name (default: app key).
	HelmReleaseName string
	// HelmNamespace overrides the Helm release namespace.
	HelmNamespace string
	// HelmValues is an optional values overlay (parsed YAML object) merged
	// after the chart's own values.yaml.
	HelmValues map[string]any
	// KustomizeDir is the relative path of the kustomization root within the
	// artifact when not at the artifact root. Defaults to ".".
	KustomizeDir string
}

// RenderedManifests is the parsed-and-typed output of a Renderer pass.
// Objects retain their declared order; identical (gvk, namespace, name)
// tuples have already been deduplicated by the renderer.
type RenderedManifests struct {
	Objects []*Object
	// Format is the detected/declared format that produced these objects.
	Format Format
}

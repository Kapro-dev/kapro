// Package outbound implements the first-party "outbound-agent" spoke
// Provider: the kapro-cluster-controller pulls OCI artifacts directly from a
// registry and applies them via the two-phase apply engine. The name pins
// the architectural role — the spoke makes outbound HTTPS calls to the OCI
// registry, with no inbound webhook from the hub.
package outbound

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/delivery"
	"kapro.io/kapro/pkg/spokeprovider"
)

// RefResolver translates a ReconcileRequest into a delivery.ArtifactRef.
// Decoupled from Provider so out-of-tree builds can plug a custom resolver
// (e.g. one that issues short-lived GCP WIF tokens on the fly).
type RefResolver interface {
	Resolve(ctx context.Context, req spokeprovider.ReconcileRequest) (delivery.ArtifactRef, error)
}

// Recognised parameter keys on SubstrateProfile.Spec.Parameters /
// FleetCluster.Spec.Delivery.Parameters. Kept exported so tests and the
// scaffolding CLI can reference them by name rather than re-typing strings.
const (
	// ParamRepository is the bare OCI repo path (no scheme, no tag).
	// Tokens {appKey} and {version} are expanded.
	ParamRepository = "repository"
	// ParamTag is the tag template (default "{version}").
	ParamTag = "tag"
	// ParamAuth selects the credential mode: "anonymous" (default),
	// "bearer", or "docker-config".
	ParamAuth = "auth"
	// ParamTokenSecretRef is "namespace/name" of a Secret containing key
	// "token" (used when auth=bearer).
	ParamTokenSecretRef = "tokenSecretRef"
	// ParamTokenSecretKey overrides the Secret key (default "token").
	ParamTokenSecretKey = "tokenSecretKey"
	// ParamDockerConfigPath is the on-disk Docker config.json path (used
	// when auth=docker-config). Typically mounted into the spoke pod from
	// a Secret.
	ParamDockerConfigPath = "dockerConfigPath"
)

// ParametersRefResolver resolves an ArtifactRef from the merged Parameters
// map. The Local client is used to read bearer-token Secrets when
// ParamAuth=="bearer".
//
// Token resolution happens on every Reconcile call — no caching — so a
// rotated Secret is picked up on the next loop tick without restart.
type ParametersRefResolver struct {
	Local client.Client
}

// Resolve constructs a delivery.ArtifactRef from req.Parameters.
//
// Required parameters: repository.
// Defaults: tag="{version}", auth="anonymous".
func (r *ParametersRefResolver) Resolve(ctx context.Context, req spokeprovider.ReconcileRequest) (delivery.ArtifactRef, error) {
	if req.DesiredVersion == "" {
		return delivery.ArtifactRef{}, fmt.Errorf("desired version is empty")
	}
	repoTpl := strings.TrimSpace(req.Parameters[ParamRepository])
	if repoTpl == "" {
		return delivery.ArtifactRef{}, fmt.Errorf("parameter %q required for substrate kind=oci", ParamRepository)
	}
	tagTpl := req.Parameters[ParamTag]
	if tagTpl == "" {
		tagTpl = "{version}"
	}

	repo, err := expandTemplate(repoTpl, req.AppKey, req.DesiredVersion)
	if err != nil {
		return delivery.ArtifactRef{}, fmt.Errorf("expand %s: %w", ParamRepository, err)
	}
	tag, err := expandTemplate(tagTpl, req.AppKey, req.DesiredVersion)
	if err != nil {
		return delivery.ArtifactRef{}, fmt.Errorf("expand %s: %w", ParamTag, err)
	}

	ref := delivery.ArtifactRef{
		Repository: repo,
		Tag:        tag,
	}

	authMode := strings.TrimSpace(strings.ToLower(req.Parameters[ParamAuth]))
	switch authMode {
	case "", "anonymous":
		ref.Authn = delivery.Authn{Mode: delivery.AuthAnonymous}
	case "bearer":
		token, err := r.resolveBearerToken(ctx, req)
		if err != nil {
			return delivery.ArtifactRef{}, err
		}
		ref.Authn = delivery.Authn{Mode: delivery.AuthBearer, Token: token}
	case "docker-config":
		path := strings.TrimSpace(req.Parameters[ParamDockerConfigPath])
		if path == "" {
			return delivery.ArtifactRef{}, fmt.Errorf("parameter %q required when auth=docker-config", ParamDockerConfigPath)
		}
		ref.Authn = delivery.Authn{Mode: delivery.AuthDockerConfig, DockerConfigPath: path}
	default:
		return delivery.ArtifactRef{}, fmt.Errorf("unsupported %s mode %q (anonymous|bearer|docker-config)", ParamAuth, authMode)
	}

	return ref, nil
}

func (r *ParametersRefResolver) resolveBearerToken(ctx context.Context, req spokeprovider.ReconcileRequest) (string, error) {
	secretRef := strings.TrimSpace(req.Parameters[ParamTokenSecretRef])
	if secretRef == "" {
		return "", fmt.Errorf("parameter %q required when auth=bearer", ParamTokenSecretRef)
	}
	ns, name, ok := splitNamespacedName(secretRef)
	if !ok {
		return "", fmt.Errorf("parameter %q must be in the form namespace/name (got %q)", ParamTokenSecretRef, secretRef)
	}
	if r.Local == nil {
		return "", fmt.Errorf("local client is nil; cannot read Secret %s/%s", ns, name)
	}
	var s corev1.Secret
	if err := r.Local.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return "", fmt.Errorf("read bearer-token Secret %s/%s: %w", ns, name, err)
	}
	key := strings.TrimSpace(req.Parameters[ParamTokenSecretKey])
	if key == "" {
		key = "token"
	}
	raw, ok := s.Data[key]
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf("secret %s/%s has no non-empty %q key", ns, name, key)
	}
	return strings.TrimSpace(string(raw)), nil
}

// expandTemplate replaces {appKey} and {version} tokens. Unknown {tokens} are
// rejected to surface typos at the first reconcile rather than silently
// producing a bogus reference.
func expandTemplate(tpl, appKey, version string) (string, error) {
	out := tpl
	out = strings.ReplaceAll(out, "{appKey}", appKey)
	out = strings.ReplaceAll(out, "{version}", version)
	if i := strings.Index(out, "{"); i >= 0 {
		end := strings.Index(out[i:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated %q in template %q", "{", tpl)
		}
		return "", fmt.Errorf("unknown template token %q in %q (supported: {appKey}, {version})",
			out[i:i+end+1], tpl)
	}
	return out, nil
}

// splitNamespacedName parses "namespace/name". Returns ok=false unless the
// input contains EXACTLY one slash and both segments are non-empty.
// Kubernetes names cannot contain '/', so values like "ns/name/extra" are
// typos — we reject them up-front with the clear "namespace/name" error
// message instead of letting the apiserver surface a confusing one later.
func splitNamespacedName(s string) (ns, name string, ok bool) {
	ns, name, ok = strings.Cut(s, "/")
	if !ok {
		return "", "", false
	}
	if ns == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return ns, name, true
}

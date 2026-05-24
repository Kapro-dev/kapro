package flux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

const (
	defaultFluxNamespace = "flux-system"

	paramOCIRepositoryName      = "ociRepositoryName"
	paramOCIRepositoryNamespace = "ociRepositoryNamespace"
	paramHelmReleaseName        = "helmReleaseName"
	paramHelmReleaseNamespace   = "helmReleaseNamespace"
)

// Flux source-controller and helm-controller GVKs. Held as constants to make
// it explicit that this package does not depend on Flux Go types — only on
// well-known group/version/kind triples.
var (
	ociRepositoryGVK = schema.GroupVersionKind{
		Group:   "source.toolkit.fluxcd.io",
		Version: "v1beta2",
		Kind:    "OCIRepository",
	}
	helmReleaseGVK = schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	}
)

// Provider is the spoke-side observer for SubstrateKindFlux. It is
// read-only: it never patches Flux objects. Mutation happens via the
// hub-side fluxoperator actuator (internal/actuator/fluxoperator) which
// already runs against the hub-held FluxInstance or against a hub-managed
// flux-operator on the spoke.
type Provider struct {
	// Local is the controller-runtime client for the spoke cluster.
	Local client.Client
	// Now is injected so tests can pin timestamps. Defaults to time.Now.
	Now func() time.Time
}

// NewProvider returns a Provider wired with the supplied spoke client.
func NewProvider(spoke client.Client) *Provider {
	return &Provider{Local: spoke}
}

// SubstrateKind returns SubstrateKindFlux. The Registry key — not this method —
// determines dispatch.
func (p *Provider) SubstrateKind() kaprov1alpha1.SubstrateKind {
	return kaprov1alpha1.SubstrateKindFlux
}

func (p *Provider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		ContractVersion:   spokeprovider.ContractVersionV1Alpha1,
		SubstrateKind:     kaprov1alpha1.SubstrateKindFlux,
		SupportsReconcile: true,
		SupportsObserve:   true,
	}
}

// Reconcile observes local Flux state for the request's app and returns
// a populated ReconcileResult. Never panics; never mutates Flux state.
func (p *Provider) Reconcile(ctx context.Context, req spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	out := spokeprovider.ReconcileResult{LastAttemptedAt: now()}

	if req.Cluster != nil && req.Cluster.Spec.Suspend {
		out.Phase = kaprov1alpha1.DeliveryPhaseSkipped
		return out
	}
	if p.Local == nil {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = errors.New("Provider.Local is nil")
		return out
	}
	if req.DesiredVersion == "" {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = errors.New("DesiredVersion is empty")
		return out
	}

	repoName := req.Parameters[paramOCIRepositoryName]
	if repoName == "" {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = fmt.Errorf("missing required parameter %q", paramOCIRepositoryName)
		return out
	}
	repoNS := req.Parameters[paramOCIRepositoryNamespace]
	if repoNS == "" {
		repoNS = defaultFluxNamespace
	}

	repo, repoErr := p.getUnstructured(ctx, ociRepositoryGVK, repoNS, repoName)
	if repoErr != nil {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = fmt.Errorf("get OCIRepository %s/%s: %w", repoNS, repoName, repoErr)
		return out
	}

	revision, hasArtifact := unstructuredString(repo.Object, "status", "artifact", "revision")
	digest, _ := unstructuredString(repo.Object, "status", "artifact", "digest")
	if !hasArtifact {
		out.Phase = kaprov1alpha1.DeliveryPhasePulling
		return out
	}

	if msg, ok := readyConditionFalse(repo.Object); ok {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = fmt.Errorf("OCIRepository %s/%s not Ready: %s", repoNS, repoName, msg)
		out.ObservedDigest = digest
		out.Format = "flux"
		return out
	}

	out.Format = "flux"
	out.ObservedDigest = digest

	if !revisionMatches(revision, req.DesiredVersion) {
		out.Phase = kaprov1alpha1.DeliveryPhasePulling
		return out
	}

	// gate review fix: revision matched, but Flux may still be reconciling
	// (Ready=Unknown or missing). The previous code treated "not False" as
	// good enough and could report Converged before Flux finished fetching.
	// Now require an explicit Ready=True before considering the OCI side
	// converged; anything else is still Pulling.
	if !isReady(repo.Object) {
		out.Phase = kaprov1alpha1.DeliveryPhasePulling
		return out
	}

	// OCIRepository is at the desired revision AND Ready=True. If a
	// HelmRelease is configured, gate on its Ready condition too;
	// otherwise mark Converged.
	hrName := req.Parameters[paramHelmReleaseName]
	if hrName == "" {
		out.Phase = kaprov1alpha1.DeliveryPhaseConverged
		out.LastAppliedAt = now()
		return out
	}
	hrNS := req.Parameters[paramHelmReleaseNamespace]
	if hrNS == "" {
		hrNS = repoNS
	}
	hr, hrErr := p.getUnstructured(ctx, helmReleaseGVK, hrNS, hrName)
	if hrErr != nil {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = fmt.Errorf("get HelmRelease %s/%s: %w", hrNS, hrName, hrErr)
		return out
	}
	if msg, ok := readyConditionFalse(hr.Object); ok {
		out.Phase = kaprov1alpha1.DeliveryPhaseFailed
		out.Err = fmt.Errorf("HelmRelease %s/%s not Ready: %s", hrNS, hrName, msg)
		return out
	}
	if isReady(hr.Object) {
		out.Phase = kaprov1alpha1.DeliveryPhaseConverged
		out.LastAppliedAt = now()
		return out
	}
	// HelmRelease present, Ready not yet True and not False — Flux is
	// still rolling out the new revision.
	out.Phase = kaprov1alpha1.DeliveryPhaseApplying
	return out
}

// getUnstructured fetches a typed-by-GVK object from the spoke cluster.
// Errors are returned wrapped with %w so callers can still use
// apierrors.IsNotFound to distinguish "Flux not installed yet" from other
// failures, while keeping the original apierror context for debugging.
func (p *Provider) getUnstructured(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := p.Local.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, u); err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", gvk.Kind, namespace, name, err)
	}
	return u, nil
}

// revisionMatches compares the Flux artifact revision (which is
// "tag@digest" for OCI sources or just "tag" for some substrates) to the
// Kapro-recorded DesiredVersion (which is the upstream tag).
//
// We accept a match when:
//   - desired equals the revision exactly, or
//   - desired equals the prefix-before-@ of revision, or
//   - desired equals the digest portion of revision.
func revisionMatches(revision, desired string) bool {
	revision = strings.TrimSpace(revision)
	desired = strings.TrimSpace(desired)
	if revision == "" || desired == "" {
		return false
	}
	if revision == desired {
		return true
	}
	if idx := strings.IndexByte(revision, '@'); idx > 0 {
		if revision[:idx] == desired {
			return true
		}
		if strings.TrimPrefix(revision[idx+1:], "sha256:") == strings.TrimPrefix(desired, "sha256:") {
			return true
		}
	}
	return false
}

// readyConditionFalse returns the message of a Ready=False condition,
// or (false, "") if the object has no Ready condition or Ready is not False.
func readyConditionFalse(obj map[string]any) (string, bool) {
	conds, ok := unstructuredSlice(obj, "status", "conditions")
	if !ok {
		return "", false
	}
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := c["type"].(string)
		if typ != "Ready" {
			continue
		}
		status, _ := c["status"].(string)
		if status == "False" {
			msg, _ := c["message"].(string)
			if msg == "" {
				msg = "Ready=False"
			}
			return msg, true
		}
		return "", false
	}
	return "", false
}

// isReady returns true when the object has a Ready=True condition.
func isReady(obj map[string]any) bool {
	conds, ok := unstructuredSlice(obj, "status", "conditions")
	if !ok {
		return false
	}
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := c["type"].(string)
		if typ != "Ready" {
			continue
		}
		status, _ := c["status"].(string)
		return status == "True"
	}
	return false
}

func unstructuredString(obj map[string]any, path ...string) (string, bool) {
	cur := any(obj)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = m[key]
	}
	s, ok := cur.(string)
	return s, ok
}

func unstructuredSlice(obj map[string]any, path ...string) ([]any, bool) {
	cur := any(obj)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[key]
	}
	s, ok := cur.([]any)
	return s, ok
}

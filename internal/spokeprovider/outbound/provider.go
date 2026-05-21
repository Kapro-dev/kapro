package outbound

import (
	"context"
	"errors"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/delivery"
	"kapro.io/kapro/pkg/spokeprovider"
)

// Provider is the first-party spoke Provider for BackendDriverOCI. It wraps
// internal/delivery.Delivery (the OCI Delivery Core from PR-4) and exposes
// the spokeprovider.Provider contract the delivery loop dispatches against.
type Provider struct {
	// Delivery performs the actual Pull → Detect → Render → Apply chain.
	// Required.
	Delivery *delivery.Delivery
	// RefResolver translates ReconcileRequest into delivery.ArtifactRef.
	// Defaults to a ParametersRefResolver backed by Local when nil.
	RefResolver RefResolver
	// Local is the spoke-cluster client used by the default RefResolver to
	// read bearer-token Secrets. Ignored when RefResolver is set explicitly.
	Local client.Client
	// Now is injected so tests can pin timestamps. Defaults to time.Now.
	Now func() time.Time
}

// NewProvider returns a Provider wired with the production delivery engine
// and a ParametersRefResolver over the supplied spoke client.
func NewProvider(spoke client.Client) *Provider {
	return &Provider{
		Delivery: delivery.NewDelivery(spoke),
		Local:    spoke,
	}
}

// Driver returns BackendDriverOCI. The Registry key — not this method — is
// what determines dispatch.
func (p *Provider) Driver() kaprov1alpha2.BackendDriver { return kaprov1alpha2.BackendDriverOCI }

// Reconcile resolves the OCI ArtifactRef from request parameters and
// delegates to internal/delivery.Delivery. Returns a populated
// spokeprovider.ReconcileResult on every code path — never panics.
//
// Phase semantics:
//   - Cluster.Spec.Suspend == true                  → Phase=Skipped, no work.
//   - Parameter resolution failure                  → Phase=Failed.
//   - Inner delivery returns Failed                 → forwarded.
//   - Inner delivery returns Converged              → forwarded (LastAppliedAt set).
func (p *Provider) Reconcile(ctx context.Context, req spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	out := spokeprovider.ReconcileResult{LastAttemptedAt: now()}

	if req.Cluster != nil && req.Cluster.Spec.Suspend {
		out.Phase = kaprov1alpha2.DeliveryPhaseSkipped
		return out
	}
	if p.Delivery == nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = errors.New("Provider.Delivery is nil")
		return out
	}

	resolver := p.RefResolver
	if resolver == nil {
		resolver = &ParametersRefResolver{Local: p.Local}
	}
	ref, err := resolver.Resolve(ctx, req)
	if err != nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = err
		return out
	}

	inner := p.Delivery.Reconcile(ctx, delivery.ReconcileRequest{
		App: req.AppKey,
		Ref: ref,
	})

	out.Phase = kaprov1alpha2.DeliveryPhase(inner.Phase)
	out.Format = string(inner.Format)
	out.ObservedDigest = inner.ObservedDigest
	out.AppliedObjects = inner.AppliedObjects
	out.Err = inner.Err
	if !inner.LastAttemptedAt.IsZero() {
		out.LastAttemptedAt = inner.LastAttemptedAt
	}
	if !inner.LastAppliedAt.IsZero() {
		out.LastAppliedAt = inner.LastAppliedAt
	}
	return out
}

package delivery

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Delivery is the single entry point spoke code calls per (app, version)
// reconcile. It chains:
//
//	Pull → Detect → Render → Apply
//
// and returns a ReconcileResult the caller writes to
// FleetCluster.status.delivery[app].
//
// Delivery is dependency-injected end-to-end so tests can swap in fakes:
//   - Puller: replaced with an in-memory OCI store in unit tests.
//   - Renderers: the map is keyed by Format; tests may register a stub.
//   - Engine: a controller-runtime fake client backs the SSA calls.
type Delivery struct {
	Puller    Puller
	Renderers map[Format]Renderer
	Engine    *ApplyEngine
	// Now returns the current time; injected so tests can pin LastAppliedAt.
	Now func() time.Time
}

// NewDelivery returns a Delivery wired with the production puller, raw-YAML
// renderer, and an ApplyEngine over the supplied spoke client. Helm and
// Kustomize renderers are registered when their respective handlers land in
// follow-up commits within PR-4.
func NewDelivery(spoke client.Client) *Delivery {
	return &Delivery{
		Puller: &OCIPuller{},
		Renderers: map[Format]Renderer{
			FormatRawYAML: RawYAMLRenderer{},
		},
		Engine: &ApplyEngine{Client: spoke},
		Now:    time.Now,
	}
}

// ReconcileRequest carries the inputs for one delivery reconcile pass.
type ReconcileRequest struct {
	// App is the FleetCluster.spec.desiredVersions key the reconcile targets.
	App string
	// Ref identifies the OCI artifact to pull.
	Ref ArtifactRef
	// Options is forwarded to the format-specific renderer; safe to leave empty.
	Options RenderOptions
}

// ReconcileResult captures everything the caller needs to write into
// FleetCluster.status.delivery[app]: phase, observed digest, error,
// timestamps, applied-object count, and the detected artifact format.
//
// All fields are populated even on failure paths so the caller can write a
// single coherent status update.
type ReconcileResult struct {
	App             string
	Phase           string
	Format          Format
	ObservedDigest  string
	AppliedObjects  int32
	LastAttemptedAt time.Time
	LastAppliedAt   time.Time
	Err             error
}

// Reconcile runs one Pull→Render→Apply pass for the requested (app, ref).
//
// Returned ReconcileResult.Phase is terminal — exactly one of
// DeliveryPhaseConverged (full commit succeeded) or DeliveryPhaseFailed
// (any step errored). Reconcile is synchronous; by the time it returns the
// pass is over, so intermediate phases (Pulling, Staging, Applying) are not
// observable on the result. The intermediate phases exist for callers that
// want to advertise in-flight progress in status.delivery — typically by
// writing the phase to status before/after each step in their own loop. A
// follow-up commit adds that streaming wrapper; this function deliberately
// stays linear so logs and metrics see a stable terminal phase.
//
// Pending and Skipped are owned by the caller's outer loop:
//   - Pending: desired version recorded but the caller hasn't started yet.
//   - Skipped: caller short-circuited (e.g. spec.suspend=true).
func (d *Delivery) Reconcile(ctx context.Context, req ReconcileRequest) ReconcileResult {
	if d == nil {
		return ReconcileResult{App: req.App, Phase: string(kaprov1alpha1.DeliveryPhaseFailed),
			Err: fmt.Errorf("nil Delivery")}
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	out := ReconcileResult{App: req.App, LastAttemptedAt: now(), Phase: string(kaprov1alpha1.DeliveryPhaseFailed)}

	// Guard against partially-constructed Delivery: a nil Puller / Engine /
	// Renderers map would panic on dereference further down. Surface the
	// misconfiguration as a Failed result so the caller can write it to
	// status.delivery and log it, rather than crashing the spoke pod.
	switch {
	case d.Puller == nil:
		out.Err = fmt.Errorf("Delivery.Puller is nil")
		return out
	case d.Engine == nil:
		out.Err = fmt.Errorf("Delivery.Engine is nil")
		return out
	case d.Renderers == nil:
		out.Err = fmt.Errorf("Delivery.Renderers is nil")
		return out
	}

	// Pull.
	pa, err := d.Puller.Pull(ctx, req.Ref)
	if err != nil {
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = fmt.Errorf("pull %s: %w", req.Ref.String(), err)
		return out
	}
	out.ObservedDigest = pa.Digest.String()

	// Detect.
	format, err := DetectFormat(pa)
	if err != nil {
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = fmt.Errorf("detect format: %w", err)
		return out
	}
	out.Format = format

	// Render.
	renderer, ok := d.Renderers[format]
	if !ok {
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = fmt.Errorf("format %q not supported by this build", format)
		return out
	}
	rendered, err := renderer.Render(ctx, pa, req.Options)
	if err != nil {
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = fmt.Errorf("render %s: %w", format, err)
		return out
	}
	if len(rendered.Objects) == 0 {
		// An artifact that renders to zero objects is suspicious enough to
		// surface as Failed: the OCI bundle is well-formed but contains
		// nothing the spoke can apply, which is almost always a bug in the
		// promotion pipeline. Cheap to spot here, painful to debug later.
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = fmt.Errorf("render %s: zero objects", format)
		return out
	}

	// Apply (two-phase).
	res, err := d.Engine.Apply(ctx, rendered.Objects)
	out.AppliedObjects = int32(res.Committed)
	if err != nil {
		out.Phase = string(kaprov1alpha1.DeliveryPhaseFailed)
		out.Err = err
		return out
	}
	out.Phase = string(kaprov1alpha1.DeliveryPhaseConverged)
	out.LastAppliedAt = now()
	return out
}

// RegisterRenderer adds (or replaces) a Renderer for the given format.
// Used by tests and by follow-up commits that add Helm / Kustomize support
// without forcing the spoke binary to import every renderer at build time.
func (d *Delivery) RegisterRenderer(format Format, r Renderer) {
	if d == nil {
		return
	}
	if d.Renderers == nil {
		d.Renderers = map[Format]Renderer{}
	}
	d.Renderers[format] = r
}

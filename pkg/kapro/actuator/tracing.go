package actuator

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

const actuatorTracerName = "kapro.io/kapro/pkg/kapro/actuator"

type tracedActuator struct {
	name         string
	delegate     Actuator
	capabilities Capabilities
}

type tracedSubstrateObjectReporter struct {
	*tracedActuator
	reporter SubstrateObjectReporter
}

type tracedTwoPhaseActuator struct {
	*tracedActuator
	staging TwoPhaseStaging
}

type tracedSubstrateObjectReporterTwoPhase struct {
	*tracedActuator
	reporter SubstrateObjectReporter
	staging  TwoPhaseStaging
}

// WithTracing wraps an actuator with the standard Kapro OpenTelemetry span
// contract. Registry.Resolve applies this automatically; SDK users can call it
// directly when they invoke an actuator outside a Registry.
func WithTracing(name string, a Actuator) Actuator {
	return withTracingCapabilities(name, a, Capabilities{})
}

func withTracingCapabilities(name string, a Actuator, capabilities Capabilities) Actuator {
	if a == nil {
		return nil
	}
	if _, ok := a.(*tracedActuator); ok {
		return a
	}
	if _, ok := a.(*tracedSubstrateObjectReporter); ok {
		return a
	}
	if _, ok := a.(*tracedTwoPhaseActuator); ok {
		return a
	}
	if _, ok := a.(*tracedSubstrateObjectReporterTwoPhase); ok {
		return a
	}
	if substrate, ok := a.(Substrate); ok && capabilitiesEmpty(capabilities) {
		capabilities = substrate.Capabilities()
	}
	traced := &tracedActuator{
		name:         name,
		delegate:     a,
		capabilities: capabilities.Normalize(),
	}
	reporter, hasReporter := a.(SubstrateObjectReporter)
	staging, hasStaging := a.(TwoPhaseStaging)
	if hasReporter && hasStaging {
		return &tracedSubstrateObjectReporterTwoPhase{
			tracedActuator: traced,
			reporter:       reporter,
			staging:        staging,
		}
	}
	if hasStaging {
		return &tracedTwoPhaseActuator{tracedActuator: traced, staging: staging}
	}
	if reporter, ok := a.(SubstrateObjectReporter); ok {
		return &tracedSubstrateObjectReporter{tracedActuator: traced, reporter: reporter}
	}
	return traced
}

func (a *tracedActuator) Apply(ctx context.Context, req ApplyRequest) error {
	ctx, span := a.start(ctx, "kapro.actuator.apply",
		attribute.String("kapro.cluster", clusterName(req.Cluster)),
		attribute.String("kapro.app_key", req.AppKey),
		attribute.String("kapro.version", req.Version),
		attribute.String("kapro.previous_version", req.PreviousVersion),
	)
	defer span.End()
	err := a.delegate.Apply(ctx, req)
	recordActuatorError(span, err)
	return err
}

func (a *tracedActuator) IsConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, version, appKey string) (bool, error) {
	ctx, span := a.start(ctx, "kapro.actuator.observe",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.String("kapro.app_key", appKey),
		attribute.String("kapro.version", version),
	)
	defer span.End()
	converged, err := a.delegate.IsConverged(ctx, cluster, version, appKey)
	span.SetAttributes(attribute.Bool("kapro.actuator.converged", converged))
	recordActuatorError(span, err)
	return converged, err
}

func (a *tracedActuator) Rollback(ctx context.Context, cluster *kaprov1alpha1.Cluster, previousVersion, appKey string) error {
	ctx, span := a.start(ctx, "kapro.actuator.rollback",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.String("kapro.app_key", appKey),
		attribute.String("kapro.previous_version", previousVersion),
	)
	defer span.End()
	err := a.delegate.Rollback(ctx, cluster, previousVersion, appKey)
	recordActuatorError(span, err)
	return err
}

func (a *tracedActuator) ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error) {
	ctx, span := a.start(ctx, "kapro.actuator.apply_delta",
		attribute.String("kapro.cluster", clusterName(req.Cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(req.DesiredVersions)),
	)
	defer span.End()
	applied, err := a.delegate.ApplyDelta(ctx, req)
	span.SetAttributes(attribute.Int("kapro.actuator.applied", applied))
	recordActuatorError(span, err)
	return applied, err
}

func (a *tracedActuator) IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) (bool, error) {
	ctx, span := a.start(ctx, "kapro.actuator.observe_all",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(desiredVersions)),
	)
	defer span.End()
	converged, err := a.delegate.IsAllConverged(ctx, cluster, desiredVersions)
	span.SetAttributes(attribute.Bool("kapro.actuator.converged", converged))
	recordActuatorError(span, err)
	return converged, err
}

func (a *tracedActuator) Capabilities() Capabilities {
	if a == nil {
		return Capabilities{}.Normalize()
	}
	return a.capabilities.Normalize()
}

func (a *tracedSubstrateObjectReporter) SubstrateObjects(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) ([]kaprov1alpha1.SubstrateObjectStatus, error) {
	ctx, span := a.start(ctx, "kapro.actuator.substrate_objects",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(desiredVersions)),
	)
	defer span.End()
	objects, err := a.reporter.SubstrateObjects(ctx, cluster, desiredVersions)
	span.SetAttributes(attribute.Int("kapro.actuator.substrate_objects", len(objects)))
	recordActuatorError(span, err)
	return objects, err
}

func (a *tracedTwoPhaseActuator) Prepare(ctx context.Context, req StageRequest) (StageHandle, error) {
	return a.prepare(ctx, req, a.staging)
}

func (a *tracedTwoPhaseActuator) Commit(ctx context.Context, handle StageHandle) (CommitResult, error) {
	return a.commit(ctx, handle, a.staging)
}

func (a *tracedTwoPhaseActuator) Discard(ctx context.Context, handle StageHandle) error {
	return a.discard(ctx, handle, a.staging)
}

func (a *tracedSubstrateObjectReporterTwoPhase) SubstrateObjects(ctx context.Context, cluster *kaprov1alpha1.Cluster, desiredVersions map[string]string) ([]kaprov1alpha1.SubstrateObjectStatus, error) {
	ctx, span := a.start(ctx, "kapro.actuator.substrate_objects",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(desiredVersions)),
	)
	defer span.End()
	objects, err := a.reporter.SubstrateObjects(ctx, cluster, desiredVersions)
	span.SetAttributes(attribute.Int("kapro.actuator.substrate_objects", len(objects)))
	recordActuatorError(span, err)
	return objects, err
}

func (a *tracedSubstrateObjectReporterTwoPhase) Prepare(ctx context.Context, req StageRequest) (StageHandle, error) {
	return a.prepare(ctx, req, a.staging)
}

func (a *tracedSubstrateObjectReporterTwoPhase) Commit(ctx context.Context, handle StageHandle) (CommitResult, error) {
	return a.commit(ctx, handle, a.staging)
}

func (a *tracedSubstrateObjectReporterTwoPhase) Discard(ctx context.Context, handle StageHandle) error {
	return a.discard(ctx, handle, a.staging)
}

func (a *tracedActuator) prepare(ctx context.Context, req StageRequest, staging TwoPhaseStaging) (StageHandle, error) {
	ctx, span := a.start(ctx, "kapro.actuator.prepare",
		attribute.String("kapro.cluster", clusterName(req.Cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(req.DesiredVersions)),
		attribute.Bool("kapro.actuator.dry_run", req.DryRun),
	)
	defer span.End()
	handle, err := staging.Prepare(ctx, req)
	span.SetAttributes(
		attribute.String("kapro.actuator.stage_handle", handle.ID),
		attribute.String("kapro.actuator.substrate", string(handle.Substrate)),
		attribute.Int("kapro.actuator.app_keys", len(handle.AppKeys)),
	)
	recordActuatorError(span, err)
	return handle, err
}

func (a *tracedActuator) commit(ctx context.Context, handle StageHandle, staging TwoPhaseStaging) (CommitResult, error) {
	ctx, span := a.start(ctx, "kapro.actuator.commit",
		attribute.String("kapro.actuator.stage_handle", handle.ID),
		attribute.String("kapro.actuator.substrate", string(handle.Substrate)),
		attribute.Int("kapro.actuator.app_keys", len(handle.AppKeys)),
	)
	defer span.End()
	result, err := staging.Commit(ctx, handle)
	span.SetAttributes(
		attribute.Int("kapro.actuator.applied", result.Applied),
		attribute.String("kapro.delivery.phase", string(result.Phase)),
	)
	recordActuatorError(span, err)
	return result, err
}

func (a *tracedActuator) discard(ctx context.Context, handle StageHandle, staging TwoPhaseStaging) error {
	ctx, span := a.start(ctx, "kapro.actuator.discard",
		attribute.String("kapro.actuator.stage_handle", handle.ID),
		attribute.String("kapro.actuator.substrate", string(handle.Substrate)),
		attribute.Int("kapro.actuator.app_keys", len(handle.AppKeys)),
	)
	defer span.End()
	err := staging.Discard(ctx, handle)
	recordActuatorError(span, err)
	return err
}

func (a *tracedActuator) start(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	baseAttrs := []attribute.KeyValue{
		attribute.String("kapro.actuator.name", a.name),
		attribute.String("kapro.actuator.contract_version", a.capabilities.Normalize().ContractVersion),
		attribute.String("kapro.actuator.substrate_kind", string(a.capabilities.SubstrateKind)),
		attribute.String("kapro.actuator.actuator", a.capabilities.Actuator),
		attribute.String("kapro.actuator.execution_scope", string(a.capabilities.ExecutionScope)),
	}
	return otel.Tracer(actuatorTracerName).Start(ctx, spanName, trace.WithAttributes(append(baseAttrs, attrs...)...))
}

func clusterName(cluster *kaprov1alpha1.Cluster) string {
	if cluster == nil {
		return ""
	}
	return cluster.Name
}

func recordActuatorError(span trace.Span, err error) {
	if err == nil {
		span.SetStatus(codes.Ok, "")
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

package actuator

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const actuatorTracerName = "kapro.io/kapro/pkg/kapro/actuator"

type tracedActuator struct {
	name         string
	delegate     Actuator
	capabilities Capabilities
}

type tracedBackendObjectReporter struct {
	*tracedActuator
	reporter BackendObjectReporter
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
	if _, ok := a.(*tracedBackendObjectReporter); ok {
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
	if reporter, ok := a.(BackendObjectReporter); ok {
		return &tracedBackendObjectReporter{tracedActuator: traced, reporter: reporter}
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

func (a *tracedActuator) IsConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, version, appKey string) (bool, error) {
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

func (a *tracedActuator) Rollback(ctx context.Context, cluster *kaprov1alpha2.Cluster, previousVersion, appKey string) error {
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

func (a *tracedActuator) IsAllConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) (bool, error) {
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

func (a *tracedBackendObjectReporter) BackendObjects(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) ([]kaprov1alpha2.BackendObjectStatus, error) {
	ctx, span := a.start(ctx, "kapro.actuator.backend_objects",
		attribute.String("kapro.cluster", clusterName(cluster)),
		attribute.Int("kapro.actuator.desired_versions", len(desiredVersions)),
	)
	defer span.End()
	objects, err := a.reporter.BackendObjects(ctx, cluster, desiredVersions)
	span.SetAttributes(attribute.Int("kapro.actuator.backend_objects", len(objects)))
	recordActuatorError(span, err)
	return objects, err
}

func (a *tracedActuator) start(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	baseAttrs := []attribute.KeyValue{
		attribute.String("kapro.actuator.name", a.name),
		attribute.String("kapro.actuator.contract_version", a.capabilities.Normalize().ContractVersion),
		attribute.String("kapro.actuator.driver", string(a.capabilities.Driver)),
		attribute.String("kapro.actuator.adapter", a.capabilities.Adapter),
		attribute.String("kapro.actuator.runtime", string(a.capabilities.Runtime)),
	}
	return otel.Tracer(actuatorTracerName).Start(ctx, spanName, trace.WithAttributes(append(baseAttrs, attrs...)...))
}

func clusterName(cluster *kaprov1alpha2.Cluster) string {
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

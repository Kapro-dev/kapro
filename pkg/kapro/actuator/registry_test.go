package actuator

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

type stubActuator struct{}

func (stubActuator) Apply(context.Context, ApplyRequest) error { return nil }
func (stubActuator) IsConverged(context.Context, *kaprov1alpha2.Cluster, string, string) (bool, error) {
	return true, nil
}
func (stubActuator) Rollback(context.Context, *kaprov1alpha2.Cluster, string, string) error {
	return nil
}
func (stubActuator) ApplyDelta(context.Context, DeltaApplyRequest) (int, error) { return 0, nil }
func (stubActuator) IsAllConverged(context.Context, *kaprov1alpha2.Cluster, map[string]string) (bool, error) {
	return true, nil
}

func TestRegistryRegisterRegistrationStoresCapabilities(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterRegistration(Registration{
		Mode: kaprov1alpha2.DeliveryModePush,
		Capabilities: Capabilities{
			Driver:              kaprov1alpha2.BackendDriverArgo,
			Runtime:             kaprov1alpha2.BackendRuntimeHub,
			SupportsApply:       true,
			SupportsConvergence: true,
		},
		Actuator: stubActuator{},
	})
	if err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}

	if _, err := registry.Resolve("push/argo"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	reg, ok := registry.Registration("push/argo")
	if !ok {
		t.Fatalf("Registration not found")
	}
	if reg.Capabilities.ContractVersion != ContractVersionV1Alpha1 {
		t.Fatalf("contract version = %q", reg.Capabilities.ContractVersion)
	}
	if !reg.Capabilities.SupportsMode(kaprov1alpha2.DeliveryModePush) {
		t.Fatalf("capabilities do not include push mode: %#v", reg.Capabilities)
	}
}

func TestRegistryResolveWrapsActuatorWithTracing(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	registry := NewRegistry()
	err := registry.RegisterRegistration(Registration{
		Mode: kaprov1alpha2.DeliveryModePush,
		Capabilities: Capabilities{
			Driver:              kaprov1alpha2.BackendDriverArgo,
			Adapter:             "argo-cd",
			Runtime:             kaprov1alpha2.BackendRuntimeHub,
			SupportsApply:       true,
			SupportsConvergence: true,
		},
		Actuator: stubActuator{},
	})
	if err != nil {
		t.Fatalf("RegisterRegistration: %v", err)
	}
	act, err := registry.Resolve("push/argo-cd")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if _, err := act.ApplyDelta(context.Background(), DeltaApplyRequest{
		Cluster:         &kaprov1alpha2.Cluster{ObjectMeta: objectMeta("cluster-a")},
		DesiredVersions: map[string]string{"api": "1.2.3", "web": "2.0.0"},
	}); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if _, err := act.IsAllConverged(context.Background(), &kaprov1alpha2.Cluster{ObjectMeta: objectMeta("cluster-a")}, map[string]string{"api": "1.2.3"}); err != nil {
		t.Fatalf("IsAllConverged: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	first := spans[0]
	if first.Name() != "kapro.actuator.apply_delta" {
		t.Fatalf("first span name = %q", first.Name())
	}
	attrs := spanAttributes(first)
	for key, want := range map[string]string{
		"kapro.actuator.name":             "push/argo-cd",
		"kapro.actuator.contract_version": ContractVersionV1Alpha1,
		"kapro.actuator.driver":           string(kaprov1alpha2.BackendDriverArgo),
		"kapro.actuator.adapter":          "argo-cd",
		"kapro.actuator.runtime":          string(kaprov1alpha2.BackendRuntimeHub),
		"kapro.cluster":                   "cluster-a",
	} {
		if got := attrs[key].AsString(); got != want {
			t.Fatalf("attribute %s = %q, want %q (all attrs %#v)", key, got, want, attrs)
		}
	}
	if got := attrs["kapro.actuator.desired_versions"].AsInt64(); got != 2 {
		t.Fatalf("desired_versions = %d, want 2", got)
	}
}

func TestWithTracingMarksErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	boom := errors.New("rollback failed")
	act := WithTracing("failing", failingRollbackActuator{err: boom})
	err := act.Rollback(context.Background(), &kaprov1alpha2.Cluster{ObjectMeta: objectMeta("cluster-a")}, "1.0.0", "api")
	if !errors.Is(err, boom) {
		t.Fatalf("Rollback err = %v, want %v", err, boom)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Name() != "kapro.actuator.rollback" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", spans[0].Status())
	}
	attrs := spanAttributes(spans[0])
	if got := attrs["kapro.previous_version"].AsString(); got != "1.0.0" {
		t.Fatalf("previous_version = %q", got)
	}
	if got := attrs["kapro.app_key"].AsString(); got != "api" {
		t.Fatalf("app = %q", got)
	}
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterRegistration(Registration{Actuator: stubActuator{}}); err == nil {
		t.Fatalf("RegisterRegistration with empty key succeeded")
	}
	if err := registry.RegisterRegistration(Registration{Name: "push/flux"}); err == nil {
		t.Fatalf("RegisterRegistration with nil actuator succeeded")
	}
	// When Name is empty and Mode/Capabilities.Modes are empty too,
	// RegistryKey() falls back to "/<adapter>" — a key that no
	// DeliverySpec.RegistryKey() at resolve time can ever match. Reject
	// instead of silently registering an unreachable actuator.
	leadingSlash := Registration{
		Actuator:     stubActuator{},
		Capabilities: Capabilities{Adapter: "argo"},
	}
	if err := registry.RegisterRegistration(leadingSlash); err == nil {
		t.Fatalf("RegisterRegistration accepted leading-slash key from empty Mode+Name")
	}
}

func TestRegistryUpsertRegistrationSurfacesValidationError(t *testing.T) {
	registry := NewRegistry()
	prev, err := registry.UpsertRegistration(Registration{Actuator: nil})
	if err == nil {
		t.Fatalf("UpsertRegistration must surface nil-actuator error")
	}
	if prev != nil {
		t.Fatalf("UpsertRegistration returned non-nil prev on validation failure: %#v", prev)
	}
}

func TestRegistryRegisterKeepsLegacyPermissiveBehavior(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("", nil); err != nil {
		t.Fatalf("legacy Register rejected empty nil entry: %v", err)
	}
	got, err := registry.Resolve("")
	if err != nil {
		t.Fatalf("Resolve legacy empty key: %v", err)
	}
	if got != nil {
		t.Fatalf("Resolve legacy empty key = %#v, want nil", got)
	}
}

type failingRollbackActuator struct {
	stubActuator
	err error
}

func (a failingRollbackActuator) Rollback(context.Context, *kaprov1alpha2.Cluster, string, string) error {
	return a.err
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	attrs := map[string]attribute.Value{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value
	}
	return attrs
}

package actuator

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

type stagedActuator struct {
	stubActuator
	prepareErr error
}

func (a stagedActuator) Prepare(context.Context, StageRequest) (StageHandle, error) {
	if a.prepareErr != nil {
		return StageHandle{}, a.prepareErr
	}
	return StageHandle{
		ID:        "stage-1",
		Substrate: kaprov1alpha1.SubstrateKindOCI,
		AppKeys:   []string{"api", "web"},
		Expiry:    metav1.Now(),
	}, nil
}

func (a stagedActuator) Commit(context.Context, StageHandle) (CommitResult, error) {
	return CommitResult{Applied: 2, Phase: kaprov1alpha1.DeliveryPhaseConverged}, nil
}

func (a stagedActuator) Discard(context.Context, StageHandle) error {
	return nil
}

func TestAsTwoPhaseDetectsExtension(t *testing.T) {
	if _, ok := AsTwoPhase(stubActuator{}); ok {
		t.Fatal("stubActuator reported TwoPhaseStaging support")
	}
	staging, ok := AsTwoPhase(stagedActuator{})
	if !ok {
		t.Fatal("stagedActuator did not report TwoPhaseStaging support")
	}
	handle, err := staging.Prepare(context.Background(), StageRequest{
		Cluster:         &kaprov1alpha1.Cluster{ObjectMeta: objectMeta("cluster-a")},
		DesiredVersions: map[string]string{"api": "1.2.3"},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if handle.ID != "stage-1" {
		t.Fatalf("handle ID = %q", handle.ID)
	}
}

func TestWithTracingPreservesTwoPhaseStaging(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	act := WithTracing("pull/oci", stagedActuator{})
	staging, ok := AsTwoPhase(act)
	if !ok {
		t.Fatal("WithTracing dropped TwoPhaseStaging support")
	}
	handle, err := staging.Prepare(context.Background(), StageRequest{
		Cluster:         &kaprov1alpha1.Cluster{ObjectMeta: objectMeta("cluster-a")},
		DesiredVersions: map[string]string{"api": "1.2.3", "web": "2.0.0"},
		DryRun:          true,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := staging.Commit(context.Background(), handle); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := staging.Discard(context.Background(), handle); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	spans := recorder.Ended()
	for _, name := range []string{"kapro.actuator.prepare", "kapro.actuator.commit", "kapro.actuator.discard"} {
		if findActuatorSpan(spans, name) == nil {
			t.Fatalf("span %q not found in %d spans", name, len(spans))
		}
	}
	prepare := findActuatorSpan(spans, "kapro.actuator.prepare")
	attrs := spanAttributes(prepare)
	if got := attrs["kapro.cluster"].AsString(); got != "cluster-a" {
		t.Fatalf("cluster attr = %q", got)
	}
	if got := attrs["kapro.actuator.desired_versions"].AsInt64(); got != 2 {
		t.Fatalf("desired_versions = %d", got)
	}
	if got := attrs["kapro.actuator.dry_run"].AsBool(); !got {
		t.Fatalf("dry_run = false, want true")
	}
}

func TestWithTracingMarksTwoPhaseErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	boom := errors.New("stage failed")
	act := WithTracing("pull/oci", stagedActuator{prepareErr: boom})
	staging, ok := AsTwoPhase(act)
	if !ok {
		t.Fatal("WithTracing dropped TwoPhaseStaging support")
	}
	_, err := staging.Prepare(context.Background(), StageRequest{})
	if !errors.Is(err, boom) {
		t.Fatalf("Prepare err = %v, want %v", err, boom)
	}
	span := findActuatorSpan(recorder.Ended(), "kapro.actuator.prepare")
	if span == nil {
		t.Fatal("prepare span not found")
	}
	if span.Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", span.Status())
	}
}

func findActuatorSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

package gate

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestRegistryWrapsPredicateWithTracing(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	reg := NewRegistry()
	reg.MustRegister("budget", PredicateFunc(func(_ context.Context, req Request) (Result, error) {
		if req.Target != "cluster-a" {
			t.Fatalf("target = %q", req.Target)
		}
		return MakePassed("ok"), nil
	}))
	predicate, err := reg.Resolve("budget")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	got, err := predicate.Evaluate(context.Background(), Request{
		Fleet:        "checkout",
		Promotion:    "checkout-prod",
		PromotionRun: "checkout-prod-abc",
		Plan:         "default",
		Stage:        "canary",
		Target:       "cluster-a",
		Version:      "v1.2.3",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got.Phase != kaprov1alpha2.GatePhasePassed {
		t.Fatalf("phase = %s, want Passed", got.Phase)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "kapro.predicate.evaluate" {
		t.Fatalf("span name = %q", span.Name())
	}
	attrs := map[string]string{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	for key, want := range map[string]string{
		"kapro.predicate.name":  "budget",
		"kapro.fleet":           "checkout",
		"kapro.promotion":       "checkout-prod",
		"kapro.promotionrun":    "checkout-prod-abc",
		"kapro.plan":            "default",
		"kapro.stage":           "canary",
		"kapro.target":          "cluster-a",
		"kapro.version":         "v1.2.3",
		"kapro.predicate.phase": string(kaprov1alpha2.GatePhasePassed),
	} {
		if attrs[key] != want {
			t.Fatalf("attribute %s = %q, want %q (all attrs %#v)", key, attrs[key], want, attrs)
		}
	}
}

func TestTracingFallsBackToRequestContextIdentity(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	predicate := WithTracing("builtin", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	}))
	_, err := predicate.Evaluate(context.Background(), Request{
		Context: &Context{
			PromotionRunRef: "run-from-context",
			Plan:            "plan-from-context",
			Stage:           "stage-from-context",
			Target:          "target-from-context",
			Version:         "version-from-context",
		},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attrs := spanAttributes(spans[0])
	for key, want := range map[string]string{
		"kapro.promotionrun": "run-from-context",
		"kapro.plan":         "plan-from-context",
		"kapro.stage":        "stage-from-context",
		"kapro.target":       "target-from-context",
		"kapro.version":      "version-from-context",
	} {
		if attrs[key] != want {
			t.Fatalf("attribute %s = %q, want %q (all attrs %#v)", key, attrs[key], want, attrs)
		}
	}
}

func TestRegistryWithoutTracing(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	reg := NewRegistryWithoutTracing()
	reg.MustRegister("plain", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	}))
	predicate, err := reg.Resolve("plain")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := predicate.Evaluate(context.Background(), Request{}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(recorder.Ended()) != 0 {
		t.Fatalf("ended spans = %d, want 0", len(recorder.Ended()))
	}
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]string {
	attrs := map[string]string{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	return attrs
}

func TestRegistryValidationAndNames(t *testing.T) {
	reg := NewRegistryWithoutTracing()
	if err := reg.Register("", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	})); err == nil {
		t.Fatalf("Register empty name succeeded")
	}
	if err := reg.Register("nil", nil); err == nil {
		t.Fatalf("Register nil predicate succeeded")
	}
	if err := reg.Register("b", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	})); err != nil {
		t.Fatalf("Register b: %v", err)
	}
	if err := reg.Register("a", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	})); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := reg.Register("a", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakePassed("ok"), nil
	})); err == nil {
		t.Fatalf("duplicate Register succeeded")
	}
	names := reg.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %#v, want sorted [a b]", names)
	}
	previous := reg.Upsert("a", PredicateFunc(func(context.Context, Request) (Result, error) {
		return MakeFailed("No", "no"), nil
	}))
	if previous == nil {
		t.Fatalf("Upsert previous = nil")
	}
}

type closeablePredicate struct {
	closed bool
}

func (p *closeablePredicate) Evaluate(context.Context, Request) (Result, error) {
	return MakePassed("ok"), nil
}

func (p *closeablePredicate) Close() error {
	p.closed = true
	return nil
}

func TestRegistryReturnsOriginalPredicateForReplacementCleanup(t *testing.T) {
	reg := NewRegistry()
	first := &closeablePredicate{}
	second := &closeablePredicate{}
	reg.MustRegister("plugin", first)

	old := reg.Upsert("plugin", second)
	if old != first {
		t.Fatalf("Upsert old = %#v, want original closeable predicate", old)
	}
	if closer, ok := old.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("close old: %v", err)
		}
	} else {
		t.Fatalf("old predicate does not expose Close")
	}
	if !first.closed {
		t.Fatalf("first predicate was not closed")
	}

	removed, ok := reg.Unregister("plugin")
	if !ok || removed != second {
		t.Fatalf("Unregister = %#v/%v, want second true", removed, ok)
	}
}

package decisiontrace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestEmitterCreatesBoundedDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := Emitter{
		Client:          c,
		MaxMessageRunes: 5,
		MaxEvidence:     1,
		MaxDetailRunes:  4,
	}.Emit(context.Background(), kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventGateEvaluate,
		Source:       "gate/slo",
		Phase:        "Failed",
		Reason:       "SLOViolation",
		Message:      "too much error budget burned",
		Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{
			{Type: "metrics", Source: "prometheus", Detail: map[string]string{"query": "sum(rate(errors_total[5m]))"}},
			{Type: "extra"},
		},
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var list kaproruntimev1alpha1.DecisionTraceList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len traces = %d, want 1", len(list.Items))
	}
	trace := list.Items[0]
	if trace.Labels["kapro.io/promotionrun"] != "run-a" {
		t.Fatalf("promotionrun label = %q", trace.Labels["kapro.io/promotionrun"])
	}
	if trace.Spec.Message != "too m" {
		t.Fatalf("message = %q, want truncated", trace.Spec.Message)
	}
	if len(trace.Spec.Evidence) != 1 {
		t.Fatalf("evidence len = %d, want 1", len(trace.Spec.Evidence))
	}
	if got := trace.Spec.Evidence[0].Detail["query"]; got != "sum(" {
		t.Fatalf("query detail = %q, want truncated", got)
	}
	if trace.Spec.Time.IsZero() {
		t.Fatal("time was not defaulted")
	}

	if err := (Emitter{Client: c}).Emit(context.Background(), trace.Spec); err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("duplicate emit created %d traces, want 1", len(list.Items))
	}
}

func TestEmitterSignsDecisionTraceWhenSignerConfigured(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := NewEd25519Signer("test-key", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaproruntimev1alpha1.DecisionTrace{}).
		Build()

	spec := kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventGateEvaluate,
		Source:       "gate/slo",
		Phase:        "Passed",
		Reason:       "GateEvaluated",
		Message:      "healthy",
	}
	if err := (Emitter{Client: c, Signer: signer}).Emit(context.Background(), spec); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var list kaproruntimev1alpha1.DecisionTraceList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len traces = %d, want 1", len(list.Items))
	}
	trace := list.Items[0]
	if !trace.Status.Signed {
		t.Fatal("trace was not marked signed")
	}
	sig := Signature{
		Algorithm:     trace.Status.SignatureAlgorithm,
		KeyID:         trace.Status.SignatureKeyID,
		PayloadDigest: trace.Status.PayloadDigest,
		Signature:     trace.Status.Signature,
	}
	if err := VerifyEd25519(trace.Spec, sig, publicKey); err != nil {
		t.Fatalf("VerifyEd25519: %v", err)
	}
}

func TestEmitterSigningFailureLeavesTraceUnsigned(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaproruntimev1alpha1.DecisionTrace{}).
		Build()
	boom := errors.New("sign boom")

	err := (Emitter{Client: c, Signer: failingSigner{err: boom}}).Emit(context.Background(), kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
		Source:       "promotionrun-controller",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Emit error = %v, want boom", err)
	}

	var list kaproruntimev1alpha1.DecisionTraceList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len traces = %d, want 1", len(list.Items))
	}
	if list.Items[0].Status.Signed {
		t.Fatal("trace was marked signed after signing failure")
	}
}

func TestEmitterReturnsCreateError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	boom := errors.New("boom")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return boom
			},
		}).
		Build()

	err := (Emitter{Client: c}).Emit(context.Background(), kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventRollback,
		Source:       "promotionrun-controller",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Emit error = %v, want boom", err)
	}
}

func TestEmitterEmitsSpanWithDecisionTraceAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := Emitter{Client: c}.Emit(context.Background(), kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		Plan:         "canary",
		Stage:        "prod",
		Target:       "cluster-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventGateEvaluate,
		Source:       "gate/slo",
		Phase:        "Passed",
		Reason:       "GateEvaluated",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "kapro.decisiontrace.emit" {
		t.Fatalf("span name = %q", span.Name())
	}
	attrs := spanAttributes(span)
	for key, want := range map[string]string{
		"kapro.promotionrun":             "run-a",
		"kapro.plan":                     "canary",
		"kapro.stage":                    "prod",
		"kapro.target":                   "cluster-a",
		"kapro.decisiontrace.event_type": "GateEvaluate",
		"kapro.decisiontrace.source":     "gate/slo",
		"kapro.decisiontrace.phase":      "Passed",
		"kapro.decisiontrace.reason":     "GateEvaluated",
	} {
		if got := attrs[key]; got != want {
			t.Fatalf("attribute %s = %q, want %q", key, got, want)
		}
	}
}

type failingSigner struct {
	err error
}

func (s failingSigner) SignDecisionTrace(context.Context, kaproruntimev1alpha1.DecisionTraceSpec) (Signature, error) {
	return Signature{}, s.err
}

func TestEmitterIgnoresAlreadyExists(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add runtime scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return apierrors.NewAlreadyExists(schema.GroupResource{Group: "kapro.io", Resource: "decisiontraces"}, "dtrace-x")
			},
		}).
		Build()

	err := (Emitter{Client: c}).Emit(context.Background(), kaproruntimev1alpha1.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaproruntimev1alpha1.DecisionTraceEventStage,
		Source:       "promotionrun-controller",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
}

func TestEmitterValidatesRequiredFields(t *testing.T) {
	for name, spec := range map[string]kaproruntimev1alpha1.DecisionTraceSpec{
		"promotionRun": {EventType: kaproruntimev1alpha1.DecisionTraceEventStage, Source: "controller"},
		"eventType":    {PromotionRun: "run-a", Source: "controller"},
		"source":       {PromotionRun: "run-a", EventType: kaproruntimev1alpha1.DecisionTraceEventStage},
	} {
		err := (Emitter{Client: fake.NewClientBuilder().Build()}).Emit(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]string {
	attrs := map[string]string{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	return attrs
}

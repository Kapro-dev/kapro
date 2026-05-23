package decisiontrace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestEmitterCreatesBoundedDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := Emitter{
		Client:          c,
		MaxMessageRunes: 5,
		MaxEvidence:     1,
		MaxDetailRunes:  4,
	}.Emit(context.Background(), kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaprov1alpha2.DecisionTraceEventGateEvaluate,
		Source:       "gate/slo",
		Phase:        "Failed",
		Reason:       "SLOViolation",
		Message:      "too much error budget burned",
		Evidence: []kaprov1alpha2.DecisionTraceEvidence{
			{Type: "metrics", Source: "prometheus", Detail: map[string]string{"query": "sum(rate(errors_total[5m]))"}},
			{Type: "extra"},
		},
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var list kaprov1alpha2.DecisionTraceList
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
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
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
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()

	spec := kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaprov1alpha2.DecisionTraceEventGateEvaluate,
		Source:       "gate/slo",
		Phase:        "Passed",
		Reason:       "GateEvaluated",
		Message:      "healthy",
	}
	if err := (Emitter{Client: c, Signer: signer}).Emit(context.Background(), spec); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var list kaprov1alpha2.DecisionTraceList
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
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()
	boom := errors.New("sign boom")

	err := (Emitter{Client: c, Signer: failingSigner{err: boom}}).Emit(context.Background(), kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaprov1alpha2.DecisionTraceEventStage,
		Source:       "promotionrun-controller",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Emit error = %v, want boom", err)
	}

	var list kaprov1alpha2.DecisionTraceList
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
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
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

	err := (Emitter{Client: c}).Emit(context.Background(), kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaprov1alpha2.DecisionTraceEventRollback,
		Source:       "promotionrun-controller",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Emit error = %v, want boom", err)
	}
}

type failingSigner struct {
	err error
}

func (s failingSigner) SignDecisionTrace(context.Context, kaprov1alpha2.DecisionTraceSpec) (Signature, error) {
	return Signature{}, s.err
}

func TestEmitterIgnoresAlreadyExists(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return apierrors.NewAlreadyExists(schema.GroupResource{Group: "kapro.io", Resource: "decisiontraces"}, "dtrace-x")
			},
		}).
		Build()

	err := (Emitter{Client: c}).Emit(context.Background(), kaprov1alpha2.DecisionTraceSpec{
		PromotionRun: "run-a",
		EventType:    kaprov1alpha2.DecisionTraceEventStage,
		Source:       "promotionrun-controller",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
}

func TestEmitterValidatesRequiredFields(t *testing.T) {
	for name, spec := range map[string]kaprov1alpha2.DecisionTraceSpec{
		"promotionRun": {EventType: kaprov1alpha2.DecisionTraceEventStage, Source: "controller"},
		"eventType":    {PromotionRun: "run-a", Source: "controller"},
		"source":       {PromotionRun: "run-a", EventType: kaprov1alpha2.DecisionTraceEventStage},
	} {
		err := (Emitter{Client: fake.NewClientBuilder().Build()}).Emit(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

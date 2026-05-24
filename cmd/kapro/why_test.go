package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kapro.io/kapro/internal/cli"
)

func TestRunWhyRendersDecisionTraceTimeline(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(
			whyTraceObject("later", "run-a", "2026-05-23T10:02:00Z", kaproruntimev1alpha1.DecisionTraceEventGateEvaluate),
			whyTraceObject("earlier", "run-a", "2026-05-23T10:01:00Z", kaproruntimev1alpha1.DecisionTraceEventStage),
			whyTraceObject("other", "run-b", "2026-05-23T10:00:00Z", kaproruntimev1alpha1.DecisionTraceEventRollback),
		).
		Build()

	out := withCapturedOutput(t, func() {
		if err := runWhyWithClient(context.Background(), c, "run-a"); err != nil {
			t.Fatalf("runWhyWithClient: %v", err)
		}
	})

	if !strings.Contains(out, "Why run-a") {
		t.Fatalf("missing heading:\n%s", out)
	}
	if strings.Contains(out, "other") {
		t.Fatalf("included trace from another run:\n%s", out)
	}
	first := strings.Index(out, "earlier")
	second := strings.Index(out, "later")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("traces not rendered in time order:\n%s", out)
	}
	for _, want := range []string{"GateEvaluate", "Stage", "plan=canary", "stage=prod", "target=cluster-a", "Ed25519 key=test-key", "error budget"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunWhyRendersDeliveryEvidenceSummary(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(whyDeliveryTraceObject("delivery", "run-a")).
		Build()

	out := withCapturedOutput(t, func() {
		if err := runWhyWithClient(context.Background(), c, "run-a"); err != nil {
			t.Fatalf("runWhyWithClient: %v", err)
		}
	})

	for _, want := range []string{
		"Delivery",
		"DeliveryFailed",
		"appKey=api",
		"stagingFailurePhase=Staging",
		"stagingFailedObjects=1",
		"observedDigest=sha256:abc",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"committedObjects=0",
		"commitFailedObjects=0",
		"appliedObjects=0",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output included zero delivery counter %q:\n%s", unwanted, out)
		}
	}
}

func TestRunWhyJSONOutput(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(whyTraceObject("trace-a", "run-a", "2026-05-23T10:01:00Z", kaproruntimev1alpha1.DecisionTraceEventSuspend)).
		Build()
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		if err := runWhyWithClient(context.Background(), c, "run-a"); err != nil {
			t.Fatalf("runWhyWithClient: %v", err)
		}
	})

	var got whyReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal why JSON: %v\nraw: %s", err, out)
	}
	if got.PromotionRun != "run-a" {
		t.Fatalf("promotionRun = %q", got.PromotionRun)
	}
	if len(got.Traces) != 1 || got.Traces[0].Name != "trace-a" {
		t.Fatalf("unexpected traces: %+v", got.Traces)
	}
	if !got.Traces[0].Signed || got.Traces[0].Signature == nil {
		t.Fatalf("signature details missing: %+v", got.Traces[0])
	}
	if got.Traces[0].Signature.KeyID != "test-key" || got.Traces[0].Signature.PayloadDigest != "sha256:abc" {
		t.Fatalf("signature details wrong: %+v", got.Traces[0].Signature)
	}
}

func TestRunWhyNoDecisionTracesIsNotError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(diagTestScheme(t)).Build()
	out := withCapturedOutput(t, func() {
		if err := runWhyWithClient(context.Background(), c, "missing"); err != nil {
			t.Fatalf("runWhyWithClient: %v", err)
		}
	})
	if !strings.Contains(out, "No DecisionTrace records found") {
		t.Fatalf("missing empty message:\n%s", out)
	}
}

func TestRunWhyNoDecisionTracesJSONUsesEmptySlice(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(diagTestScheme(t)).Build()
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		if err := runWhyWithClient(context.Background(), c, "missing"); err != nil {
			t.Fatalf("runWhyWithClient: %v", err)
		}
	})

	var got whyReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal why JSON: %v\nraw: %s", err, out)
	}
	if got.Traces == nil || len(got.Traces) != 0 {
		t.Fatalf("traces = %#v, want empty non-nil slice", got.Traces)
	}
	if !strings.Contains(out, `"traces": []`) {
		t.Fatalf("json should encode empty traces as [], got: %s", out)
	}
}

func whyTraceObject(name, run, ts string, eventType kaproruntimev1alpha1.DecisionTraceEventType) *kaproruntimev1alpha1.DecisionTrace {
	return &kaproruntimev1alpha1.DecisionTrace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{promotionRunLabelKey: run},
		},
		Spec: kaproruntimev1alpha1.DecisionTraceSpec{
			PromotionRun: run,
			Plan:         "canary",
			Stage:        "prod",
			Target:       "cluster-a",
			EventType:    eventType,
			Source:       "slo",
			Phase:        "Failed",
			Reason:       "SLOViolation",
			Message:      "error budget exhausted",
			Time:         mustTime(ts),
			Evidence: []kaproruntimev1alpha1.DecisionTraceEvidence{{
				Type:   "gate",
				Source: "prometheus",
				Detail: map[string]string{"query": "sum(errors_total)"},
			}},
		},
		Status: kaproruntimev1alpha1.DecisionTraceStatus{
			Signed:             true,
			SignatureAlgorithm: "Ed25519",
			SignatureKeyID:     "test-key",
			PayloadDigest:      "sha256:abc",
		},
	}
}

func whyDeliveryTraceObject(name, run string) *kaproruntimev1alpha1.DecisionTrace {
	trace := whyTraceObject(name, run, "2026-05-23T10:03:00Z", kaproruntimev1alpha1.DecisionTraceEventDelivery)
	trace.Spec.Source = "cluster-delivery"
	trace.Spec.Reason = "DeliveryFailed"
	trace.Spec.Message = "cluster cluster-a app api delivery Failed: dry-run rejected configmap"
	trace.Spec.Evidence = []kaproruntimev1alpha1.DecisionTraceEvidence{{
		Type:   "cluster-delivery",
		Source: "cluster-a",
		Detail: map[string]string{
			"appKey":               "api",
			"desiredVersion":       "v2",
			"phase":                "Failed",
			"stagingFailurePhase":  "Staging",
			"stagedObjects":        "3",
			"stagingFailedObjects": "1",
			"committedObjects":     "0",
			"commitFailedObjects":  "0",
			"appliedObjects":       "0",
			"format":               "raw-yaml",
			"observedDigest":       "sha256:abc",
		},
	}}
	return trace
}

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

func TestRunWhyRendersDecisionTraceTimeline(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(
			whyTraceObject("later", "run-a", "2026-05-23T10:02:00Z", kaprov1alpha2.DecisionTraceEventGateEvaluate),
			whyTraceObject("earlier", "run-a", "2026-05-23T10:01:00Z", kaprov1alpha2.DecisionTraceEventStage),
			whyTraceObject("other", "run-b", "2026-05-23T10:00:00Z", kaprov1alpha2.DecisionTraceEventRollback),
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
	for _, want := range []string{"GateEvaluate", "Stage", "plan=canary", "stage=prod", "target=cluster-a", "signed", "error budget"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunWhyJSONOutput(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(whyTraceObject("trace-a", "run-a", "2026-05-23T10:01:00Z", kaprov1alpha2.DecisionTraceEventSuspend)).
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
	if !got.Traces[0].Signed || !strings.Contains(got.Traces[0].Signature, "key=test-key") {
		t.Fatalf("signature summary missing: %+v", got.Traces[0])
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

func whyTraceObject(name, run, ts string, eventType kaprov1alpha2.DecisionTraceEventType) *kaprov1alpha2.DecisionTrace {
	return &kaprov1alpha2.DecisionTrace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{promotionRunLabelKey: run},
		},
		Spec: kaprov1alpha2.DecisionTraceSpec{
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
			Evidence: []kaprov1alpha2.DecisionTraceEvidence{{
				Type:   "gate",
				Source: "prometheus",
				Detail: map[string]string{"query": "sum(errors_total)"},
			}},
		},
		Status: kaprov1alpha2.DecisionTraceStatus{
			Signed:             true,
			SignatureAlgorithm: "Ed25519",
			SignatureKeyID:     "test-key",
			PayloadDigest:      "sha256:abc",
		},
	}
}

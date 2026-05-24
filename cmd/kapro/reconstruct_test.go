package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"kapro.io/kapro/internal/cli"
)

func TestRunReconstructFiltersTimelineAtTimestamp(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(
			whyTraceObject("gate-failed", "run-a", "2026-05-23T10:01:00Z", kaproruntimev1alpha1.DecisionTraceEventGateEvaluate),
			whyTraceObject("stage-ready", "run-a", "2026-05-23T10:02:00Z", kaproruntimev1alpha1.DecisionTraceEventStage),
			whyTraceObject("rollback-later", "run-a", "2026-05-23T10:03:00Z", kaproruntimev1alpha1.DecisionTraceEventRollback),
			whyTraceObject("other-run", "run-b", "2026-05-23T10:01:00Z", kaproruntimev1alpha1.DecisionTraceEventStage),
		).
		Build()
	at := mustParseRFC3339(t, "2026-05-23T10:02:30Z")

	out := withCapturedOutput(t, func() {
		if err := runReconstructWithClient(context.Background(), c, "run-a", at); err != nil {
			t.Fatalf("runReconstructWithClient: %v", err)
		}
	})

	for _, want := range []string{
		"Reconstruction run-a @ 2026-05-23T10:02:30Z",
		"GateEvaluate",
		"Stage",
		"plan=canary stage=prod target=cluster-a type=Stage",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"rollback-later", "Rollback", "other-run"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output included %q after reconstruction cutoff or wrong run:\n%s", unwanted, out)
		}
	}
}

func TestRunReconstructJSONIncludesTimeline(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(
			whyTraceObject("gate-failed", "run-a", "2026-05-23T10:01:00Z", kaproruntimev1alpha1.DecisionTraceEventGateEvaluate),
			whyTraceObject("rollback-later", "run-a", "2026-05-23T10:03:00Z", kaproruntimev1alpha1.DecisionTraceEventRollback),
		).
		Build()
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		if err := runReconstructWithClient(context.Background(), c, "run-a", mustParseRFC3339(t, "2026-05-23T10:02:00Z")); err != nil {
			t.Fatalf("runReconstructWithClient: %v", err)
		}
	})

	var got reconstructReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal reconstruct JSON: %v\nraw: %s", err, out)
	}
	if got.PromotionRun != "run-a" || got.At != "2026-05-23T10:02:00Z" {
		t.Fatalf("unexpected report identity: %+v", got)
	}
	if got.TraceCount != 1 || len(got.Timeline) != 1 || got.Timeline[0].Name != "gate-failed" {
		t.Fatalf("unexpected timeline: %+v", got)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].EventType != string(kaproruntimev1alpha1.DecisionTraceEventGateEvaluate) {
		t.Fatalf("unexpected decisions: %+v", got.Decisions)
	}
}

func TestRunReconstructJSONUsesEmptySlicesWhenNoTraceMatches(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(whyTraceObject("future", "run-a", "2026-05-23T10:03:00Z", kaproruntimev1alpha1.DecisionTraceEventRollback)).
		Build()
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		if err := runReconstructWithClient(context.Background(), c, "run-a", mustParseRFC3339(t, "2026-05-23T10:02:00Z")); err != nil {
			t.Fatalf("runReconstructWithClient: %v", err)
		}
	})

	var got reconstructReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal reconstruct JSON: %v\nraw: %s", err, out)
	}
	if got.TraceCount != 0 || got.Timeline == nil || got.Decisions == nil {
		t.Fatalf("empty reconstruction should use non-nil slices: %+v", got)
	}
	if !strings.Contains(out, `"decisions": []`) || !strings.Contains(out, `"timeline": []`) {
		t.Fatalf("json should encode empty slices as [], got: %s", out)
	}
}

func TestLatestDecisionsByScopeUsesLatestTimestampForUnsortedInput(t *testing.T) {
	decisions, err := latestDecisionsByScope([]whyTrace{
		{
			Name:      "newer",
			Time:      "2026-05-23T10:03:00Z",
			EventType: kaproruntimev1alpha1.DecisionTraceEventStage,
			Plan:      "canary",
			Stage:     "prod",
			Target:    "cluster-a",
			Reason:    "StageComplete",
		},
		{
			Name:      "older",
			Time:      "2026-05-23T10:01:00Z",
			EventType: kaproruntimev1alpha1.DecisionTraceEventStage,
			Plan:      "canary",
			Stage:     "prod",
			Target:    "cluster-a",
			Reason:    "StageStarted",
		},
	})
	if err != nil {
		t.Fatalf("latestDecisionsByScope: %v", err)
	}
	if len(decisions) != 1 || decisions[0].Reason != "StageComplete" {
		t.Fatalf("expected newer decision to win for unsorted input, got %+v", decisions)
	}
}

func TestNewReconstructCmdRequiresAt(t *testing.T) {
	cmd := newReconstructCmd()
	cmd.SetArgs([]string{"run-a"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `required flag(s) "at" not set`) {
		t.Fatalf("expected --at validation error, got %v", err)
	}
}

func TestNewReconstructCmdRejectsInvalidAt(t *testing.T) {
	cmd := newReconstructCmd()
	cmd.SetArgs([]string{"run-a", "--at", "not-a-time"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "parse --at as RFC3339 timestamp") {
		t.Fatalf("expected invalid --at validation error, got %v", err)
	}
}

func mustParseRFC3339(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return ts
}

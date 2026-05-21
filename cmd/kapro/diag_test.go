package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

func diagTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := kaprov1alpha2.AddToScheme(s); err != nil {
		t.Fatalf("kapro scheme: %v", err)
	}
	return s
}

// withCapturedOutput swaps cli.Out for a buffer so renderDiag is observable.
func withCapturedOutput(t *testing.T, fn func()) string {
	t.Helper()
	orig := cli.Out
	defer func() { cli.Out = orig }()
	var buf bytes.Buffer
	cli.Out = &buf
	fn()
	return buf.String()
}

func mustTime(s string) metav1.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return metav1.NewTime(t)
}

func TestRunDiag_PromotionNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(diagTestScheme(t)).Build()
	err := runDiagWithClient(context.Background(), c, "missing", 10)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestRunDiag_HappyPathRendersExpectedSections(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "checkout-v1.2.3",
			CreationTimestamp: mustTime("2026-05-19T10:00:00Z"),
		},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef: "checkout",
			Version:  "v1.2.3",
		},
		Status: kaprov1alpha2.PromotionStatus{
			Phase: kaprov1alpha2.PromotionPhaseProgressing,
			ActiveAttemptRef: &kaprov1alpha2.PromotionAttemptRef{
				Name:    "checkout-v1.2.3-001",
				Version: "v1.2.3",
				Phase:   kaprov1alpha2.PromotionRunPhaseProgressing,
			},
			Attempts: []kaprov1alpha2.PromotionAttemptRef{
				{Name: "checkout-v1.2.3-001", Version: "v1.2.3",
					Phase: kaprov1alpha2.PromotionRunPhaseProgressing},
			},
			Conditions: []metav1.Condition{{
				Type:               "Progressing",
				Status:             metav1.ConditionTrue,
				Reason:             "AttemptInFlight",
				Message:            "attempt 1 in flight",
				LastTransitionTime: mustTime("2026-05-19T10:00:30Z"),
			}},
		},
	}
	run := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-v1.2.3-001",
			Labels: map[string]string{"kapro.io/promotion": "checkout-v1.2.3"},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{Version: "v1.2.3"},
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-v1.2.3-001-de-prod"},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "checkout-v1.2.3-001",
			Target:          "de-prod",
			Stage:           "canary",
			Plan:            "checkout-progressive",
			Version:         "v1.2.3",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseWaitingApproval},
		},
	}
	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "evt-1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Promotion", Name: "checkout-v1.2.3"},
		Type:           corev1.EventTypeNormal,
		Reason:         "AttemptStamped",
		Message:        "stamped attempt 1",
		LastTimestamp:  mustTime("2026-05-19T10:00:05Z"),
	}

	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run, target, event).
		Build()

	out := withCapturedOutput(t, func() {
		if err := runDiagWithClient(context.Background(), c, "checkout-v1.2.3", 10); err != nil {
			t.Fatalf("runDiag: %v", err)
		}
	})

	for _, want := range []string{
		"promotion/checkout-v1.2.3",
		"checkout",    // FleetRef
		"Progressing", // phase
		"Active Run",
		"checkout-v1.2.3-001", // run name
		"Conditions",
		"AttemptInFlight",
		"Attempt history",
		"Active run targets",
		"de-prod",
		"WaitingApproval",
		"Blocked on",
		"Recent events",
		"AttemptStamped",
		"Suggested next actions",
		"kapro approve checkout-v1.2.3-001/de-prod",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestRunDiag_JSONOutputIsStable(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha2.PromotionSpec{FleetRef: "k1", Version: "v1", Suspended: true},
		Status:     kaprov1alpha2.PromotionStatus{Phase: kaprov1alpha2.PromotionPhasePaused},
	}
	c := fake.NewClientBuilder().WithScheme(diagTestScheme(t)).WithObjects(promo).Build()

	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		if err := runDiagWithClient(context.Background(), c, "p1", 10); err != nil {
			t.Fatalf("runDiag: %v", err)
		}
	})

	var got promotionDiag
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json unmarshal: %v\nraw: %s", err, out)
	}
	if got.Promotion == nil || got.Promotion.Name != "p1" {
		t.Fatalf("promotion not roundtripped: %+v", got.Promotion)
	}
	if !strings.Contains(out, `"targets"`) || strings.Contains(out, `"promotionTargets"`) {
		t.Fatalf("diag JSON should use targets key, got: %s", out)
	}
	if len(got.BlockedOn) == 0 || !strings.Contains(got.BlockedOn[0], "suspended") {
		t.Fatalf("expected suspended in blockedOn, got %v", got.BlockedOn)
	}
	if len(got.Next) == 0 {
		t.Fatalf("expected next-action suggestion for suspended promotion")
	}
}

func TestComputeBlockedOn_FailedTarget(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	targets := []kaprov1alpha2.Target{{
		Spec: kaprov1alpha2.TargetSpec{Target: "de-prod", Stage: "canary"},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseFailed, Message: "gate timeout"},
		},
	}}
	got := computeBlockedOn(promo, targets)
	if len(got) != 1 || !strings.Contains(got[0], "de-prod") || !strings.Contains(got[0], "gate timeout") {
		t.Fatalf("unexpected blockedOn: %v", got)
	}
}

func TestFilterPromotionEvents_OnlyMatchingObjects(t *testing.T) {
	promo := &kaprov1alpha2.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	runs := []kaprov1alpha2.PromotionRun{{ObjectMeta: metav1.ObjectMeta{Name: "r1"}}}
	targets := []kaprov1alpha2.Target{{ObjectMeta: metav1.ObjectMeta{Name: "t1"}}}
	all := []corev1.Event{
		{InvolvedObject: corev1.ObjectReference{Kind: "Promotion", Name: "p"}, Reason: "A"},
		{InvolvedObject: corev1.ObjectReference{Kind: "PromotionRun", Name: "r1"}, Reason: "B"},
		{InvolvedObject: corev1.ObjectReference{Kind: "Target", Name: "t1"}, Reason: "C"},
		{InvolvedObject: corev1.ObjectReference{Kind: "Promotion", Name: "other"}, Reason: "C"},
		{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "noise"}, Reason: "D"},
	}
	got := filterPromotionEvents(all, promo, runs, targets)
	if len(got) != 3 {
		t.Fatalf("expected 3 matching events, got %d: %+v", len(got), got)
	}
	reasons := got[0].Reason + got[1].Reason + got[2].Reason
	if !strings.Contains(reasons, "A") || !strings.Contains(reasons, "B") || !strings.Contains(reasons, "C") {
		t.Fatalf("wrong events filtered: %+v", got)
	}
}

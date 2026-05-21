package main

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestRenderTopShowsPromotionTargetSummary(t *testing.T) {
	promo := testPromotion("checkout")
	promo.Status.ActiveAttemptRef = &kaprov1alpha2.PromotionAttemptRef{Name: "checkout-g1"}
	run := testPromotionRun("checkout-g1", "checkout")
	run.Status.Summary = &kaprov1alpha2.PromotionRunSummary{
		TotalTargets:  3,
		SyncedTargets: 2,
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run).
		Build()

	out := withCapturedOutput(t, func() {
		if err := renderTopWithClient(context.Background(), c, "all"); err != nil {
			t.Fatalf("renderTop: %v", err)
		}
	})

	for _, want := range []string{"checkout", "Progressing", "retail", "v1.2.3", "2/3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("top output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTopRejectsNamespacedScope(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(diagTestScheme(t)).Build()
	err := renderTopWithClient(context.Background(), c, "default")
	if err == nil || !strings.Contains(err.Error(), "cluster-scoped") {
		t.Fatalf("expected cluster-scoped namespace error, got %v", err)
	}
}

func TestRenderTreeShowsRunsAndTargets(t *testing.T) {
	promo := testPromotion("checkout")
	run := testPromotionRun("checkout-g1", "checkout")
	run.Status.Summary = &kaprov1alpha2.PromotionRunSummary{TotalTargets: 1, SyncedTargets: 1}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-g1-prod",
			Labels: map[string]string{promotionRunLabelKey: "checkout-g1"},
		},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "checkout-g1",
			Target:          "prod",
			Stage:           "prod",
			Version:         "v1.2.3",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				Phase:   kaprov1alpha2.TargetPhaseConverged,
				Message: "ok",
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run, target).
		Build()

	out := withCapturedOutput(t, func() {
		if err := renderTreeWithClient(context.Background(), c, "checkout"); err != nil {
			t.Fatalf("renderTree: %v", err)
		}
	})

	for _, want := range []string{"Promotion/checkout", "`- PromotionRun/checkout-g1", "Target/checkout-g1-prod", "Converged", "stage=prod"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree output missing %q:\n%s", want, out)
		}
	}
}

func TestCollectKaproEventsFiltersNonKaproEvents(t *testing.T) {
	kaproEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "kapro-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{APIVersion: "kapro.io/v1alpha2", Kind: "Promotion", Name: "checkout"},
		Type:           corev1.EventTypeNormal,
		Reason:         "AttemptStamped",
		Message:        "stamped",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	podEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "pod-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "pod-1"},
		Type:           corev1.EventTypeNormal,
		Reason:         "Started",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(kaproEvent, podEvent).
		Build()

	events, err := collectKaproEvents(context.Background(), c, "", 0)
	if err != nil {
		t.Fatalf("collectKaproEvents: %v", err)
	}
	if len(events) != 1 || events[0].Name != "kapro-event" {
		t.Fatalf("expected only Kapro event, got %#v", events)
	}
}

func TestPromotionScopedEventsFilterSameNameDifferentKind(t *testing.T) {
	promo := testPromotion("checkout")
	run := testPromotionRun("checkout-g1", "checkout")
	kaproEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "kapro-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{APIVersion: "kapro.io/v1alpha2", Kind: "Promotion", Name: "checkout"},
		Type:           corev1.EventTypeNormal,
		Reason:         "AttemptStamped",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	podEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "pod-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout"},
		Type:           corev1.EventTypeNormal,
		Reason:         "Started",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run, kaproEvent, podEvent).
		Build()

	events, err := collectKaproEvents(context.Background(), c, "checkout", 0)
	if err != nil {
		t.Fatalf("collectKaproEvents: %v", err)
	}
	if len(events) != 1 || events[0].Name != "kapro-event" {
		t.Fatalf("expected scoped Kapro event only, got %#v", events)
	}
}

func TestPromotionScopedEventsRejectSameKindDifferentAPIGroup(t *testing.T) {
	promo := testPromotion("checkout")
	run := testPromotionRun("checkout-g1", "checkout")
	kaproEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "kapro-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{APIVersion: "kapro.io/v1alpha2", Kind: "Promotion", Name: "checkout"},
		Type:           corev1.EventTypeNormal,
		Reason:         "AttemptStamped",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	otherEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "other-event", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{APIVersion: "example.io/v1", Kind: "Promotion", Name: "checkout"},
		Type:           corev1.EventTypeNormal,
		Reason:         "OtherPromotion",
		LastTimestamp:  mustTime("2026-05-21T10:00:00Z"),
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run, kaproEvent, otherEvent).
		Build()

	events, err := collectKaproEvents(context.Background(), c, "checkout", 0)
	if err != nil {
		t.Fatalf("collectKaproEvents: %v", err)
	}
	if len(events) != 1 || events[0].Name != "kapro-event" {
		t.Fatalf("expected only Kapro API-group event, got %#v", events)
	}
}

func TestRunGetPromotionShowsActiveAttemptProgress(t *testing.T) {
	promo := testPromotion("checkout")
	promo.Status.ActiveAttemptRef = &kaprov1alpha2.PromotionAttemptRef{Name: "checkout-g1"}
	promo.Status.LifecycleHandlerResults = []kaprov1alpha2.PromotionLifecycleHandlerResult{{
		Name:     "notify",
		Phase:    kaprov1alpha2.PromotionPhaseSucceeded,
		Kind:     "Event",
		Result:   "Succeeded",
		Attempts: 1,
		Message:  "ok",
		FiredAt:  mustTime("2026-05-21T10:00:00Z"),
	}}
	run := testPromotionRun("checkout-g1", "checkout")
	run.Status.Summary = &kaprov1alpha2.PromotionRunSummary{
		TotalTargets:   2,
		SyncedTargets:  1,
		FailedTargets:  0,
		PendingTargets: 1,
	}
	c := fake.NewClientBuilder().
		WithScheme(diagTestScheme(t)).
		WithObjects(promo, run).
		Build()

	out := withCapturedOutput(t, func() {
		if err := runGetPromotionWithClient(context.Background(), c, "checkout", 5); err != nil {
			t.Fatalf("getPromotion: %v", err)
		}
	})

	for _, want := range []string{"promotion/checkout", "Active attempt progress", "checkout-g1", "Lifecycle handlers", "notify"} {
		if !strings.Contains(out, want) {
			t.Fatalf("get promotion output missing %q:\n%s", want, out)
		}
	}
}

func testPromotion(name string) *kaprov1alpha2.Promotion {
	return &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: mustTime("2026-05-21T09:00:00Z"),
		},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef: "retail",
			Version:  "v1.2.3",
		},
		Status: kaprov1alpha2.PromotionStatus{
			Phase: kaprov1alpha2.PromotionPhaseProgressing,
			Attempts: []kaprov1alpha2.PromotionAttemptRef{{
				Name:    name + "-g1",
				Version: "v1.2.3",
				Phase:   kaprov1alpha2.PromotionRunPhaseProgressing,
			}},
			ResolvedVersion: "v1.2.3",
		},
	}
}

func testPromotionRun(name, promotion string) *kaprov1alpha2.PromotionRun {
	return &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            map[string]string{promotionLabelKey: promotion},
			CreationTimestamp: mustTime("2026-05-21T09:05:00Z"),
		},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "v1.2.3",
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase: kaprov1alpha2.PromotionRunPhaseProgressing,
		},
	}
}

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestPromotionTriggerSuspendedCreatesNothing(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.Suspended = ptr.To(true)
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionSuspended)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Suspended condition = %+v", cond)
	}
}

func TestPromotionTriggerOmittedSuspendedDefaultsSafe(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.Suspended = nil
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionSuspended)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Suspended condition = %+v", cond)
	}
}

func TestPromotionTriggerDryRunCreatesNothingAndRecordsArtifact(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.DryRun = true
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	if got.Status.LastArtifact == nil || got.Status.LastArtifact.Digest != testArtifact().Digest {
		t.Fatalf("LastArtifact = %+v", got.Status.LastArtifact)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionUpdated)
	if cond == nil || cond.Reason != "DryRun" {
		t.Fatalf("PromotionUpdated condition = %+v", cond)
	}
}

func TestPromotionTriggerSignatureFailureBlocksPromotionRun(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{err: errors.New("bad signature")}, promotionTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	if cond == nil || cond.Reason != "SignatureVerificationFailed" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerCreatesDigestPinnedPromotion(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionCount(t, ctx, c, 1)
	// PromotionTrigger no longer creates PromotionRun directly; the
	// PromotionController stamps that under the managed Promotion.
	assertPromotionRunCount(t, ctx, c, 0)

	managed := getManagedPromotion(t, ctx, c, "checkout")
	if managed.Spec.FleetRef != "checkout" {
		t.Fatalf("FleetRef = %q", managed.Spec.FleetRef)
	}
	if managed.Spec.Version != "oci://registry.example.com/checkout@sha256:abcdef1234567890" {
		t.Fatalf("Version = %q", managed.Spec.Version)
	}
	if !managed.Spec.Suspended {
		t.Fatal("managed Promotion should be suspended by default per template fixture")
	}
	if managed.Labels[promotionTriggerLabel] != "checkout" || managed.Annotations[promotionTriggerDigestAnno] != testArtifact().Digest {
		t.Fatalf("metadata labels=%v annotations=%v", managed.Labels, managed.Annotations)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	if got.Status.LastTriggeredAt == "" || got.Status.ManagedPromotion != "checkout" {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestPromotionTriggerOmittedTemplateSuspendedDefaultsPromotionSafe(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.PromotionTemplate.Suspended = nil
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	managed := getManagedPromotion(t, ctx, c, "checkout")
	if !managed.Spec.Suspended {
		t.Fatal("omitted template suspended should create a suspended Promotion")
	}
}

func TestPromotionTriggerExplicitTemplateUnsuspendedCreatesActivePromotion(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.PromotionTemplate.Suspended = ptr.To(false)
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	managed := getManagedPromotion(t, ctx, c, "checkout")
	if managed.Spec.Suspended {
		t.Fatal("explicit template suspended=false should create an unsuspended Promotion")
	}
}

func TestPromotionTriggerCreatedAtUsesCreationTime(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture())
	checkTime := fixedNow()
	createTime := fixedNow().Add(2 * time.Minute)
	calls := 0
	reconciler.Now = func() time.Time {
		calls++
		if calls == 1 {
			return checkTime
		}
		return createTime
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	managed := getManagedPromotion(t, ctx, c, "checkout")
	want := createTime.UTC().Format(time.RFC3339)
	if got := managed.Annotations[promotionTriggerCreatedAnno]; got != want {
		t.Fatalf("created annotation = %q, want %q", got, want)
	}
	trigger := getPromotionTrigger(t, ctx, c, "checkout")
	if trigger.Status.LastCheckedAt != checkTime.UTC().Format(time.RFC3339) {
		t.Fatalf("last checked = %q", trigger.Status.LastCheckedAt)
	}
	if trigger.Status.LastTriggeredAt != want {
		t.Fatalf("last triggered = %q, want %q", trigger.Status.LastTriggeredAt, want)
	}
}

func TestPromotionTriggerDoesNotDuplicateSameDigest(t *testing.T) {
	ctx := context.Background()
	trigger := promotionTriggerFixture()
	// Pre-existing managed Promotion already targets the same digest with the
	// same template hash; the trigger must dedupe.
	tmplHash := triggerTemplateHash(trigger)
	existing := &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout",
			Labels: map[string]string{
				promotionTriggerLabel:             "checkout",
				promotionTriggerTemplateHashLabel: tmplHash,
			},
			Annotations: map[string]string{promotionTriggerDigestAnno: testArtifact().Digest},
		},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef: "checkout",
			Version:  "oci://registry.example.com/checkout@sha256:abcdef1234567890",
		},
	}
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger, existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionCount(t, ctx, c, 1)
	// LastTriggeredAt should NOT have been bumped, since the dedup path returned
	// early before reaching the upsert.
	got := getPromotionTrigger(t, ctx, c, "checkout")
	if got.Status.LastTriggeredAt != "" {
		t.Fatalf("LastTriggeredAt should be empty after dedup, got %q", got.Status.LastTriggeredAt)
	}
}

func TestPromotionTriggerMaxActiveBlocksCreation(t *testing.T) {
	ctx := context.Background()
	// Active PromotionRun under the managed Promotion (label points at it).
	active := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-active",
			Labels: map[string]string{promotionOwnerLabel: "checkout"},
		},
		Status: kaprov1alpha2.PromotionRunStatus{Phase: kaprov1alpha2.PromotionRunPhaseProgressing},
	}
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(), active)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 1)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionUpdated)
	if cond == nil || cond.Reason != "MaxActiveReached" {
		t.Fatalf("PromotionUpdated condition = %+v", cond)
	}
}

func TestPromotionTriggerCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionUpdated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("PromotionUpdated condition = %+v", cond)
	}
}

func TestPromotionTriggerInvalidCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.Cooldown = "soon"
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidCooldown" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerInvalidPollIntervalBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.Source.OCI.PollInterval = "0s"
	})
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidPollInterval" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerExistingPromotionRunCooldownPreventsRapidBypass(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-recent",
			Labels: map[string]string{promotionTriggerLabel: "checkout"},
			Annotations: map[string]string{
				promotionTriggerCreatedAnno: fixedNow().Add(-5 * time.Minute).Format(time.RFC3339),
				promotionTriggerDigestAnno:  "sha256:previous",
			},
		},
		Status: kaprov1alpha2.PromotionRunStatus{Phase: kaprov1alpha2.PromotionRunPhaseComplete},
	}
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Status.LastTriggeredAt = ""
	})
	reconciler, c := newTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger, existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 1)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionUpdated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("PromotionUpdated condition = %+v", cond)
	}
}

func TestPromotionTriggerNameTemplateFailsOnMissingKey(t *testing.T) {
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.PromotionTemplate.NameTemplate = "{{ .Missing.Value }}"
	})

	if _, err := managedPromotionName(trigger); err == nil {
		t.Fatal("expected missing-key template error")
	}
}

func TestPromotionTriggerTagOrderingPrefersSemver(t *testing.T) {
	tags := []string{"v1.2.0", "v1.10.0", "v1.9.9", "v1.2.1"}
	sort.SliceStable(tags, func(i, j int) bool {
		return promotionTriggerTagLess(tags[i], tags[j])
	})
	if got := tags[len(tags)-1]; got != "v1.10.0" {
		t.Fatalf("latest tag = %q, want v1.10.0", got)
	}
}

func TestPromotionTriggerTagOrderingPrefersSemverLikeTags(t *testing.T) {
	tags := []string{"1.2.0", "1.10.0", "1.9.9"}
	sort.SliceStable(tags, func(i, j int) bool {
		return promotionTriggerTagLess(tags[i], tags[j])
	})
	if got := tags[len(tags)-1]; got != "1.10.0" {
		t.Fatalf("latest tag = %q, want 1.10.0", got)
	}
}

func newTriggerReconciler(t *testing.T, resolver PromotionTriggerResolver, verifier PromotionTriggerVerifier, objects ...client.Object) (*TriggerReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha2.Trigger{}, &kaprov1alpha2.PromotionRun{}, &kaprov1alpha2.Promotion{}).
		Build()
	return &TriggerReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
		Resolver: resolver,
		Verifier: verifier,
		Now:      fixedNow,
	}, c
}

func promotionTriggerFixture(mutators ...func(*kaprov1alpha2.Trigger)) *kaprov1alpha2.Trigger {
	rt := &kaprov1alpha2.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Generation: 1},
		Spec: kaprov1alpha2.TriggerSpec{
			Suspended: ptr.To(false),
			Source: kaprov1alpha2.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha2.OCITriggerSource{
					Repository:       "oci://registry.example.com/checkout",
					TagPattern:       "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
					RequireSignature: true,
					PollInterval:     "1m",
				},
			},
			PromotionTemplate: kaprov1alpha2.TriggerTemplate{
				FleetRef:  "checkout",
				Plans:     []kaprov1alpha2.PlanRef{{Name: "prod", Plan: "checkout-prod"}},
				Suspended: ptr.To(true),
				Scope:     &kaprov1alpha2.PromotionRunScope{Targets: []string{"canary-eu"}},
			},
			Cooldown:  "30m",
			MaxActive: 1,
		},
	}
	for _, mutate := range mutators {
		mutate(rt)
	}
	return rt
}

func testArtifact() *TriggerArtifactObservation {
	return &TriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:abcdef1234567890"}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
}

func getPromotionTrigger(t *testing.T, ctx context.Context, c client.Client, name string) kaprov1alpha2.Trigger {
	t.Helper()
	var got kaprov1alpha2.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertPromotionRunCount(t *testing.T, ctx context.Context, c client.Client, want int) {
	t.Helper()
	var promotionruns kaprov1alpha2.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		t.Fatal(err)
	}
	if len(promotionruns.Items) != want {
		t.Fatalf("promotionrun count = %d, want %d", len(promotionruns.Items), want)
	}
}

func assertPromotionCount(t *testing.T, ctx context.Context, c client.Client, want int) {
	t.Helper()
	var promotions kaprov1alpha2.PromotionList
	if err := c.List(ctx, &promotions); err != nil {
		t.Fatal(err)
	}
	if len(promotions.Items) != want {
		t.Fatalf("promotion count = %d, want %d", len(promotions.Items), want)
	}
}

func getManagedPromotion(t *testing.T, ctx context.Context, c client.Client, name string) kaprov1alpha2.Promotion {
	t.Helper()
	var got kaprov1alpha2.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

type fakeTriggerResolver struct {
	artifact *TriggerArtifactObservation
	err      error
	calls    *int
}

func (f *fakeTriggerResolver) Resolve(context.Context, *kaprov1alpha2.Trigger) (*TriggerArtifactObservation, error) {
	if f.calls != nil {
		(*f.calls)++
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.artifact, nil
}

type fakeVerifier struct {
	err error
}

func (f fakeVerifier) Verify(context.Context, *kaprov1alpha2.Trigger, TriggerArtifactObservation) error {
	return f.err
}

func TestPromotionTriggerTagFlipUpdatesManagedPromotion(t *testing.T) {
	ctx := context.Background()
	artifact := &TriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:aaaa"}
	resolver := &fakeTriggerResolver{artifact: artifact}
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.Cooldown = "1m"
	})
	reconciler, c := newTriggerReconciler(t, resolver, fakeVerifier{}, trigger)
	// Advance Now() by 1h between reconciles so cooldown does not block tag flips.
	tick := fixedNow()
	reconciler.Now = func() time.Time {
		t := tick
		tick = tick.Add(time.Hour)
		return t
	}

	// Tag A → managed Promotion stamped with digest A.
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	managed := getManagedPromotion(t, ctx, c, "checkout")
	if !strings.Contains(managed.Spec.Version, "sha256:aaaa") {
		t.Fatalf("after tag A: Version = %q, want digest aaaa", managed.Spec.Version)
	}

	// Tag B → managed Promotion updated to digest B.
	resolver.artifact = &TriggerArtifactObservation{Tag: "v1.2.4", Digest: "sha256:bbbb"}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	managed = getManagedPromotion(t, ctx, c, "checkout")
	if !strings.Contains(managed.Spec.Version, "sha256:bbbb") {
		t.Fatalf("after tag B: Version = %q, want digest bbbb", managed.Spec.Version)
	}

	// Tag A again → managed Promotion updated back to digest A (active is B).
	resolver.artifact = &TriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:aaaa"}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	managed = getManagedPromotion(t, ctx, c, "checkout")
	if !strings.Contains(managed.Spec.Version, "sha256:aaaa") {
		t.Fatalf("after tag A flip back: Version = %q, want digest aaaa", managed.Spec.Version)
	}
}

func TestTriggerTemplateHashStableAndChangeDetectable(t *testing.T) {
	a := promotionTriggerFixture()
	b := promotionTriggerFixture()
	if triggerTemplateHash(a) != triggerTemplateHash(b) {
		t.Fatal("identical templates should hash equal")
	}
	c := promotionTriggerFixture(func(rt *kaprov1alpha2.Trigger) {
		rt.Spec.PromotionTemplate.Timeout = "1h"
	})
	if triggerTemplateHash(a) == triggerTemplateHash(c) {
		t.Fatal("template change (timeout) must produce different hash")
	}
}

func TestPromotionTriggerRecentArtifactsBoundedAndDedupedByDigest(t *testing.T) {
	var list []kaprov1alpha2.TriggerArtifact
	// Same digest + tag twice → coalesced to one entry, refreshed.
	first := kaprov1alpha2.TriggerArtifact{Tag: "v1", Digest: "sha256:a", ObservedAt: "t1"}
	second := kaprov1alpha2.TriggerArtifact{Tag: "v1", Digest: "sha256:a", ObservedAt: "t2"}
	list = recordRecentArtifact(list, first)
	list = recordRecentArtifact(list, second)
	if len(list) != 1 || list[0].ObservedAt != "t2" {
		t.Fatalf("same digest+tag should coalesce; got %+v", list)
	}
	// Different digest → new entry prepended.
	third := kaprov1alpha2.TriggerArtifact{Tag: "v2", Digest: "sha256:b", ObservedAt: "t3"}
	list = recordRecentArtifact(list, third)
	if len(list) != 2 || list[0].Digest != "sha256:b" {
		t.Fatalf("different digest should prepend; got %+v", list)
	}
	// Fill past cap; oldest should fall off.
	for i := 0; i < kaprov1alpha2.MaxRecentArtifacts+5; i++ {
		list = recordRecentArtifact(list, kaprov1alpha2.TriggerArtifact{
			Tag:    "v",
			Digest: fmt.Sprintf("sha256:fill-%d", i),
		})
	}
	if len(list) != kaprov1alpha2.MaxRecentArtifacts {
		t.Fatalf("len = %d, want %d", len(list), kaprov1alpha2.MaxRecentArtifacts)
	}
}

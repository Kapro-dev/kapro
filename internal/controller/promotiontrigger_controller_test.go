package controller

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestPromotionTriggerSuspendedCreatesNothing(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Spec.Suspended = true
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
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
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
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionRunCreated)
	if cond == nil || cond.Reason != "DryRun" {
		t.Fatalf("PromotionRunCreated condition = %+v", cond)
	}
}

func TestPromotionTriggerSignatureFailureBlocksPromotionRun(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{err: errors.New("bad signature")}, promotionTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "SignatureVerificationFailed" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerCreatesDigestPinnedPromotionRun(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var promotionruns kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		t.Fatal(err)
	}
	if len(promotionruns.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(promotionruns.Items))
	}
	promotionrun := promotionruns.Items[0]
	if promotionrun.Spec.Version != "oci://registry.example.com/checkout@sha256:abcdef1234567890" {
		t.Fatalf("Version = %q", promotionrun.Spec.Version)
	}
	if !promotionrun.Spec.Suspended {
		t.Fatal("created PromotionRun should be suspended by default in the template fixture")
	}
	if promotionrun.Labels[promotionTriggerLabel] != "checkout" || promotionrun.Annotations[promotionTriggerDigestAnno] != testArtifact().Digest {
		t.Fatalf("metadata labels=%v annotations=%v", promotionrun.Labels, promotionrun.Annotations)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	if got.Status.LastTriggeredAt == "" || got.Status.ActivePromotionRunCount != 1 {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestPromotionTriggerCreatedAtUsesCreationTime(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture())
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

	var promotionruns kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		t.Fatal(err)
	}
	if len(promotionruns.Items) != 1 {
		t.Fatalf("promotionrun count = %d", len(promotionruns.Items))
	}
	want := createTime.UTC().Format(time.RFC3339)
	if got := promotionruns.Items[0].Annotations[promotionTriggerCreatedAnno]; got != want {
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
	existing := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "checkout-existing",
			Labels:      map[string]string{promotionTriggerLabel: "checkout"},
			Annotations: map[string]string{promotionTriggerDigestAnno: testArtifact().Digest},
		},
		Spec: kaprov1alpha1.PromotionRunSpec{Version: "oci://registry.example.com/checkout@sha256:abcdef1234567890"},
	}
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(), existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 1)
}

func TestPromotionTriggerMaxActiveBlocksCreation(t *testing.T) {
	ctx := context.Background()
	active := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-active", Labels: map[string]string{promotionTriggerLabel: "checkout"}},
		Status:     kaprov1alpha1.PromotionRunStatus{Phase: kaprov1alpha1.PromotionRunPhaseProgressing},
	}
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, promotionTriggerFixture(), active)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 1)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionRunCreated)
	if cond == nil || cond.Reason != "MaxActiveReached" {
		t.Fatalf("PromotionRunCreated condition = %+v", cond)
	}
}

func TestPromotionTriggerCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionRunCreated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("PromotionRunCreated condition = %+v", cond)
	}
}

func TestPromotionTriggerInvalidCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Spec.Cooldown = "soon"
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidCooldown" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerInvalidPollIntervalBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Spec.Source.OCI.PollInterval = "0s"
	})
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidPollInterval" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestPromotionTriggerExistingPromotionRunCooldownPreventsRapidBypass(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-recent",
			Labels: map[string]string{promotionTriggerLabel: "checkout"},
			Annotations: map[string]string{
				promotionTriggerCreatedAnno: fixedNow().Add(-5 * time.Minute).Format(time.RFC3339),
				promotionTriggerDigestAnno:  "sha256:previous",
			},
		},
		Status: kaprov1alpha1.PromotionRunStatus{Phase: kaprov1alpha1.PromotionRunPhaseComplete},
	}
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Status.LastTriggeredAt = ""
	})
	reconciler, c := newPromotionTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger, existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertPromotionRunCount(t, ctx, c, 1)
	got := getPromotionTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionPromotionRunCreated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("PromotionRunCreated condition = %+v", cond)
	}
}

func TestPromotionTriggerNameTemplateFailsOnMissingKey(t *testing.T) {
	trigger := promotionTriggerFixture(func(rt *kaprov1alpha1.PromotionTrigger) {
		rt.Spec.PromotionRunTemplate.NameTemplate = "{{ .Missing.Value }}"
	})

	if _, err := promotionrunName(trigger, *testArtifact()); err == nil {
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

func newPromotionTriggerReconciler(t *testing.T, resolver PromotionTriggerResolver, verifier PromotionTriggerVerifier, objects ...client.Object) (*PromotionTriggerReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha1.PromotionTrigger{}, &kaprov1alpha1.PromotionRun{}).
		Build()
	return &PromotionTriggerReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
		Resolver: resolver,
		Verifier: verifier,
		Now:      fixedNow,
	}, c
}

func promotionTriggerFixture(mutators ...func(*kaprov1alpha1.PromotionTrigger)) *kaprov1alpha1.PromotionTrigger {
	rt := &kaprov1alpha1.PromotionTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Generation: 1},
		Spec: kaprov1alpha1.PromotionTriggerSpec{
			Suspended: false,
			Source: kaprov1alpha1.PromotionTriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIPromotionTriggerSource{
					Repository:       "oci://registry.example.com/checkout",
					TagPattern:       "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
					RequireSignature: true,
					PollInterval:     "1m",
				},
			},
			PromotionRunTemplate: kaprov1alpha1.PromotionTriggerTemplate{
				PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{Name: "prod", PromotionPlan: "checkout-prod"}},
				Suspended:      true,
				Scope:          &kaprov1alpha1.PromotionRunScope{Targets: []string{"canary-eu"}},
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

func testArtifact() *PromotionTriggerArtifactObservation {
	return &PromotionTriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:abcdef1234567890"}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
}

func getPromotionTrigger(t *testing.T, ctx context.Context, c client.Client, name string) kaprov1alpha1.PromotionTrigger {
	t.Helper()
	var got kaprov1alpha1.PromotionTrigger
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertPromotionRunCount(t *testing.T, ctx context.Context, c client.Client, want int) {
	t.Helper()
	var promotionruns kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &promotionruns); err != nil {
		t.Fatal(err)
	}
	if len(promotionruns.Items) != want {
		t.Fatalf("promotionrun count = %d, want %d", len(promotionruns.Items), want)
	}
}

type fakeTriggerResolver struct {
	artifact *PromotionTriggerArtifactObservation
	err      error
	calls    *int
}

func (f *fakeTriggerResolver) Resolve(context.Context, *kaprov1alpha1.PromotionTrigger) (*PromotionTriggerArtifactObservation, error) {
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

func (f fakeVerifier) Verify(context.Context, *kaprov1alpha1.PromotionTrigger, PromotionTriggerArtifactObservation) error {
	return f.err
}

package controller

import (
	"context"
	"errors"
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

func TestReleaseTriggerSuspendedCreatesNothing(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.Suspended = true
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionSuspended)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Suspended condition = %+v", cond)
	}
}

func TestReleaseTriggerDryRunCreatesNothingAndRecordsArtifact(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.DryRun = true
	}))

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	if got.Status.LastArtifact == nil || got.Status.LastArtifact.Digest != testArtifact().Digest {
		t.Fatalf("LastArtifact = %+v", got.Status.LastArtifact)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionReleaseCreated)
	if cond == nil || cond.Reason != "DryRun" {
		t.Fatalf("ReleaseCreated condition = %+v", cond)
	}
}

func TestReleaseTriggerSignatureFailureBlocksRelease(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{err: errors.New("bad signature")}, releaseTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "SignatureVerificationFailed" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestReleaseTriggerCreatesDigestPinnedRelease(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, releaseTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var releases kaprov1alpha1.ReleaseList
	if err := c.List(ctx, &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases.Items) != 1 {
		t.Fatalf("release count = %d", len(releases.Items))
	}
	release := releases.Items[0]
	if release.Spec.Version != "oci://registry.example.com/checkout@sha256:abcdef1234567890" {
		t.Fatalf("Version = %q", release.Spec.Version)
	}
	if !release.Spec.Suspended {
		t.Fatal("created Release should be suspended by default in the template fixture")
	}
	if release.Labels[releaseTriggerLabel] != "checkout" || release.Annotations[releaseTriggerDigestAnno] != testArtifact().Digest {
		t.Fatalf("metadata labels=%v annotations=%v", release.Labels, release.Annotations)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	if got.Status.LastTriggeredAt == "" || got.Status.ActiveReleaseCount != 1 {
		t.Fatalf("status = %+v", got.Status)
	}
}

func TestReleaseTriggerDoesNotDuplicateSameDigest(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "checkout-existing",
			Labels:      map[string]string{releaseTriggerLabel: "checkout"},
			Annotations: map[string]string{releaseTriggerDigestAnno: testArtifact().Digest},
		},
		Spec: kaprov1alpha1.ReleaseSpec{Version: "oci://registry.example.com/checkout@sha256:abcdef1234567890"},
	}
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, releaseTriggerFixture(), existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 1)
}

func TestReleaseTriggerMaxActiveBlocksCreation(t *testing.T) {
	ctx := context.Background()
	active := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-active", Labels: map[string]string{releaseTriggerLabel: "checkout"}},
		Status:     kaprov1alpha1.ReleaseStatus{Phase: kaprov1alpha1.ReleasePhaseProgressing},
	}
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, releaseTriggerFixture(), active)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 1)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionReleaseCreated)
	if cond == nil || cond.Reason != "MaxActiveReached" {
		t.Fatalf("ReleaseCreated condition = %+v", cond)
	}
}

func TestReleaseTriggerCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionReleaseCreated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("ReleaseCreated condition = %+v", cond)
	}
}

func newReleaseTriggerReconciler(t *testing.T, resolver ReleaseTriggerResolver, verifier ReleaseTriggerVerifier, objects ...client.Object) (*ReleaseTriggerReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha1.ReleaseTrigger{}, &kaprov1alpha1.Release{}).
		Build()
	return &ReleaseTriggerReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
		Resolver: resolver,
		Verifier: verifier,
		Now:      fixedNow,
	}, c
}

func releaseTriggerFixture(mutators ...func(*kaprov1alpha1.ReleaseTrigger)) *kaprov1alpha1.ReleaseTrigger {
	rt := &kaprov1alpha1.ReleaseTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Generation: 1},
		Spec: kaprov1alpha1.ReleaseTriggerSpec{
			Suspended: false,
			Source: kaprov1alpha1.ReleaseTriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIReleaseTriggerSource{
					Repository:       "oci://registry.example.com/checkout",
					TagPattern:       "^v[0-9]+\\.[0-9]+\\.[0-9]+$",
					RequireSignature: true,
					PollInterval:     "1m",
				},
			},
			ReleaseTemplate: kaprov1alpha1.ReleaseTriggerTemplate{
				Pipelines: []kaprov1alpha1.ReleasePipelineRef{{Name: "prod", Pipeline: "checkout-prod"}},
				Suspended: true,
				Scope:     &kaprov1alpha1.ReleaseScope{Targets: []string{"canary-eu"}},
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

func testArtifact() *ReleaseTriggerArtifactObservation {
	return &ReleaseTriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:abcdef1234567890"}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
}

func getReleaseTrigger(t *testing.T, ctx context.Context, c client.Client, name string) kaprov1alpha1.ReleaseTrigger {
	t.Helper()
	var got kaprov1alpha1.ReleaseTrigger
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertReleaseCount(t *testing.T, ctx context.Context, c client.Client, want int) {
	t.Helper()
	var releases kaprov1alpha1.ReleaseList
	if err := c.List(ctx, &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases.Items) != want {
		t.Fatalf("release count = %d, want %d", len(releases.Items), want)
	}
}

type fakeTriggerResolver struct {
	artifact *ReleaseTriggerArtifactObservation
	err      error
}

func (f *fakeTriggerResolver) Resolve(context.Context, *kaprov1alpha1.ReleaseTrigger) (*ReleaseTriggerArtifactObservation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.artifact, nil
}

type fakeVerifier struct {
	err error
}

func (f fakeVerifier) Verify(context.Context, *kaprov1alpha1.ReleaseTrigger, ReleaseTriggerArtifactObservation) error {
	return f.err
}

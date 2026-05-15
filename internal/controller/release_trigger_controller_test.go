package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	cosignlib "github.com/sigstore/cosign/v2/pkg/cosign"
	cosignoci "github.com/sigstore/cosign/v2/pkg/oci"
	corev1 "k8s.io/api/core/v1"
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
	verified := apimeta.FindStatusCondition(got.Status.Conditions, conditionArtifactVerified)
	if verified == nil || verified.Status != metav1.ConditionFalse || verified.Reason != "SignatureVerificationFailed" {
		t.Fatalf("ArtifactVerified condition = %+v", verified)
	}
}

func TestReleaseTriggerVerifierUnavailableBlocksRelease(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, nil, releaseTriggerFixture())

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionArtifactVerified)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != "VerifierUnavailable" {
		t.Fatalf("ArtifactVerified condition = %+v", cond)
	}
}

func TestReleaseTriggerCosignKeyVerificationCreatesRelease(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, releaseTriggerFixture(), cosignKeySecret(t))
	reconciler.Verifier = &CosignReleaseTriggerVerifier{
		Client: c,
		VerifyImageSignatures: func(_ context.Context, ref name.Reference, opts *cosignlib.CheckOpts) ([]cosignoci.Signature, bool, error) {
			if got, want := ref.Name(), "registry.example.com/checkout@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"; got != want {
				return nil, false, fmt.Errorf("reference = %q, want %q", got, want)
			}
			if opts.SigVerifier == nil || opts.ClaimVerifier == nil || !opts.IgnoreTlog {
				return nil, false, fmt.Errorf("unexpected cosign check options: %+v", opts)
			}
			return []cosignoci.Signature{nil}, false, nil
		},
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	var releases kaprov1alpha1.ReleaseList
	if err := c.List(ctx, &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases.Items) != 1 {
		t.Fatalf("release count = %d, want 1; status = %+v", len(releases.Items), got.Status)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionArtifactVerified)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "SignatureVerified" {
		t.Fatalf("ArtifactVerified condition = %+v", cond)
	}
	if got.Status.LastArtifact == nil || !got.Status.LastArtifact.SignatureVerified {
		t.Fatalf("LastArtifact = %+v", got.Status.LastArtifact)
	}
}

func TestReleaseTriggerCosignKeyVerificationFailureBlocksRelease(t *testing.T) {
	ctx := context.Background()
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, nil, releaseTriggerFixture(), cosignKeySecret(t))
	reconciler.Verifier = &CosignReleaseTriggerVerifier{
		Client: c,
		VerifyImageSignatures: func(context.Context, name.Reference, *cosignlib.CheckOpts) ([]cosignoci.Signature, bool, error) {
			return nil, false, errors.New("bad signature")
		},
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionArtifactVerified)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "SignatureVerificationFailed" {
		t.Fatalf("ArtifactVerified condition = %+v", cond)
	}
}

func TestReleaseTriggerInvalidTrustConfigBlocksBeforeResolve(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.Verification = nil
	})
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, nil, trigger)
	reconciler.Verifier = &CosignReleaseTriggerVerifier{Client: c}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionArtifactVerified)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidTrustConfig" {
		t.Fatalf("ArtifactVerified condition = %+v", cond)
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
	if release.Spec.Version != "oci://registry.example.com/checkout@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890" {
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
		Spec: kaprov1alpha1.ReleaseSpec{Version: "oci://registry.example.com/checkout@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
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

func TestReleaseTriggerInvalidCooldownBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.Cooldown = "soon"
		rt.Status.LastTriggeredAt = fixedNow().Add(-5 * time.Minute).Format(time.RFC3339)
	})
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidCooldown" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestReleaseTriggerInvalidPollIntervalBlocksCreation(t *testing.T) {
	ctx := context.Background()
	resolveCalls := 0
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.Source.OCI.PollInterval = "0s"
	})
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact(), calls: &resolveCalls}, fakeVerifier{}, trigger)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 0)
	if resolveCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolveCalls)
	}
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if cond == nil || cond.Reason != "InvalidPollInterval" {
		t.Fatalf("Stalled condition = %+v", cond)
	}
}

func TestReleaseTriggerExistingReleaseCooldownPreventsRapidBypass(t *testing.T) {
	ctx := context.Background()
	existing := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "checkout-recent",
			Labels: map[string]string{releaseTriggerLabel: "checkout"},
			Annotations: map[string]string{
				releaseTriggerCreatedAnno: fixedNow().Add(-5 * time.Minute).Format(time.RFC3339),
				releaseTriggerDigestAnno:  "sha256:previous",
			},
		},
		Status: kaprov1alpha1.ReleaseStatus{Phase: kaprov1alpha1.ReleasePhaseComplete},
	}
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Status.LastTriggeredAt = ""
	})
	reconciler, c := newReleaseTriggerReconciler(t, &fakeTriggerResolver{artifact: testArtifact()}, fakeVerifier{}, trigger, existing)

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	assertReleaseCount(t, ctx, c, 1)
	got := getReleaseTrigger(t, ctx, c, "checkout")
	cond := apimeta.FindStatusCondition(got.Status.Conditions, conditionReleaseCreated)
	if cond == nil || cond.Reason != "CooldownActive" {
		t.Fatalf("ReleaseCreated condition = %+v", cond)
	}
}

func TestReleaseTriggerNameTemplateFailsOnMissingKey(t *testing.T) {
	trigger := releaseTriggerFixture(func(rt *kaprov1alpha1.ReleaseTrigger) {
		rt.Spec.ReleaseTemplate.NameTemplate = "{{ .Missing.Value }}"
	})

	if _, err := releaseName(trigger, *testArtifact()); err == nil {
		t.Fatal("expected missing-key template error")
	}
}

func TestReleaseTriggerTagOrderingPrefersSemver(t *testing.T) {
	tags := []string{"v1.2.0", "v1.10.0", "v1.9.9", "v1.2.1"}
	sort.SliceStable(tags, func(i, j int) bool {
		return releaseTriggerTagLess(tags[i], tags[j])
	})
	if got := tags[len(tags)-1]; got != "v1.10.0" {
		t.Fatalf("latest tag = %q, want v1.10.0", got)
	}
}

func TestReleaseTriggerTagOrderingPrefersSemverLikeTags(t *testing.T) {
	tags := []string{"1.2.0", "1.10.0", "1.9.9"}
	sort.SliceStable(tags, func(i, j int) bool {
		return releaseTriggerTagLess(tags[i], tags[j])
	})
	if got := tags[len(tags)-1]; got != "1.10.0" {
		t.Fatalf("latest tag = %q, want 1.10.0", got)
	}
}

func newReleaseTriggerReconciler(t *testing.T, resolver ReleaseTriggerResolver, verifier ReleaseTriggerVerifier, objects ...client.Object) (*ReleaseTriggerReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
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
			Verification: &kaprov1alpha1.VerificationGateSpec{
				CosignPolicy: &kaprov1alpha1.CosignPolicySpec{
					Key: &kaprov1alpha1.KeyVerificationSpec{
						SecretRef: kaprov1alpha1.CosignKeySecretRef{
							Name:      "checkout-cosign",
							Namespace: "kapro-system",
							Key:       "cosign.pub",
						},
					},
				},
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
	return &ReleaseTriggerArtifactObservation{Tag: "v1.2.3", Digest: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}
}

func cosignKeySecret(t *testing.T) *corev1.Secret {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-cosign", Namespace: "kapro-system"},
		Data: map[string][]byte{
			"cosign.pub": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKey}),
		},
	}
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
	calls    *int
}

func (f *fakeTriggerResolver) Resolve(context.Context, *kaprov1alpha1.ReleaseTrigger) (*ReleaseTriggerArtifactObservation, error) {
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

func (f fakeVerifier) Verify(context.Context, *kaprov1alpha1.ReleaseTrigger, ReleaseTriggerArtifactObservation) error {
	return f.err
}

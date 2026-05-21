package flux

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/spokeprovider"
)

func TestDriverReturnsFlux(t *testing.T) {
	p := NewProvider(nil)
	if got, want := p.Driver(), kaprov1alpha2.BackendDriverFlux; got != want {
		t.Fatalf("Driver() = %q, want %q", got, want)
	}
}

func TestSuspendShortCircuits(t *testing.T) {
	p := NewProvider(newFakeClient())
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster: &kaprov1alpha2.Cluster{
			Spec: kaprov1alpha2.ClusterSpec{Suspend: true},
		},
		AppKey:         "default",
		DesiredVersion: "v1.2.3",
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseSkipped {
		t.Fatalf("Phase = %q, want Skipped", res.Phase)
	}
}

func TestEmptyDesiredVersionFails(t *testing.T) {
	p := NewProvider(newFakeClient())
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster: &kaprov1alpha2.Cluster{},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "DesiredVersion is empty") {
		t.Fatalf("Err = %v, want DesiredVersion-empty message", res.Err)
	}
}

func TestMissingOCIRepositoryParamFails(t *testing.T) {
	p := NewProvider(newFakeClient())
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), paramOCIRepositoryName) {
		t.Fatalf("Err = %v, want missing-param message", res.Err)
	}
}

func TestOCIRepositoryNotFoundFails(t *testing.T) {
	p := NewProvider(newFakeClient())
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "missing"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil {
		t.Fatal("Err = nil, want not-found")
	}
}

func TestArtifactUnsetPulling(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "", "", "")
	p := NewProvider(newFakeClient(repo))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhasePulling {
		t.Fatalf("Phase = %q, want Pulling", res.Phase)
	}
}

func TestOCIRepositoryReadyFalseFails(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "False")
	p := NewProvider(newFakeClient(repo))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("Phase = %q, want Failed (got err=%v)", res.Phase, res.Err)
	}
	if res.ObservedDigest != "sha256:abcd" {
		t.Fatalf("ObservedDigest = %q, want sha256:abcd", res.ObservedDigest)
	}
}

func TestOCIRepositoryRevisionMismatchPulling(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	p := NewProvider(newFakeClient(repo))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v2",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhasePulling {
		t.Fatalf("Phase = %q, want Pulling", res.Phase)
	}
	if res.ObservedDigest != "sha256:abcd" {
		t.Fatalf("ObservedDigest = %q, want sha256:abcd", res.ObservedDigest)
	}
}

func TestOCIRepositoryMatchNoHelmReleaseConverged(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	p := NewProvider(newFakeClient(repo))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseConverged {
		t.Fatalf("Phase = %q, want Converged (got err=%v)", res.Phase, res.Err)
	}
	if res.ObservedDigest != "sha256:abcd" {
		t.Fatalf("ObservedDigest = %q", res.ObservedDigest)
	}
	if res.LastAppliedAt.IsZero() {
		t.Fatal("LastAppliedAt should be set when Converged")
	}
}

func TestOCIRepositoryMatchHelmReleaseNotReadyApplying(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	hr := newHelmRelease("flux-system", "app", "")
	p := NewProvider(newFakeClient(repo, hr))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters: map[string]string{
			paramOCIRepositoryName: "bundle",
			paramHelmReleaseName:   "app",
		},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseApplying {
		t.Fatalf("Phase = %q, want Applying", res.Phase)
	}
}

func TestOCIRepositoryMatchHelmReleaseReadyConverged(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	hr := newHelmRelease("flux-system", "app", "True")
	p := NewProvider(newFakeClient(repo, hr))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters: map[string]string{
			paramOCIRepositoryName: "bundle",
			paramHelmReleaseName:   "app",
		},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseConverged {
		t.Fatalf("Phase = %q, want Converged (err=%v)", res.Phase, res.Err)
	}
}

func TestHelmReleaseReadyFalseFails(t *testing.T) {
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	hr := newHelmRelease("flux-system", "app", "False")
	p := NewProvider(newFakeClient(repo, hr))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters: map[string]string{
			paramOCIRepositoryName: "bundle",
			paramHelmReleaseName:   "app",
		},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("Phase = %q, want Failed (err=%v)", res.Phase, res.Err)
	}
}

func TestRevisionMatchesAcceptsTagAtDigestForm(t *testing.T) {
	cases := []struct{ rev, desired string }{
		{"v1.2.3", "v1.2.3"},
		{"v1.2.3@sha256:abcd", "v1.2.3"},
		{"v1.2.3@sha256:abcd", "sha256:abcd"},
		{"v1.2.3@sha256:abcd", "abcd"},
	}
	for _, c := range cases {
		if !revisionMatches(c.rev, c.desired) {
			t.Errorf("revisionMatches(%q, %q) = false, want true", c.rev, c.desired)
		}
	}

	negatives := []struct{ rev, desired string }{
		{"v1.2.3", "v1.2.4"},
		{"v1.2.3@sha256:abcd", "v2"},
		{"", "v1"},
		{"v1", ""},
	}
	for _, c := range negatives {
		if revisionMatches(c.rev, c.desired) {
			t.Errorf("revisionMatches(%q, %q) = true, want false", c.rev, c.desired)
		}
	}
}

func TestProviderRespectsInjectedNow(t *testing.T) {
	pinned := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "True")
	p := &Provider{Local: newFakeClient(repo), Now: func() time.Time { return pinned }}
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if !res.LastAttemptedAt.Equal(pinned) {
		t.Fatalf("LastAttemptedAt = %v, want %v", res.LastAttemptedAt, pinned)
	}
	if !res.LastAppliedAt.Equal(pinned) {
		t.Fatalf("LastAppliedAt = %v, want %v", res.LastAppliedAt, pinned)
	}
}

// ---- test helpers ----

func newFakeClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	// Unstructured clients tolerate unknown GVKs without scheme registration.
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func newOCIRepo(namespace, name, revision, digest, readyStatus string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ociRepositoryGVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	status := map[string]any{}
	if revision != "" || digest != "" {
		status["artifact"] = map[string]any{
			"revision": revision,
			"digest":   digest,
		}
	}
	if readyStatus != "" {
		status["conditions"] = []any{
			map[string]any{
				"type":    "Ready",
				"status":  readyStatus,
				"message": "test",
			},
		}
	}
	if len(status) > 0 {
		_ = unstructuredSet(u.Object, status, "status")
	}
	return u
}

func newHelmRelease(namespace, name, readyStatus string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(helmReleaseGVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	if readyStatus != "" {
		_ = unstructuredSet(u.Object, map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Ready",
					"status":  readyStatus,
					"message": "test",
				},
			},
		}, "status")
	}
	return u
}

func unstructuredSet(obj map[string]any, value any, path ...string) error {
	cur := obj
	for i, key := range path {
		if i == len(path)-1 {
			cur[key] = value
			return nil
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	return nil
}

func TestOCIRepositoryReadyUnknownIsPulling_RegressionGateReview(t *testing.T) {
	// Regression for gate review fix: a matched revision with no Ready
	// condition (or Ready=Unknown) is NOT enough to declare Converged —
	// Flux may still be reconciling.
	repo := newOCIRepo("flux-system", "bundle", "v1", "sha256:abcd", "")
	p := NewProvider(newFakeClient(repo))
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		DesiredVersion: "v1",
		Parameters:     map[string]string{paramOCIRepositoryName: "bundle"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhasePulling {
		t.Fatalf("Phase = %q, want Pulling (Ready not yet True); converged-on-Unknown was the bug being fixed", res.Phase)
	}
}

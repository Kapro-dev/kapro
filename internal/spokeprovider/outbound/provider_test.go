package outbound

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/opencontainers/go-digest"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/delivery"
	"kapro.io/kapro/pkg/spokeprovider"
)

// stubPuller returns a fixed in-memory artifact so the delivery chain can
// run without a registry.
type stubPuller struct {
	called int
	ref    delivery.ArtifactRef
}

func (s *stubPuller) Pull(ctx context.Context, ref delivery.ArtifactRef) (*delivery.PulledArtifact, error) {
	s.called++
	s.ref = ref
	return &delivery.PulledArtifact{
		FS: fstest.MapFS{
			"cm.yaml": &fstest.MapFile{Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: x
  namespace: default
`)},
		},
		Digest:    digest.Digest("sha256:abc123"),
		MediaType: delivery.MediaTypeRawYAML,
	}, nil
}

func TestProvider_SuspendShortCircuit(t *testing.T) {
	pulled := &stubPuller{}
	p := &Provider{
		Delivery: &delivery.Delivery{
			Puller:    pulled,
			Renderers: map[delivery.Format]delivery.Renderer{delivery.FormatRawYAML: delivery.RawYAMLRenderer{}},
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
	cluster := &kaprov1alpha2.Cluster{}
	cluster.Spec.Suspend = true

	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        cluster,
		AppKey:         "x",
		DesiredVersion: "1",
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseSkipped {
		t.Fatalf("phase = %q, want Skipped", res.Phase)
	}
	if pulled.called != 0 {
		t.Fatalf("Puller called on suspended cluster: %d times", pulled.called)
	}
	if res.Err != nil {
		t.Fatalf("Skipped should not carry an error, got %v", res.Err)
	}
	if !res.LastAttemptedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("LastAttemptedAt not stamped from injected clock: %v", res.LastAttemptedAt)
	}
}

func TestProvider_MissingRepositoryFails(t *testing.T) {
	p := &Provider{
		Delivery: &delivery.Delivery{Puller: &stubPuller{}},
	}
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		AppKey:         "x",
		DesiredVersion: "1",
		Parameters:     map[string]string{},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "repository") {
		t.Fatalf("expected repository error, got %v", res.Err)
	}
}

func TestProvider_NilDeliveryFails(t *testing.T) {
	p := &Provider{}
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster:        &kaprov1alpha2.Cluster{},
		AppKey:         "x",
		DesiredVersion: "1",
		Parameters:     map[string]string{ParamRepository: "r.io/x"},
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "Delivery is nil") {
		t.Fatalf("expected nil-delivery error, got %v", res.Err)
	}
}

type scriptedResolver struct {
	ref delivery.ArtifactRef
	err error
}

func (s scriptedResolver) Resolve(ctx context.Context, _ spokeprovider.ReconcileRequest) (delivery.ArtifactRef, error) {
	return s.ref, s.err
}

func TestProvider_ResolverErrorForwarded(t *testing.T) {
	p := &Provider{
		Delivery:    &delivery.Delivery{Puller: &stubPuller{}},
		RefResolver: scriptedResolver{err: errors.New("token rotated")},
	}
	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster: &kaprov1alpha2.Cluster{},
		AppKey:  "x", DesiredVersion: "1",
	})
	if res.Phase != kaprov1alpha2.DeliveryPhaseFailed {
		t.Fatalf("phase = %q, want Failed", res.Phase)
	}
	if res.Err == nil || res.Err.Error() != "token rotated" {
		t.Fatalf("err = %v, want \"token rotated\"", res.Err)
	}
}

func TestProvider_DelegatesToDeliveryAndForwardsResult(t *testing.T) {
	pulled := &stubPuller{}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	spoke := fake.NewClientBuilder().WithScheme(scheme).Build()

	d := &delivery.Delivery{
		Puller:    pulled,
		Renderers: map[delivery.Format]delivery.Renderer{delivery.FormatRawYAML: delivery.RawYAMLRenderer{}},
		Engine:    &delivery.ApplyEngine{Client: spoke},
		Now:       func() time.Time { return time.Unix(1700000123, 0) },
	}
	p := &Provider{
		Delivery: d,
		RefResolver: scriptedResolver{ref: delivery.ArtifactRef{
			Repository: "r.io/x", Tag: "1.0",
		}},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}

	res := p.Reconcile(context.Background(), spokeprovider.ReconcileRequest{
		Cluster: &kaprov1alpha2.Cluster{},
		AppKey:  "x", DesiredVersion: "1.0",
	})

	if res.Phase != kaprov1alpha2.DeliveryPhaseConverged {
		t.Fatalf("phase = %q, want Converged; err=%v", res.Phase, res.Err)
	}
	if res.ObservedDigest != "sha256:abc123" {
		t.Fatalf("digest = %q", res.ObservedDigest)
	}
	if res.AppliedObjects != 1 {
		t.Fatalf("appliedObjects = %d, want 1", res.AppliedObjects)
	}
	if res.Staging == nil {
		t.Fatal("staging status not forwarded")
	}
	if res.Staging.StagedObjects != 1 || res.Staging.CommittedObjects != 1 {
		t.Fatalf("staging counts = %+v, want staged=1 committed=1", res.Staging)
	}
	if res.Format != string(delivery.FormatRawYAML) {
		t.Fatalf("format = %q, want raw-yaml", res.Format)
	}
	if !res.LastAppliedAt.Equal(time.Unix(1700000123, 0)) {
		t.Fatalf("LastAppliedAt not forwarded from inner delivery: %v", res.LastAppliedAt)
	}
	if pulled.called != 1 || pulled.ref.Repository != "r.io/x" || pulled.ref.Tag != "1.0" {
		t.Fatalf("Puller called with wrong ref: %+v (called=%d)", pulled.ref, pulled.called)
	}
}

func TestProvider_Driver(t *testing.T) {
	p := &Provider{}
	if p.Driver() != kaprov1alpha2.BackendDriverOCI {
		t.Fatalf("Driver() = %q, want oci", p.Driver())
	}
}

func TestProvider_CapabilitiesAdvertiseDryRun(t *testing.T) {
	p := &Provider{}
	caps := p.Capabilities()
	if !caps.SupportsDryRun {
		t.Fatal("SupportsDryRun=false, want true for OCI two-phase delivery")
	}
}

package delivery

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/opencontainers/go-digest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// fakePuller serves a hand-crafted PulledArtifact, optionally erroring.
type fakePuller struct {
	pa  *PulledArtifact
	err error
}

func (f *fakePuller) Pull(ctx context.Context, ref ArtifactRef) (*PulledArtifact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pa, nil
}

func newRawArtifact(t *testing.T) *PulledArtifact {
	t.Helper()
	return &PulledArtifact{
		FS: fstest.MapFS{
			"10-ns.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: Namespace
metadata:
  name: demo
`)},
			"20-cm.yaml": &fstest.MapFile{Data: []byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: demo
data:
  key: value
`)},
		},
		Digest:    digest.FromString("test-payload"),
		MediaType: MediaTypeRawYAML,
	}
}

func newDeliveryFixture(t *testing.T, puller Puller) *Delivery {
	t.Helper()
	sch := runtime.NewScheme()
	_ = corev1.AddToScheme(sch)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	pinned := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	return &Delivery{
		Puller: puller,
		Renderers: map[Format]Renderer{
			FormatRawYAML: RawYAMLRenderer{},
		},
		Engine: &ApplyEngine{Client: c},
		Now:    func() time.Time { return pinned },
	}
}

func TestDelivery_Reconcile_HappyPath(t *testing.T) {
	pa := newRawArtifact(t)
	d := newDeliveryFixture(t, &fakePuller{pa: pa})
	res := d.Reconcile(context.Background(), ReconcileRequest{
		App: "demo",
		Ref: ArtifactRef{Repository: "test.example.com/demo", Tag: "v1"},
	})
	if res.Err != nil {
		t.Fatalf("err: %v", res.Err)
	}
	if res.Phase != string(kaprov1alpha2.DeliveryPhaseConverged) {
		t.Fatalf("phase=%s, want Converged", res.Phase)
	}
	if res.Format != FormatRawYAML {
		t.Fatalf("format=%s, want raw-yaml", res.Format)
	}
	if res.AppliedObjects != 2 {
		t.Fatalf("appliedObjects=%d, want 2", res.AppliedObjects)
	}
	if res.ObservedDigest != pa.Digest.String() {
		t.Fatalf("digest=%s, want %s", res.ObservedDigest, pa.Digest)
	}
	if res.LastAppliedAt.IsZero() {
		t.Fatal("LastAppliedAt should be set on success")
	}
}

func TestDelivery_Reconcile_PullErrorIsTerminal(t *testing.T) {
	d := newDeliveryFixture(t, &fakePuller{err: errors.New("network down")})
	res := d.Reconcile(context.Background(), ReconcileRequest{
		App: "demo",
		Ref: ArtifactRef{Repository: "test.example.com/demo", Tag: "v1"},
	})
	if res.Err == nil {
		t.Fatal("expected error")
	}
	if res.Phase != string(kaprov1alpha2.DeliveryPhaseFailed) {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
	if !res.LastAppliedAt.IsZero() {
		t.Fatal("LastAppliedAt should NOT be set on pull error")
	}
}

func TestDelivery_Reconcile_UnregisteredFormatFails(t *testing.T) {
	pa := &PulledArtifact{
		FS:        fstest.MapFS{"Chart.yaml": &fstest.MapFile{Data: []byte("apiVersion: v2\nname: x\n")}},
		MediaType: MediaTypeHelmChartContent,
		Digest:    digest.FromString("helm-chart"),
	}
	d := newDeliveryFixture(t, &fakePuller{pa: pa})
	res := d.Reconcile(context.Background(), ReconcileRequest{App: "demo", Ref: ArtifactRef{Repository: "r", Tag: "v1"}})
	if res.Err == nil {
		t.Fatal("expected error for missing renderer")
	}
	if res.Format != FormatHelm {
		t.Fatalf("format=%s, want helm (detected even though renderer missing)", res.Format)
	}
}

func TestDelivery_Reconcile_ZeroObjectsIsFailure(t *testing.T) {
	pa := &PulledArtifact{
		FS:        fstest.MapFS{".gitkeep": &fstest.MapFile{Data: []byte("")}, "_doc.yaml": &fstest.MapFile{Data: []byte("# just a comment\n")}},
		MediaType: MediaTypeRawYAML,
		Digest:    digest.FromString("empty"),
	}
	d := newDeliveryFixture(t, &fakePuller{pa: pa})
	res := d.Reconcile(context.Background(), ReconcileRequest{App: "demo", Ref: ArtifactRef{Repository: "r", Tag: "v1"}})
	if res.Err == nil {
		t.Fatal("expected error for zero-object render")
	}
	if res.Phase != string(kaprov1alpha2.DeliveryPhaseFailed) {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
}

func TestDelivery_Reconcile_PartiallyConstructedNoPanic(t *testing.T) {
	cases := []struct {
		name string
		d    *Delivery
		want string
	}{
		{"nil receiver", nil, "nil Delivery"},
		{"nil Puller", &Delivery{Engine: &ApplyEngine{}, Renderers: map[Format]Renderer{}}, "Puller is nil"},
		{"nil Engine", &Delivery{Puller: &fakePuller{}, Renderers: map[Format]Renderer{}}, "Engine is nil"},
		{"nil Renderers", &Delivery{Puller: &fakePuller{}, Engine: &ApplyEngine{}}, "Renderers is nil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.d.Reconcile(context.Background(), ReconcileRequest{App: "x"})
			if res.Err == nil {
				t.Fatal("expected error")
			}
			if res.Phase != string(kaprov1alpha2.DeliveryPhaseFailed) {
				t.Fatalf("phase=%s, want Failed", res.Phase)
			}
			if !contains(res.Err.Error(), tc.want) {
				t.Fatalf("err=%q does not contain %q", res.Err.Error(), tc.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDelivery_RegisterRenderer(t *testing.T) {
	d := newDeliveryFixture(t, &fakePuller{pa: newRawArtifact(t)})
	stub := stubRenderer{}
	d.RegisterRenderer(FormatHelm, stub)
	if _, ok := d.Renderers[FormatHelm]; !ok {
		t.Fatal("renderer not registered")
	}
}

type stubRenderer struct{}

func (stubRenderer) Render(ctx context.Context, pa *PulledArtifact, _ RenderOptions) (RenderedManifests, error) {
	return RenderedManifests{}, nil
}

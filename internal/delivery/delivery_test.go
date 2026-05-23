package delivery

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/opencontainers/go-digest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	if res.Staging == nil {
		t.Fatal("staging status not populated")
	}
	if res.Staging.StagedObjects != 2 || res.Staging.CommittedObjects != 2 {
		t.Fatalf("staging counts = staged %d committed %d, want 2/2", res.Staging.StagedObjects, res.Staging.CommittedObjects)
	}
	if res.Staging.FailurePhase != "" {
		t.Fatalf("failurePhase = %q, want empty on success", res.Staging.FailurePhase)
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
	if res.Staging == nil || res.Staging.FailurePhase != kaprov1alpha2.DeliveryPhasePulling {
		t.Fatalf("staging failurePhase = %+v, want Pulling", res.Staging)
	}
}

func TestDelivery_Reconcile_StagingFailureReportsDiagnostics(t *testing.T) {
	c := applyInterceptClient(t, func(_ context.Context, obj client.Object, opts ...client.PatchOption) error {
		po := &client.PatchOptions{}
		po.ApplyOptions(opts)
		if len(po.DryRun) > 0 && objectKind(obj) == "ConfigMap" {
			return apierrors.NewInvalid(schema.GroupKind{Kind: "ConfigMap"}, obj.GetName(), nil)
		}
		return nil
	})
	d := newDeliveryFixture(t, &fakePuller{pa: newRawArtifact(t)})
	d.Engine = &ApplyEngine{Client: c}

	res := d.Reconcile(context.Background(), ReconcileRequest{App: "demo", Ref: ArtifactRef{Repository: "r", Tag: "v1"}})
	if res.Err == nil {
		t.Fatal("expected staging error")
	}
	if res.Phase != string(kaprov1alpha2.DeliveryPhaseFailed) {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
	if res.Staging == nil {
		t.Fatal("staging status not populated")
	}
	if res.Staging.FailurePhase != kaprov1alpha2.DeliveryPhaseStaging {
		t.Fatalf("failurePhase=%q, want Staging", res.Staging.FailurePhase)
	}
	if res.Staging.StagedObjects != 1 || res.Staging.StagingFailedObjects != 1 || res.Staging.CommittedObjects != 0 {
		t.Fatalf("staging counts = %+v, want staged=1 failed=1 committed=0", res.Staging)
	}
	if res.AppliedObjects != 0 {
		t.Fatalf("appliedObjects=%d, want 0", res.AppliedObjects)
	}
}

func TestDelivery_Reconcile_CommitFailureReportsDiagnostics(t *testing.T) {
	c := applyInterceptClient(t, func(_ context.Context, obj client.Object, opts ...client.PatchOption) error {
		po := &client.PatchOptions{}
		po.ApplyOptions(opts)
		if len(po.DryRun) == 0 && objectKind(obj) == "ConfigMap" {
			return errors.New("apiserver write timeout")
		}
		return nil
	})
	d := newDeliveryFixture(t, &fakePuller{pa: newRawArtifact(t)})
	d.Engine = &ApplyEngine{Client: c}

	res := d.Reconcile(context.Background(), ReconcileRequest{App: "demo", Ref: ArtifactRef{Repository: "r", Tag: "v1"}})
	if res.Err == nil {
		t.Fatal("expected commit error")
	}
	if res.Staging == nil {
		t.Fatal("staging status not populated")
	}
	if res.Staging.FailurePhase != kaprov1alpha2.DeliveryPhaseApplying {
		t.Fatalf("failurePhase=%q, want Applying", res.Staging.FailurePhase)
	}
	if res.Staging.StagedObjects != 2 || res.Staging.CommittedObjects != 1 || res.Staging.CommitFailedObjects != 1 {
		t.Fatalf("staging counts = %+v, want staged=2 committed=1 commitFailed=1", res.Staging)
	}
	if res.AppliedObjects != 1 {
		t.Fatalf("appliedObjects=%d, want 1", res.AppliedObjects)
	}
}

func TestDelivery_Reconcile_ApplyEngineSetupErrorReportsFailurePhase(t *testing.T) {
	d := newDeliveryFixture(t, &fakePuller{pa: newRawArtifact(t)})
	d.Engine = &ApplyEngine{}

	res := d.Reconcile(context.Background(), ReconcileRequest{App: "demo", Ref: ArtifactRef{Repository: "r", Tag: "v1"}})
	if res.Err == nil {
		t.Fatal("expected apply setup error")
	}
	if res.Staging == nil {
		t.Fatal("staging status not populated")
	}
	if res.Staging.FailurePhase != kaprov1alpha2.DeliveryPhaseStaging {
		t.Fatalf("failurePhase=%q, want Staging", res.Staging.FailurePhase)
	}
}

func applyInterceptClient(t *testing.T, patch func(context.Context, client.Object, ...client.PatchOption) error) client.Client {
	t.Helper()
	sch := runtime.NewScheme()
	_ = corev1.AddToScheme(sch)
	return fake.NewClientBuilder().WithScheme(sch).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if err := patch(ctx, obj, opts...); err != nil {
					return err
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
}

func objectKind(obj client.Object) string {
	if obj == nil {
		return ""
	}
	if k := obj.GetObjectKind().GroupVersionKind().Kind; k != "" {
		return k
	}
	if typed, ok := obj.(interface{ GetKind() string }); ok {
		return typed.GetKind()
	}
	return ""
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
	if res.Staging == nil || res.Staging.FailurePhase != kaprov1alpha2.DeliveryPhaseStaging {
		t.Fatalf("staging = %+v, want Staging failure", res.Staging)
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

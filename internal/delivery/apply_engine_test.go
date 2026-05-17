package delivery

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newObj(kind, ns, name string, data map[string]string) *Object {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(ns)
	if data != nil {
		d := map[string]any{}
		for k, v := range data {
			d[k] = v
		}
		u.Object["data"] = d
	}
	return FromUnstructured(u, "test")
}

func newFakeClient(t *testing.T) client.Client {
	t.Helper()
	sch := runtime.NewScheme()
	_ = corev1.AddToScheme(sch)
	return fake.NewClientBuilder().WithScheme(sch).Build()
}

func TestApplyEngine_HappyPath(t *testing.T) {
	c := newFakeClient(t)
	eng := &ApplyEngine{Client: c}

	objs := []*Object{
		newObj("ConfigMap", "default", "a", map[string]string{"k": "v"}),
		newObj("ConfigMap", "default", "b", map[string]string{"k": "v"}),
	}

	res, err := eng.Apply(context.Background(), objs)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Succeeded() {
		t.Fatalf("not succeeded: %+v", res)
	}
	if res.Committed != 2 {
		t.Fatalf("committed=%d, want 2", res.Committed)
	}
}

func TestApplyEngine_StagingFailureAbortsCommit(t *testing.T) {
	// Inject an Invalid error on dry-run for the second object; the engine
	// must NOT commit the first object even though its dry-run succeeded.
	committed := map[string]bool{}
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				po := &client.PatchOptions{}
				po.ApplyOptions(opts)
				dryRun := len(po.DryRun) > 0
				if obj.GetName() == "bad" {
					return apierrors.NewInvalid(
						schema.GroupKind{Kind: "ConfigMap"},
						"bad",
						nil,
					)
				}
				if !dryRun {
					committed[obj.GetName()] = true
				}
				return nil
			},
		}).Build()

	eng := &ApplyEngine{Client: c}
	objs := []*Object{
		newObj("ConfigMap", "ns", "good", map[string]string{"k": "v"}),
		newObj("ConfigMap", "ns", "bad", map[string]string{"k": "v"}),
	}
	res, err := eng.Apply(context.Background(), objs)
	if err == nil {
		t.Fatal("expected staging error")
	}
	if res.Committed != 0 {
		t.Fatalf("committed=%d on staging failure, want 0", res.Committed)
	}
	if len(committed) != 0 {
		t.Fatalf("commit happened despite staging failure: %v", committed)
	}
	if len(res.StagingErrors) != 1 {
		t.Fatalf("staging errors=%d, want 1", len(res.StagingErrors))
	}
	if res.StagingErrors[0].Key == "" {
		t.Fatal("staging error missing key")
	}
}

func TestApplyEngine_CommitFailureSurfacesError(t *testing.T) {
	failed := false
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				po := &client.PatchOptions{}
				po.ApplyOptions(opts)
				dryRun := len(po.DryRun) > 0
				if !dryRun && obj.GetName() == "flaky" && !failed {
					failed = true
					return errors.New("transient network blip")
				}
				return nil
			},
		}).Build()

	eng := &ApplyEngine{Client: c}
	objs := []*Object{
		newObj("ConfigMap", "ns", "ok", nil),
		newObj("ConfigMap", "ns", "flaky", nil),
	}
	res, err := eng.Apply(context.Background(), objs)
	if err == nil {
		t.Fatal("expected commit error")
	}
	if res.Committed != 1 {
		t.Fatalf("committed=%d, want 1 (the non-flaky object commits before the flaky one fails)", res.Committed)
	}
	if len(res.CommitErrors) != 1 {
		t.Fatalf("commit errors=%d, want 1", len(res.CommitErrors))
	}
}

func TestApplyEngine_EmptyInputReturnsZero(t *testing.T) {
	eng := &ApplyEngine{Client: newFakeClient(t)}
	res, err := eng.Apply(context.Background(), nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Committed != 0 || res.Staged != 0 {
		t.Fatalf("non-zero result for empty input: %+v", res)
	}
}

func TestApplyEngine_NilClientErrors(t *testing.T) {
	eng := &ApplyEngine{}
	if _, err := eng.Apply(context.Background(), []*Object{newObj("ConfigMap", "ns", "x", nil)}); err == nil {
		t.Fatal("expected error for nil client")
	}
}

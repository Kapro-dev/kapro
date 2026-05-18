package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestStatusUpdateWithRetry_SuccessFirstTry(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kaprov1alpha1.AddToScheme(scheme)
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c-a"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).
		WithObjects(fc).Build()

	err := StatusUpdateWithRetry(context.Background(), c, fc, func(f *kaprov1alpha1.FleetCluster) error {
		f.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var refreshed kaprov1alpha1.FleetCluster
	_ = c.Get(context.Background(), client.ObjectKey{Name: "c-a"}, &refreshed)
	if refreshed.Status.Phase != kaprov1alpha1.ClusterPhaseConverged {
		t.Fatalf("Phase = %q, want Converged", refreshed.Status.Phase)
	}
}

func TestStatusUpdateWithRetry_RetriesOnConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kaprov1alpha1.AddToScheme(scheme)
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c-b"},
	}
	calls := 0
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).
		WithObjects(fc).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cli client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				calls++
				if calls < 3 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "kapro.io", Resource: "fleetclusters"},
						o.GetName(),
						nil,
					)
				}
				return cli.Status().Update(ctx, o, opts...)
			},
		}).Build()

	err := StatusUpdateWithRetry(context.Background(), c, fc, func(f *kaprov1alpha1.FleetCluster) error {
		f.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after conflict retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 conflicts + 1 success)", calls)
	}
}

func TestStatusUpdateWithRetry_GivesUpAfterMaxRetries(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = kaprov1alpha1.AddToScheme(scheme)
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c-c"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).
		WithObjects(fc).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cli client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				return apierrors.NewConflict(
					schema.GroupResource{Group: "kapro.io", Resource: "fleetclusters"},
					o.GetName(),
					nil,
				)
			},
		}).Build()

	err := StatusUpdateWithRetry(context.Background(), c, fc, func(f *kaprov1alpha1.FleetCluster) error {
		f.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
		return nil
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if !apierrors.IsConflict(err) && !strings.Contains(err.Error(), "lost") {
		t.Fatalf("err = %v, want wrapped conflict", err)
	}
}

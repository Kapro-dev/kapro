package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/decisiontrace"
)

func TestPromotionRunSuspendedEmitsDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "run-a",
			Finalizers: []string{promotionrunFinalizer},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{Suspended: true},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithObjects(run).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var traces kaprov1alpha2.DecisionTraceList
	if err := c.List(context.Background(), &traces); err != nil {
		t.Fatalf("List traces: %v", err)
	}
	if len(traces.Items) != 1 {
		t.Fatalf("trace count = %d, want 1", len(traces.Items))
	}
	trace := traces.Items[0]
	if trace.Spec.EventType != kaprov1alpha2.DecisionTraceEventSuspend ||
		trace.Spec.PromotionRun != "run-a" ||
		trace.Spec.Source != "promotionrun-controller" {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
}

func TestPromotionRunSuspendedTraceFailureDoesNotBlockReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "run-a",
			Finalizers: []string{promotionrunFinalizer},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{Suspended: true},
	}
	boom := errors.New("trace sink down")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kaprov1alpha2.DecisionTrace); ok {
					return boom
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile should ignore trace create failure, got %v", err)
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestWaitForBootstrapSecretReturnsStalledCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("kapro AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&kaprov1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01", Generation: 2},
			Status: kaprov1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               kaprov1alpha1.ConditionTypeStalled,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 2,
					Reason:             "BootstrapExpired",
					Message:            "bootstrap slot expired",
				}},
			},
		}).
		WithStatusSubresource(&kaprov1alpha1.Cluster{}).
		Build()

	_, err := waitForBootstrapSecret(context.Background(), c, "de-prod-01", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected stalled bootstrap error")
	}
	if !strings.Contains(err.Error(), "cluster bootstrap stalled: BootstrapExpired") {
		t.Fatalf("error = %q, want stalled reason", err)
	}
}

func TestWaitForBootstrapSecretIgnoresStaleStalledCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("kapro AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&kaprov1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01", Generation: 3},
			Status: kaprov1alpha1.ClusterStatus{
				Conditions: []metav1.Condition{{
					Type:               kaprov1alpha1.ConditionTypeStalled,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 2,
					Reason:             "BootstrapExpired",
					Message:            "old bootstrap slot expired",
				}},
			},
		}).
		WithStatusSubresource(&kaprov1alpha1.Cluster{}).
		Build()

	_, err := waitForBootstrapSecret(context.Background(), c, "de-prod-01", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout while stale stalled condition is ignored")
	}
	if strings.Contains(err.Error(), "cluster bootstrap stalled") {
		t.Fatalf("error = %q, stale stalled condition should be ignored", err)
	}
}

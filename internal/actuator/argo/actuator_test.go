package argo

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

func TestApplyRequestsHardRefreshAndSyncOperation(t *testing.T) {
	ctx := context.Background()
	app := newArgoApplication("argocd", "checkout", "old")
	c := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		WithObjects(app).
		Build()
	act := &Actuator{Client: c}
	cluster := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{
				Parameters: map[string]string{
					"namespace":   "argocd",
					"application": "checkout",
				},
			},
		},
	}

	if err := act.Apply(ctx, actuator.ApplyRequest{Cluster: cluster, Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	var updated unstructured.Unstructured
	updated.SetGroupVersionKind(applicationGVR.GroupVersion().WithKind("Application"))
	if err := c.Get(ctx, client.ObjectKey{Namespace: "argocd", Name: "checkout"}, &updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.GetAnnotations()["argocd.argoproj.io/refresh"]; got != "hard" {
		t.Fatalf("refresh annotation=%q", got)
	}
	if got, _, _ := unstructured.NestedString(updated.Object, "spec", "source", "targetRevision"); got != "v1.2.3" {
		t.Fatalf("targetRevision=%q", got)
	}
	username, _, _ := unstructured.NestedString(updated.Object, "operation", "initiatedBy", "username")
	if username != "kapro-controller" {
		t.Fatalf("operation username=%q", username)
	}
	if _, ok, _ := unstructured.NestedMap(updated.Object, "operation", "sync"); !ok {
		t.Fatal("operation.sync was not set")
	}
}

func newArgoApplication(namespace, name, revision string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"source": map[string]any{
					"repoURL":        "https://example.com/repo.git",
					"targetRevision": revision,
					"path":           "apps/checkout",
				},
			},
		},
	}
	app.SetGroupVersionKind(applicationGVR.GroupVersion().WithKind("Application"))
	return app
}

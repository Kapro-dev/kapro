package direct

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/actuator"
)

func TestApplyUpdatesDeploymentImage(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()
	act := &Actuator{Client: c}
	cluster := directCluster("checkout")

	if err := act.Apply(ctx, actuator.ApplyRequest{Cluster: cluster, Version: "ghcr.io/example/checkout:0.1.1"}); err != nil {
		t.Fatal(err)
	}
	var got appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	if image := got.Spec.Template.Spec.Containers[0].Image; image != "ghcr.io/example/checkout:0.1.1" {
		t.Fatalf("image=%q", image)
	}
}

func TestApplyDeltaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.1")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()
	act := &Actuator{Client: c}

	changed, err := act.ApplyDelta(ctx, actuator.DeltaApplyRequest{
		Cluster:         directCluster("checkout"),
		DesiredVersions: map[string]string{"default": "ghcr.io/example/checkout:0.1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("changed=%d, want 0", changed)
	}
}

func TestApplyDeltaSupportsEmptyDefaultAppKey(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()
	act := &Actuator{Client: c}

	changed, err := act.ApplyDelta(ctx, actuator.DeltaApplyRequest{
		Cluster:         directCluster("checkout"),
		DesiredVersions: map[string]string{"": "ghcr.io/example/checkout:0.1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed=%d, want 1", changed)
	}
	var got appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	if image := got.Spec.Template.Spec.Containers[0].Image; image != "ghcr.io/example/checkout:0.1.1" {
		t.Fatalf("image=%q", image)
	}
}

func TestIsConvergedRequiresImageAndAvailability(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.1")
	deployment.Generation = 2
	deployment.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 2,
		Replicas:           2,
		UpdatedReplicas:    2,
		ReadyReplicas:      2,
		AvailableReplicas:  2,
		Conditions:         []appsv1.DeploymentCondition{AvailableCondition()},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()
	act := &Actuator{Client: c}

	ok, err := act.IsConverged(ctx, directCluster("checkout"), "ghcr.io/example/checkout:0.1.1", "default")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected converged deployment")
	}
}

func TestIsConvergedWaitsForUpdatedReplicas(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.1")
	deployment.Generation = 2
	deployment.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 2,
		Replicas:           2,
		UpdatedReplicas:    1,
		ReadyReplicas:      2,
		AvailableReplicas:  2,
		Conditions:         []appsv1.DeploymentCondition{AvailableCondition()},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment).
		WithStatusSubresource(&appsv1.Deployment{}).
		Build()
	act := &Actuator{Client: c}

	ok, err := act.IsConverged(ctx, directCluster("checkout"), "ghcr.io/example/checkout:0.1.1", "default")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected rolling deployment to remain unconverged until updated replicas are ready")
	}
}

func TestBackendObjectsReportsDeploymentStatus(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	deployment := testDeployment("default", "checkout", "ghcr.io/example/checkout:0.1.1")
	deployment.Status = appsv1.DeploymentStatus{AvailableReplicas: 2}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()
	act := &Actuator{Client: c}

	statuses, err := act.BackendObjects(ctx, directCluster("checkout"), map[string]string{"default": "ghcr.io/example/checkout:0.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses=%d", len(statuses))
	}
	if statuses[0].Kind != "Deployment" || statuses[0].Name != "checkout" || statuses[0].CurrentVersion != "ghcr.io/example/checkout:0.1.1" {
		t.Fatalf("status=%#v", statuses[0])
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func directCluster(deployment string) *kaprov1alpha2.Cluster {
	return &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "canary-eu"},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode:       kaprov1alpha2.DeliveryModePush,
				BackendRef: "direct",
				Parameters: map[string]string{
					"namespace":  "default",
					"deployment": deployment,
					"container":  "app",
				},
			},
		},
	}
}

func testDeployment(namespace, name, image string) *appsv1.Deployment {
	replicas := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: image,
					}},
				},
			},
		},
	}
}

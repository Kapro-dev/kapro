package flux_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/internal/actuator/flux"
)

// Compile-time assertion: FluxActuator must satisfy actuator.Actuator.
var _ actuator.Actuator = (*flux.FluxActuator)(nil)

func fluxScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestFluxActuator_ImplementsInterface passes if the file compiles (the var _ assertion above).
func TestFluxActuator_ImplementsInterface(t *testing.T) {
	t.Log("FluxActuator satisfies actuator.Actuator at compile time")
}

func TestFluxActuator_Apply_NoCluster(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	err := a.Apply(context.Background(), actuator.ApplyRequest{
		Cluster: nil,
		Version: "v1.0.0",
	})
	if err == nil {
		t.Error("expected error when Cluster is nil")
	}
}

func TestFluxActuator_Apply_AlreadyAtDesiredVersion(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			DesiredVersion: "v1.0.0",
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	if err := a.Apply(context.Background(), actuator.ApplyRequest{
		Cluster: mc,
		Version: "v1.0.0",
	}); err != nil {
		t.Fatalf("unexpected error when version already set: %v", err)
	}
}

func TestFluxActuator_Apply_PatchesDesiredVersion(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			DesiredVersion: "v1.0.0",
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	if err := a.Apply(context.Background(), actuator.ApplyRequest{
		Cluster: mc,
		Version: "v2.0.0",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kaprov1alpha1.MemberCluster
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "de-prod"}, &updated); err != nil {
		t.Fatalf("get updated MemberCluster: %v", err)
	}
	if updated.Spec.DesiredVersion != "v2.0.0" {
		t.Errorf("expected DesiredVersion=v2.0.0, got %s", updated.Spec.DesiredVersion)
	}
}

func TestFluxActuator_IsConverged_StaleHeartbeat_ReturnsError(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-stale"},
		Status: kaprov1alpha1.MemberClusterStatus{
			LastHeartbeat: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithStatusSubresource(mc).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	_, err := a.IsConverged(context.Background(), mc, "v1.0.0", "default")
	if err == nil {
		t.Error("expected error for stale heartbeat")
	}
}

func TestFluxActuator_IsConverged_FreshHeartbeat_MatchingVersion(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-conv"},
		Status: kaprov1alpha1.MemberClusterStatus{
			LastHeartbeat:   time.Now().UTC().Format(time.RFC3339),
			Phase:           kaprov1alpha1.ClusterPhaseConverged,
			CurrentVersions: map[string]string{"default": "v2.0.0"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	converged, err := a.IsConverged(context.Background(), mc, "v2.0.0", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !converged {
		t.Error("expected converged=true")
	}
}

func TestFluxActuator_IsConverged_FreshHeartbeat_WrongVersion(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-wrong"},
		Status: kaprov1alpha1.MemberClusterStatus{
			LastHeartbeat:   time.Now().UTC().Format(time.RFC3339),
			Phase:           kaprov1alpha1.ClusterPhaseConverged,
			CurrentVersions: map[string]string{"default": "v1.0.0"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	converged, err := a.IsConverged(context.Background(), mc, "v2.0.0", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if converged {
		t.Error("expected converged=false when current version doesn't match desired")
	}
}

func TestFluxActuator_Rollback_SetsDesiredVersionToPrevious(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-rbk"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			DesiredVersion: "v2.0.0",
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(mc).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	if err := a.Rollback(context.Background(), mc, "v1.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kaprov1alpha1.MemberCluster
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Name: "de-rbk"}, &updated)
	if updated.Spec.DesiredVersion != "v1.0.0" {
		t.Errorf("expected rollback to set DesiredVersion=v1.0.0, got %s", updated.Spec.DesiredVersion)
	}
}

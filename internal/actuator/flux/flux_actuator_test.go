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

func TestFluxActuator_Apply_NoRegistration(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-dev"},
	}
	err := a.Apply(context.Background(), actuator.ApplyRequest{
		Environment: env,
		Version:     "v1.0.0",
	})
	if err == nil {
		t.Error("expected error when no ClusterRegistration exists for environment")
	}
}

func TestFluxActuator_Apply_AlreadyAtDesiredVersion(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-dev",
			Labels: map[string]string{"kapro.io/environment": "env-dev"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-dev",
			DesiredVersion: "v1.0.0", // already at desired version
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-dev"},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	if err := a.Apply(context.Background(), actuator.ApplyRequest{
		Environment: env,
		Version:     "v1.0.0",
	}); err != nil {
		t.Fatalf("unexpected error when version already set: %v", err)
	}
}

func TestFluxActuator_Apply_PatchesDesiredVersion(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-prod",
			Labels: map[string]string{"kapro.io/environment": "env-prod"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-prod",
			DesiredVersion: "v1.0.0",
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-prod"},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	if err := a.Apply(context.Background(), actuator.ApplyRequest{
		Environment: env,
		Version:     "v2.0.0",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kaprov1alpha1.ClusterRegistration
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "reg-prod"}, &updated); err != nil {
		t.Fatalf("get updated registration: %v", err)
	}
	if updated.Spec.DesiredVersion != "v2.0.0" {
		t.Errorf("expected DesiredVersion=v2.0.0, got %s", updated.Spec.DesiredVersion)
	}
}

func TestFluxActuator_IsConverged_StaleHeartbeat_ReturnsError(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-stale",
			Labels: map[string]string{"kapro.io/environment": "env-stale"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-stale",
		},
		Status: kaprov1alpha1.ClusterRegistrationStatus{
			LastHeartbeat: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env-stale"}}
	_, err := a.IsConverged(context.Background(), env, "v1.0.0")
	if err == nil {
		t.Error("expected error for stale heartbeat")
	}
}

func TestFluxActuator_IsConverged_FreshHeartbeat_MatchingVersion(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-conv",
			Labels: map[string]string{"kapro.io/environment": "env-conv"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-conv",
		},
		Status: kaprov1alpha1.ClusterRegistrationStatus{
			LastHeartbeat:   time.Now().UTC().Format(time.RFC3339),
			Phase:           kaprov1alpha1.ClusterPhaseConverged,
			CurrentVersions: map[string]string{"default": "v2.0.0"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env-conv"}}
	converged, err := a.IsConverged(context.Background(), env, "v2.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !converged {
		t.Error("expected converged=true")
	}
}

func TestFluxActuator_IsConverged_FreshHeartbeat_WrongVersion(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-wrong",
			Labels: map[string]string{"kapro.io/environment": "env-wrong"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-wrong",
		},
		Status: kaprov1alpha1.ClusterRegistrationStatus{
			LastHeartbeat:   time.Now().UTC().Format(time.RFC3339),
			Phase:           kaprov1alpha1.ClusterPhaseConverged,
			CurrentVersions: map[string]string{"default": "v1.0.0"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env-wrong"}}
	converged, err := a.IsConverged(context.Background(), env, "v2.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if converged {
		t.Error("expected converged=false when current version doesn't match desired")
	}
}

func TestFluxActuator_Rollback_SetsDesiredVersionToPrevious(t *testing.T) {
	reg := &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "reg-rbk",
			Labels: map[string]string{"kapro.io/environment": "env-rbk"},
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{
			EnvironmentRef: "env-rbk",
			DesiredVersion: "v2.0.0",
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(reg).Build()
	a := &flux.FluxActuator{Client: fakeClient}

	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-rbk"},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{OCIRepository: "oci-repo"},
			},
		},
	}
	if err := a.Rollback(context.Background(), env, "v1.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kaprov1alpha1.ClusterRegistration
	_ = fakeClient.Get(context.Background(), client.ObjectKey{Name: "reg-rbk"}, &updated)
	if updated.Spec.DesiredVersion != "v1.0.0" {
		t.Errorf("expected rollback to set DesiredVersion=v1.0.0, got %s", updated.Spec.DesiredVersion)
	}
}

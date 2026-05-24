package main

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestMigrateBackendObjectUsesOpenSubstrateShape(t *testing.T) {
	in := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "argo-prod",
			Labels:      map[string]string{"team": "platform"},
			Annotations: map[string]string{"note": "keep"},
		},
		Spec: kaprov1alpha2.BackendSpec{
			Driver:  kaprov1alpha2.BackendDriverArgo,
			Adapter: "argo-cd",
			Runtime: kaprov1alpha2.BackendRuntimeHub,
		},
	}
	out := migrateBackendObject(in)
	if out.Spec.Driver != "" || out.Spec.Adapter != "" || out.Spec.Runtime != "" {
		t.Fatalf("deprecated fields were not cleared: %#v", out.Spec)
	}
	if out.Spec.Substrate == nil || out.Spec.Substrate.Kind != "argo" || out.Spec.Substrate.Actuator != "argo-cd" {
		t.Fatalf("substrate = %#v", out.Spec.Substrate)
	}
	if out.Spec.Execution == nil || out.Spec.Execution.Mode != kaprov1alpha2.ExecutionModeHubPush {
		t.Fatalf("execution = %#v", out.Spec.Execution)
	}
	if out.Labels["team"] != "platform" || out.Annotations["note"] != "keep" {
		t.Fatalf("metadata was not preserved: labels=%v annotations=%v", out.Labels, out.Annotations)
	}
}

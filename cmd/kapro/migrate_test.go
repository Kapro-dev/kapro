package main

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestMigrateSubstrateObjectUsesOpenSubstrateShape(t *testing.T) {
	in := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "argo-prod",
			Labels:      map[string]string{"team": "platform"},
			Annotations: map[string]string{"note": "keep"},
		},
		Spec: kaprov1alpha1.SubstrateSpec{
			Substrate: &kaprov1alpha1.SubstrateImplementationSpec{Kind: "argo", Actuator: "argo"},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
		},
	}
	out := migrateSubstrateObject(in)
	if out.Spec.Substrate == nil || out.Spec.Substrate.Kind != "argo" || out.Spec.Substrate.Actuator != "argo" {
		t.Fatalf("substrate = %#v", out.Spec.Substrate)
	}
	if out.Spec.Execution == nil || out.Spec.Execution.Mode != kaprov1alpha1.ExecutionModeHubPush {
		t.Fatalf("execution = %#v", out.Spec.Execution)
	}
	if out.Labels["team"] != "platform" || out.Annotations["note"] != "keep" {
		t.Fatalf("metadata was not preserved: labels=%v annotations=%v", out.Labels, out.Annotations)
	}
}

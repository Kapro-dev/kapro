package main

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestMigrateSubstrateObjectUsesClassRefShape(t *testing.T) {
	in := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "argo-prod",
			Labels:      map[string]string{"team": "platform"},
			Annotations: map[string]string{"note": "keep"},
		},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: "argo"},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
		},
	}
	out := migrateSubstrateObject(in)
	if out.Spec.ClassRef == nil || out.Spec.ClassRef.Name != "argo" {
		t.Fatalf("classRef = %#v", out.Spec.ClassRef)
	}
	if out.Spec.Execution == nil || out.Spec.Execution.Mode != kaprov1alpha1.ExecutionModeHubPush {
		t.Fatalf("execution = %#v", out.Spec.Execution)
	}
	if out.Labels["team"] != "platform" || out.Annotations["note"] != "keep" {
		t.Fatalf("metadata was not preserved: labels=%v annotations=%v", out.Labels, out.Annotations)
	}
}

func TestMigrateV06ToV062ManifestRenamesPublicFields(t *testing.T) {
	input := []byte(`apiVersion: kapro.io/v1alpha1
kind: Fleet
metadata:
  name: checkout
spec:
  substrate:
    mode: pull
    substrateRef: flux
  suspend: true
  clusters:
  - name: prod
    labels:
      stage: prod
---
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: checkout-v1
spec:
  deliveryUnitRef: checkout
  fleetRef: checkout
  planRef: progressive
  version: v1.2.3
  suspend: true
`)
	out, changed, err := migrateV06ToV062Manifest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	text := string(out)
	for _, want := range []string{
		"delivery:",
		"ref: flux",
		"suspended: true",
		"unit: checkout",
		"fleet: checkout",
		"plan: progressive",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("migrated manifest missing %q:\n%s", want, text)
		}
	}
	for _, stale := range []string{"substrateRef:", "deliveryUnitRef:", "fleetRef:", "planRef:", "suspend:"} {
		if strings.Contains(text, stale) {
			t.Fatalf("migrated manifest still contains %q:\n%s", stale, text)
		}
	}
}

func TestMigrateV06ToV062SubstrateInvertsDiscoveryEnabled(t *testing.T) {
	input := []byte(`apiVersion: kapro.io/v1alpha1
kind: Substrate
metadata:
  name: argo
spec:
  substrate:
    kind: argo
    actuator: argo
  discovery:
    enabled: false
`)
	out, changed, err := migrateV06ToV062Manifest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	text := string(out)
	for _, want := range []string{"classRef:", "name: argo", "discovery:", "suspended: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("migrated Substrate missing %q:\n%s", want, text)
		}
	}
	for _, stale := range []string{"enabled:", "actuator:"} {
		if strings.Contains(text, stale) {
			t.Fatalf("migrated Substrate still contains %q:\n%s", stale, text)
		}
	}
}

func TestMigrateV06ToV062RejectsMultiplePreviewPaths(t *testing.T) {
	err := runMigrateV06ToV062(migrateV06ToV062Options{}, []string{"one.yaml", "two.yaml"})
	if err == nil || !strings.Contains(err.Error(), "multiple paths require --write") {
		t.Fatalf("error = %v, want --write guidance", err)
	}
}

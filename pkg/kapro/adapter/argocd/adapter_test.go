package argocd_test

import (
	"context"
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/argocd"
)

type objectShape struct {
	apiVersion  string
	kind        string
	pattern     string
	versionPath string
}

func TestModelDescribesArgoDiscoveryTopology(t *testing.T) {
	model := argocd.Model()

	if model.SubstrateKind != kaprov1alpha1.SubstrateKindArgo {
		t.Fatalf("driver = %q, want argo", model.SubstrateKind)
	}
	if model.ExecutionScope != kaprov1alpha1.ExecutionScopeHub {
		t.Fatalf("runtime = %q, want Hub", model.ExecutionScope)
	}
	if model.DefaultNamespace != "argocd" {
		t.Fatalf("default namespace = %q, want argocd", model.DefaultNamespace)
	}
	if !model.Supported {
		t.Fatal("model should support discovery")
	}
	assertObjectShapes(t, "selected", model.SelectedObjects, []objectShape{
		{apiVersion: "v1", kind: "Secret", pattern: "argocd-cluster-secret"},
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "application", versionPath: "spec.source.targetRevision"},
	})
	assertObjectShapes(t, "skipped", model.SkippedObjects, []objectShape{
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "applicationset-child", versionPath: "spec.source.targetRevision"},
		{apiVersion: "argoproj.io/v1alpha1", kind: "ApplicationSet", pattern: "applicationset", versionPath: "spec.template.spec.source.targetRevision"},
	})
	assertObjectShapes(t, "unsupported", model.UnsupportedObjects, []objectShape{
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "app-of-apps-root", versionPath: "spec.source.targetRevision"},
	})
}

func TestDiscoverReturnsArgoModeledResult(t *testing.T) {
	result, err := argocd.New().Discover(context.Background(), kaproadapter.DiscoveryRequest{Namespace: "team-argocd"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if !result.Ready || result.Reason != "DiscoveryModeled" {
		t.Fatalf("discovery readiness = %t/%q, want ready DiscoveryModeled", result.Ready, result.Reason)
	}
	if result.SubstrateKind != kaprov1alpha1.SubstrateKindArgo || result.ExecutionScope != kaprov1alpha1.ExecutionScopeHub {
		t.Fatalf("driver/runtime = %q/%q, want argo/Hub", result.SubstrateKind, result.ExecutionScope)
	}
	if result.DiscoveredApplications != 5 {
		t.Fatalf("discovered applications = %d, want 5", result.DiscoveredApplications)
	}
	assertObjectShapes(t, "selected", result.SelectedObjects, []objectShape{
		{apiVersion: "v1", kind: "Secret", pattern: "argocd-cluster-secret"},
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "application", versionPath: "spec.source.targetRevision"},
	})
	assertObjectShapes(t, "skipped", result.SkippedObjects, []objectShape{
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "applicationset-child", versionPath: "spec.source.targetRevision"},
		{apiVersion: "argoproj.io/v1alpha1", kind: "ApplicationSet", pattern: "applicationset", versionPath: "spec.template.spec.source.targetRevision"},
	})
	assertObjectShapes(t, "unsupported", result.UnsupportedPatterns, []objectShape{
		{apiVersion: "argoproj.io/v1alpha1", kind: "Application", pattern: "app-of-apps-root", versionPath: "spec.source.targetRevision"},
	})
	if len(result.SubstrateObjectStatusExamples) != len(result.SelectedObjects) {
		t.Fatalf("substrate examples = %d, want %d", len(result.SubstrateObjectStatusExamples), len(result.SelectedObjects))
	}
	for i, example := range result.SubstrateObjectStatusExamples {
		selected := result.SelectedObjects[i]
		if example.APIVersion != selected.APIVersion || example.Kind != selected.Kind {
			t.Fatalf("example[%d] = %#v, selected = %#v", i, example, selected)
		}
		if example.Phase != string(kaprov1alpha1.DeliveryPhasePending) || example.Message != selected.Reason {
			t.Fatalf("example[%d] phase/message = %q/%q", i, example.Phase, example.Message)
		}
	}
}

func assertObjectShapes(t *testing.T, name string, got []kaprov1alpha1.DiscoveredSubstrateObject, want []objectShape) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s objects = %d, want %d: %#v", name, len(got), len(want), got)
	}
	for i := range want {
		if got[i].APIVersion != want[i].apiVersion ||
			got[i].Kind != want[i].kind ||
			got[i].Pattern != want[i].pattern ||
			got[i].VersionField != want[i].versionPath {
			t.Fatalf("%s[%d] = %#v, want %#v", name, i, got[i], want[i])
		}
		if got[i].Reason == "" {
			t.Fatalf("%s[%d] reason is empty", name, i)
		}
	}
}

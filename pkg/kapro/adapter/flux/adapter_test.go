package flux_test

import (
	"context"
	"strings"
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/flux"
)

type objectShape struct {
	apiVersion  string
	kind        string
	pattern     string
	versionPath string
}

func TestModelDescribesFluxDiscoveryTopology(t *testing.T) {
	model := flux.Model()

	if model.Driver != kaprov1alpha2.BackendDriverFlux {
		t.Fatalf("driver = %q, want flux", model.Driver)
	}
	if model.Runtime != kaprov1alpha2.BackendRuntimeBoth {
		t.Fatalf("runtime = %q, want Both", model.Runtime)
	}
	if model.DefaultNamespace != "flux-system" {
		t.Fatalf("default namespace = %q, want flux-system", model.DefaultNamespace)
	}
	if !model.Supported {
		t.Fatal("model should support discovery")
	}
	assertObjectShapes(t, "selected", model.SelectedObjects, []objectShape{
		{apiVersion: "source.toolkit.fluxcd.io/v1", kind: "GitRepository", pattern: "gitrepository", versionPath: "spec.ref.branch"},
		{apiVersion: "source.toolkit.fluxcd.io/v1", kind: "OCIRepository", pattern: "ocirepository", versionPath: "spec.ref.tag"},
		{apiVersion: "source.toolkit.fluxcd.io/v1", kind: "Bucket", pattern: "bucket", versionPath: "spec.ref.branch"},
		{apiVersion: "helm.toolkit.fluxcd.io/v2", kind: "HelmRelease", pattern: "helmrelease", versionPath: "spec.chart.spec.version"},
		{apiVersion: "kustomize.toolkit.fluxcd.io/v1", kind: "Kustomization", pattern: "kustomization", versionPath: "spec.sourceRef.name + spec.path + source revision"},
	})
	if len(model.SkippedObjects) != 0 || len(model.UnsupportedObjects) != 0 {
		t.Fatalf("skipped/unsupported = %d/%d, want 0/0", len(model.SkippedObjects), len(model.UnsupportedObjects))
	}
}

func TestDiscoverReturnsFluxModeledResult(t *testing.T) {
	result, err := flux.New().Discover(context.Background(), kaproadapter.DiscoveryRequest{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if !result.Ready || result.Reason != "DiscoveryModeled" {
		t.Fatalf("discovery readiness = %t/%q, want ready DiscoveryModeled", result.Ready, result.Reason)
	}
	if result.Driver != kaprov1alpha2.BackendDriverFlux || result.Runtime != kaprov1alpha2.BackendRuntimeBoth {
		t.Fatalf("driver/runtime = %q/%q, want flux/Both", result.Driver, result.Runtime)
	}
	if !strings.Contains(result.Message, "flux-system") {
		t.Fatalf("message %q does not mention default namespace", result.Message)
	}
	if result.DiscoveredApplications != 5 {
		t.Fatalf("discovered applications = %d, want 5", result.DiscoveredApplications)
	}
	if len(result.BackendObjectStatusExamples) != 5 {
		t.Fatalf("backend examples = %d, want 5", len(result.BackendObjectStatusExamples))
	}
	for i, example := range result.BackendObjectStatusExamples {
		selected := result.SelectedObjects[i]
		if example.APIVersion != selected.APIVersion ||
			example.Kind != selected.Kind ||
			example.Phase != string(kaprov1alpha2.DeliveryPhasePending) ||
			example.Message != selected.Reason {
			t.Fatalf("example[%d] = %#v, selected = %#v", i, example, selected)
		}
	}
}

func assertObjectShapes(t *testing.T, name string, got []kaprov1alpha2.DiscoveredBackendObject, want []objectShape) {
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

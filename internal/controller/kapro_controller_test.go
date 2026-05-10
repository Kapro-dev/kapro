package controller

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestBuildResourceSet_Components(t *testing.T) {
	kapro := &kaprov1alpha1.Kapro{
		Spec: kaprov1alpha1.KaproSpec{
			Registry: kaprov1alpha1.KaproRegistry{
				URL:      "oci://europe-west1-docker.pkg.dev/myproject/charts",
				Provider: "gcp",
			},
			Clusters: []kaprov1alpha1.KaproCluster{
				{Name: "canary-eu", Labels: map[string]string{"tier": "canary"}},
				{Name: "prod-eu", Labels: map[string]string{"tier": "prod"}},
			},
		},
	}
	kapro.Name = "demo"

	app := &kaprov1alpha1.KaproApp{
		Spec: kaprov1alpha1.KaproAppSpec{
			Components: []kaprov1alpha1.AppComponent{
				{Name: "pos-server", Version: "5.28.0"},
				{Name: "sdc", Version: "5.28.0"},
				{Name: "keycloak", Version: "6.5.0"},
			},
		},
	}

	r := &KaproReconciler{}
	rs := r.buildResourceSet(kapro, app)

	// Verify ResourceSet metadata.
	if rs.GetName() != "demo-workloads" {
		t.Errorf("name = %q, want demo-workloads", rs.GetName())
	}
	if rs.GetNamespace() != "flux-system" {
		t.Errorf("namespace = %q, want flux-system", rs.GetNamespace())
	}

	spec, ok := rs.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("spec is not a map")
	}

	// Verify inputs: one per cluster.
	inputs, ok := spec["inputs"].([]interface{})
	if !ok {
		t.Fatal("inputs is not a slice")
	}
	if len(inputs) != 2 {
		t.Fatalf("len(inputs) = %d, want 2", len(inputs))
	}

	// Verify first input has per-component version fields.
	input0, _ := inputs[0].(map[string]interface{})
	if input0["tenant"] != "canary-eu" {
		t.Errorf("input[0].tenant = %v, want canary-eu", input0["tenant"])
	}
	// Primary version comes from first component.
	if input0["version"] != "5.28.0" {
		t.Errorf("input[0].version = %v, want 5.28.0", input0["version"])
	}

	// Verify resources: one HelmRelease per component + one HelmRepository.
	resources, ok := spec["resources"].([]interface{})
	if !ok {
		t.Fatal("resources is not a slice")
	}
	// 3 HelmReleases + 1 HelmRepository = 4.
	if len(resources) != 4 {
		t.Fatalf("len(resources) = %d, want 4", len(resources))
	}

	// Verify first HelmRelease template uses << inputs.X >> substitution.
	hr0, _ := resources[0].(map[string]interface{})
	if hr0["kind"] != "HelmRelease" {
		t.Errorf("resources[0].kind = %v, want HelmRelease", hr0["kind"])
	}
	meta0, _ := hr0["metadata"].(map[string]interface{})
	if meta0["name"] != "pos-server-<< inputs.tenant >>" {
		t.Errorf("HelmRelease name = %v, want pos-server-<< inputs.tenant >>", meta0["name"])
	}
	hrSpec, _ := hr0["spec"].(map[string]interface{})
	chart, _ := hrSpec["chart"].(map[string]interface{})
	chartSpec, _ := chart["spec"].(map[string]interface{})
	if chartSpec["version"] != "<< inputs.version >>" {
		t.Errorf("chart version = %v, want << inputs.version >>", chartSpec["version"])
	}

	// Verify HelmRepository is the last resource.
	lastRes, _ := resources[3].(map[string]interface{})
	if lastRes["kind"] != "HelmRepository" {
		t.Errorf("resources[3].kind = %v, want HelmRepository", lastRes["kind"])
	}
	repoSpec, _ := lastRes["spec"].(map[string]interface{})
	if repoSpec["url"] != "oci://europe-west1-docker.pkg.dev/myproject/charts" {
		t.Errorf("HelmRepository url = %v", repoSpec["url"])
	}
}

func TestBuildResourceSet_OverrideMerging(t *testing.T) {
	kapro := &kaprov1alpha1.Kapro{
		Spec: kaprov1alpha1.KaproSpec{
			Registry: kaprov1alpha1.KaproRegistry{URL: "oci://registry.example.com/charts"},
			Clusters: []kaprov1alpha1.KaproCluster{
				{Name: "canary", Labels: map[string]string{"tier": "canary"}},
				{Name: "prod", Labels: map[string]string{"tier": "prod"}},
			},
		},
	}
	kapro.Name = "test"

	app := &kaprov1alpha1.KaproApp{
		Spec: kaprov1alpha1.KaproAppSpec{
			Components: []kaprov1alpha1.AppComponent{
				{Name: "app", Version: "1.0"},
			},
			Defaults: &apiextensionsv1.JSON{
				Raw: []byte(`{"replicaCount":3,"logging":{"level":"info","format":"json"}}`),
			},
			Overrides: []kaprov1alpha1.AppOverride{
				{
					Selector: map[string]string{"tier": "canary"},
					Values: &apiextensionsv1.JSON{
						Raw: []byte(`{"replicaCount":1,"logging":{"level":"debug"}}`),
					},
				},
			},
		},
	}

	r := &KaproReconciler{}
	rs := r.buildResourceSet(kapro, app)

	spec, _ := rs.Object["spec"].(map[string]interface{})
	inputs, _ := spec["inputs"].([]interface{})

	// Canary should have merged values: replicaCount=1, logging.level=debug, logging.format=json (deep merge).
	canaryInput, _ := inputs[0].(map[string]interface{})
	canaryValues := canaryInput["values_override"].(string)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(canaryValues), &parsed); err != nil {
		t.Fatalf("parse canary values: %v", err)
	}

	if parsed["replicaCount"] != float64(1) {
		t.Errorf("canary replicaCount = %v, want 1", parsed["replicaCount"])
	}
	logging, _ := parsed["logging"].(map[string]interface{})
	if logging["level"] != "debug" {
		t.Errorf("canary logging.level = %v, want debug", logging["level"])
	}
	if logging["format"] != "json" {
		t.Errorf("canary logging.format = %v, want json (deep merge should preserve)", logging["format"])
	}

	// Prod should have defaults only: replicaCount=3, logging.level=info.
	prodInput, _ := inputs[1].(map[string]interface{})
	prodValues := prodInput["values_override"].(string)

	var prodParsed map[string]interface{}
	if err := json.Unmarshal([]byte(prodValues), &prodParsed); err != nil {
		t.Fatalf("parse prod values: %v", err)
	}
	if prodParsed["replicaCount"] != float64(3) {
		t.Errorf("prod replicaCount = %v, want 3", prodParsed["replicaCount"])
	}
	prodLogging, _ := prodParsed["logging"].(map[string]interface{})
	if prodLogging["level"] != "info" {
		t.Errorf("prod logging.level = %v, want info", prodLogging["level"])
	}
}

func TestBuildPipeline(t *testing.T) {
	kapro := &kaprov1alpha1.Kapro{
		Spec: kaprov1alpha1.KaproSpec{
			Pipeline: kaprov1alpha1.KaproPipeline{
				Stages: []kaprov1alpha1.KaproStage{
					{Name: "canary", Selector: map[string]string{"tier": "canary"}},
					{Name: "prod", Selector: map[string]string{"tier": "prod"},
						DependsOn: []kaprov1alpha1.StageDependency{{Stage: "canary"}}},
				},
			},
		},
	}
	kapro.Name = "demo"

	r := &KaproReconciler{}
	pipeline := r.buildPipeline(kapro)

	if pipeline.Name != "demo-pipeline" {
		t.Errorf("pipeline name = %q, want demo-pipeline", pipeline.Name)
	}
	if len(pipeline.Spec.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(pipeline.Spec.Stages))
	}
	if pipeline.Spec.Stages[1].DependsOn[0].Stage != "canary" {
		t.Errorf("prod dependsOn = %v, want canary", pipeline.Spec.Stages[1].DependsOn)
	}
}

func TestDeepMerge(t *testing.T) {
	dst := map[string]interface{}{
		"a": "1",
		"nested": map[string]interface{}{
			"x": "keep",
			"y": "original",
		},
	}
	src := map[string]interface{}{
		"b": "2",
		"nested": map[string]interface{}{
			"y": "override",
			"z": "new",
		},
	}

	deepMerge(dst, src)

	if dst["a"] != "1" {
		t.Errorf("a = %v, want 1", dst["a"])
	}
	if dst["b"] != "2" {
		t.Errorf("b = %v, want 2", dst["b"])
	}
	nested, _ := dst["nested"].(map[string]interface{})
	if nested["x"] != "keep" {
		t.Errorf("nested.x = %v, want keep", nested["x"])
	}
	if nested["y"] != "override" {
		t.Errorf("nested.y = %v, want override", nested["y"])
	}
	if nested["z"] != "new" {
		t.Errorf("nested.z = %v, want new", nested["z"])
	}
}

func TestResolveVersionField(t *testing.T) {
	// Import actuator helpers indirectly — test the naming convention.
	tests := []struct {
		appKey string
		want   string
	}{
		{"pos-server", "pos-server_version"},
		{"sdc", "sdc_version"},
		{"", "tag"},
		{"default", "tag"},
	}

	for _, tt := range tests {
		got := resolveTestVersionField(tt.appKey)
		if got != tt.want {
			t.Errorf("resolveVersionField(%q) = %q, want %q", tt.appKey, got, tt.want)
		}
	}
}

// resolveTestVersionField mirrors the actuator's resolveVersionField logic.
func resolveTestVersionField(appKey string) string {
	if appKey != "" && appKey != "default" {
		return appKey + "_version"
	}
	return "tag"
}

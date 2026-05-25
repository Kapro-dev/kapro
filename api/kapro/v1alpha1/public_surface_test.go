package v1alpha1_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type crdDocument struct {
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind       string   `json:"kind"`
			Singular   string   `json:"singular"`
			Plural     string   `json:"plural"`
			ShortNames []string `json:"shortNames"`
			Categories []string `json:"categories"`
		} `json:"names"`
		Versions []struct {
			Name    string `json:"name"`
			Served  bool   `json:"served"`
			Storage bool   `json:"storage"`
		} `json:"versions"`
	} `json:"spec"`
}

var publicCRDs = map[string]string{
	"kapro.io_substratediscoverypolicies.yaml": "SubstrateDiscoveryPolicy",
	"kapro.io_approvals.yaml":                  "Approval",
	"kapro.io_clusters.yaml":                   "Cluster",
	"kapro.io_clustertemplates.yaml":           "ClusterTemplate",
	"kapro.io_deliveryunits.yaml":              "DeliveryUnit",
	"kapro.io_fleets.yaml":                     "Fleet",
	"kapro.io_plans.yaml":                      "Plan",
	"kapro.io_plugins.yaml":                    "Plugin",
	"kapro.io_policies.yaml":                   "Policy",
	"kapro.io_promotions.yaml":                 "Promotion",
	"kapro.io_sources.yaml":                    "Source",
	"kapro.io_substrateclasses.yaml":           "SubstrateClass",
	"kapro.io_substrates.yaml":                 "Substrate",
	"kapro.io_triggers.yaml":                   "Trigger",
}

var runtimeCRDs = map[string]string{
	"runtime.kapro.io_decisiontraces.yaml": "DecisionTrace",
	"runtime.kapro.io_promotionruns.yaml":  "PromotionRun",
	"runtime.kapro.io_targets.yaml":        "Target",
}

func TestCRDSurfaceMatchesAuthorshipBoundary(t *testing.T) {
	root := repoRoot(t)
	got := crdFileSet(t, filepath.Join(root, "config", "crd", "bases"))
	want := make([]string, 0, len(publicCRDs)+len(runtimeCRDs)+4)
	for name := range publicCRDs {
		want = append(want, name)
	}
	for name := range runtimeCRDs {
		want = append(want, name)
	}
	want = append(want, []string{
		"argocd.substrate.kapro.io_argocdsubstrateconfigs.yaml",
		"flux.substrate.kapro.io_fluxsubstrateconfigs.yaml",
		"kubernetes.substrate.kapro.io_kubernetesapplyconfigs.yaml",
		"oci.substrate.kapro.io_ocibundleapplyconfigs.yaml",
	}...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CRD file set drifted\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPublicAndRuntimeCRDsUseV1Alpha1Storage(t *testing.T) {
	root := repoRoot(t)
	for file, kind := range publicCRDs {
		crd := readCRD(t, filepath.Join(root, "config", "crd", "bases", file))
		if crd.Spec.Group != "kapro.io" || crd.Spec.Names.Kind != kind {
			t.Fatalf("%s = %s/%s, want kapro.io/%s", file, crd.Spec.Group, crd.Spec.Names.Kind, kind)
		}
		assertOnlyStorageVersion(t, file, crd, "v1alpha1")
	}
	for file, kind := range runtimeCRDs {
		crd := readCRD(t, filepath.Join(root, "config", "crd", "bases", file))
		if crd.Spec.Group != "runtime.kapro.io" || crd.Spec.Names.Kind != kind {
			t.Fatalf("%s = %s/%s, want runtime.kapro.io/%s", file, crd.Spec.Group, crd.Spec.Names.Kind, kind)
		}
		assertOnlyStorageVersion(t, file, crd, "v1alpha1")
	}
}

func TestRemovedPreviewCRDsDoNotShip(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"config/crd/bases/kapro.io_backends.yaml",
		"config/crd/bases/kapro.io_gateexpressions.yaml",
		"config/crd/bases/kapro.io_promotionunits.yaml",
		"config/crd/bases/kapro.io_fleetdriftreports.yaml",
		"config/crd/bases/kapro.io_promotionruns.yaml",
		"config/crd/bases/kapro.io_targets.yaml",
		"config/crd/bases/kapro.io_decisiontraces.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist after the API split", rel)
		}
	}
}

func TestSubstrateCRDUsesPublicNaming(t *testing.T) {
	root := repoRoot(t)
	data := readFile(t, filepath.Join(root, "config", "crd", "bases", "kapro.io_substrates.yaml"))
	text := string(data)
	for _, want := range []string{"name: substrates.kapro.io", "kind: Substrate", "singular: substrate", "substrateRef"} {
		if !strings.Contains(text, want) {
			t.Fatalf("kapro.io_substrates.yaml missing %q", want)
		}
	}
	for _, stale := range []string{"name: backends.kapro.io", "kind: Backend", "backendRef"} {
		if strings.Contains(text, stale) {
			t.Fatalf("kapro.io_substrates.yaml still contains stale %q", stale)
		}
	}
}

func TestGeneratedCRDsSyncedToChartAndBootstrap(t *testing.T) {
	root := repoRoot(t)
	for _, file := range crdFileSet(t, filepath.Join(root, "config", "crd", "bases")) {
		source := readFile(t, filepath.Join(root, "config", "crd", "bases", file))
		for _, dir := range []string{
			filepath.Join(root, "charts", "kapro-operator", "crds"),
			filepath.Join(root, "internal", "bootstrap", "kaprocrds"),
		} {
			if got := readFile(t, filepath.Join(dir, file)); string(got) != string(source) {
				t.Fatalf("%s is not synced to %s", file, dir)
			}
		}
	}
}

func TestHelmChartRBACCoversRuntimeAndSubstrateSurface(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"config/rbac/role.yaml",
		"charts/kapro-operator/templates/rbac.yaml",
	} {
		text := string(readFile(t, filepath.Join(root, rel)))
		for _, want := range []string{
			"runtime.kapro.io",
			"decisiontraces",
			"promotionruns",
			"targets",
			"substrateclasses",
			"substrates",
			"argocd.substrate.kapro.io",
			"flux.substrate.kapro.io",
			"kubernetes.substrate.kapro.io",
			"oci.substrate.kapro.io",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing RBAC surface %q", rel, want)
			}
		}
		if strings.Contains(text, "webhook.substrate.kapro.io") {
			t.Fatalf("%s still grants removed webhook substrate RBAC", rel)
		}
	}

	values := string(readFile(t, filepath.Join(root, "charts", "kapro-operator", "values.yaml")))
	for _, want := range []string{"promotionrun", "substrateclass", "substrate"} {
		if !strings.Contains(values, "- "+want) {
			t.Fatalf("values.yaml missing default controller %q", want)
		}
	}
	readme := string(readFile(t, filepath.Join(root, "charts", "kapro-operator", "README.md")))
	for _, want := range []string{"runtime.kapro.io/v1alpha1", "substrateclass", "substrate"} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README.md missing %q", want)
		}
	}
}

func assertOnlyStorageVersion(t *testing.T, file string, crd crdDocument, version string) {
	t.Helper()
	if len(crd.Spec.Versions) != 1 {
		t.Fatalf("%s has %d versions, want 1", file, len(crd.Spec.Versions))
	}
	got := crd.Spec.Versions[0]
	if got.Name != version || !got.Served || !got.Storage {
		t.Fatalf("%s version = %#v, want served+storage %s", file, got, version)
	}
}

func readCRD(t *testing.T, path string) crdDocument {
	t.Helper()
	var crd crdDocument
	if err := yaml.Unmarshal(readFile(t, path), &crd); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return crd
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func crdFileSet(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("go.mod not found")
		}
		dir = next
	}
}

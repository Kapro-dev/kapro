package v1alpha2_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPublicSurfaceUsesV1Alpha2Names(t *testing.T) {
	root := repoRoot(t)
	bad := []*regexp.Regexp{
		regexp.MustCompile(`apiVersion:\s*kapro\.io/v1alpha1`),
		regexp.MustCompile(`kind:\s*(FleetCluster|PromotionPlan|PromotionSource|PromotionTarget|BackendProfile|PluginRegistration|AgentPolicy|FleetClusterTemplate|PromotionTrigger)\b`),
		regexp.MustCompile(`\bkaproRef\b`),
		regexp.MustCompile(`\bpromotionPlans\b`),
		regexp.MustCompile(`\bpromotionPlan\b`),
		regexp.MustCompile(`\b(fleetclusters|promotionplans|promotionsources|promotiontargets|promotiontriggers|pluginregistrations|backendprofiles)\b`),
		regexp.MustCompile(`\b(PromotionTarget|PromotionTrigger|PluginRegistration|BackendProfile|FleetCluster|FleetClusterTemplate|PromotionPlan|PromotionSource)\b`),
		regexp.MustCompile(`kubectl[^\n]*(backendprofile|fleetcluster|promotiontarget|promotiontrigger|pluginregistration|promotionplan|promotionsource)s?\b`),
	}

	for _, rel := range []string{"README.md", "docs", "examples", "scripts"} {
		scanPublicSurface(t, filepath.Join(root, rel), bad)
	}
}

func TestHelmWebhookRulesUseServedVersion(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "charts", "kapro-operator", "templates", "validating-webhook.yaml")
	data := readText(t, path)
	if strings.Contains(data, `apiVersions: ["v1alpha1"]`) {
		t.Fatalf("%s still contains v1alpha1 webhook rules", path)
	}
	if !strings.Contains(data, `apiVersions: ["v1alpha2"]`) {
		t.Fatalf("%s does not contain v1alpha2 webhook rules", path)
	}
}

func TestTargetCRDPrintColumnsUseCurrentFields(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "config", "crd", "bases", "kapro.io_targets.yaml")
	data := readText(t, path)
	for _, stale := range []string{".spec.promotionRunRef", ".spec.promotionPlanRef"} {
		if strings.Contains(data, stale) {
			t.Fatalf("%s still contains stale Target printcolumn %s", path, stale)
		}
	}
	for _, want := range []string{".spec.runRef", ".spec.planRef"} {
		if !strings.Contains(data, want) {
			t.Fatalf("%s missing Target printcolumn %s", path, want)
		}
	}
}

func scanPublicSurface(t *testing.T, root string, bad []*regexp.Regexp) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		checkFile(t, root, bad)
		return
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(path)
		if d.IsDir() {
			if strings.Contains(rel, "/docs/adr") {
				return filepath.SkipDir
			}
			return nil
		}
		if isPublicTextFile(path) {
			checkFile(t, path, bad)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkFile(t *testing.T, path string, bad []*regexp.Regexp) {
	t.Helper()
	data := readText(t, path)
	for _, re := range bad {
		if match := re.FindString(data); match != "" {
			t.Fatalf("%s contains stale v1alpha1 public API spelling %q", path, match)
		}
	}
}

func isPublicTextFile(path string) bool {
	switch filepath.Ext(path) {
	case ".md", ".yaml", ".yml", ".json", ".sh":
		return true
	default:
		return filepath.Base(path) == "README"
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

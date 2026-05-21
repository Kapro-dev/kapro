package v1alpha2_test

import (
	"bytes"
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

	for _, rel := range []string{"README.md", "docs", "examples", "scripts", "charts"} {
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
	matches := regexp.MustCompile(`apiVersions:\s*\[([^\]]+)\]`).FindAllStringSubmatch(data, -1)
	if len(matches) == 0 {
		t.Fatalf("%s does not contain any webhook apiVersions rules", path)
	}
	for _, match := range matches {
		if strings.TrimSpace(match[1]) != `"v1alpha2"` {
			t.Fatalf("%s contains non-v1alpha2 webhook apiVersions rule %q", path, match[0])
		}
	}
}

func TestGeneratedCRDsAreSyncedAndServedVersion(t *testing.T) {
	root := repoRoot(t)
	configDir := filepath.Join(root, "config", "crd", "bases")
	chartDir := filepath.Join(root, "charts", "kapro-operator", "crds")
	bootstrapDir := filepath.Join(root, "internal", "bootstrap", "kaprocrds")

	for _, name := range crdBaseNames(t, configDir) {
		configPath := filepath.Join(configDir, name)
		configData := readBytes(t, configPath)
		checkCRDServedVersion(t, configPath, string(configData))

		for _, dir := range []string{chartDir, bootstrapDir} {
			path := filepath.Join(dir, name)
			data := readBytes(t, path)
			checkCRDServedVersion(t, path, string(data))
			if !bytes.Equal(configData, data) {
				t.Fatalf("%s differs from %s; generated CRD copy is stale", path, configPath)
			}
		}
	}
}

func TestTargetCRDPrintColumnsUseCurrentFields(t *testing.T) {
	root := repoRoot(t)
	for _, relPath := range []string{
		filepath.Join("config", "crd", "bases", "kapro.io_targets.yaml"),
		filepath.Join("charts", "kapro-operator", "crds", "kapro.io_targets.yaml"),
		filepath.Join("internal", "bootstrap", "kaprocrds", "kapro.io_targets.yaml"),
	} {
		path := filepath.Join(root, relPath)
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
}

func TestCRDShortNamesUseCurrentAliases(t *testing.T) {
	root := repoRoot(t)
	staleShortNames := map[string][]string{
		"kapro.io_backends.yaml":         {"bp", "backend"},
		"kapro.io_clusters.yaml":         {"mc", "fc", "fleetcluster"},
		"kapro.io_clustertemplates.yaml": {"fct", "fleettemplate"},
		"kapro.io_fleets.yaml":           {"kp"},
		"kapro.io_plugins.yaml":          {"pluginreg"},
		"kapro.io_policies.yaml":         {"agp"},
		"kapro.io_promotionruns.yaml":    {"rel"},
		"kapro.io_sources.yaml":          {"ps", "source", "sources"},
		"kapro.io_targets.yaml":          {"relt"},
		"kapro.io_triggers.yaml":         {"reltrig"},
	}
	for _, relDir := range []string{
		filepath.Join("config", "crd", "bases"),
		filepath.Join("charts", "kapro-operator", "crds"),
		filepath.Join("internal", "bootstrap", "kaprocrds"),
	} {
		for file, names := range staleShortNames {
			path := filepath.Join(root, relDir, file)
			data := readText(t, path)
			for _, name := range names {
				if strings.Contains(data, "\n    - "+name+"\n") {
					t.Fatalf("%s still exposes stale shortName %q", path, name)
				}
			}
		}
	}
}

func TestReleaseVersionMarkersStayInSync(t *testing.T) {
	root := repoRoot(t)
	operatorChart := filepath.Join(root, "charts", "kapro-operator", "Chart.yaml")
	clusterControllerChart := filepath.Join(root, "charts", "kapro-cluster-controller", "Chart.yaml")

	operatorVersion, operatorAppVersion := helmChartVersionPair(t, operatorChart)
	clusterControllerVersion, clusterControllerAppVersion := helmChartVersionPair(t, clusterControllerChart)
	if operatorVersion != clusterControllerVersion {
		t.Fatalf("chart versions drifted: kapro-operator=%s, kapro-cluster-controller=%s", operatorVersion, clusterControllerVersion)
	}

	releaseTag := "v" + operatorVersion
	for chart, appVersion := range map[string]string{
		operatorChart:          operatorAppVersion,
		clusterControllerChart: clusterControllerAppVersion,
	} {
		if appVersion != releaseTag {
			t.Fatalf("%s appVersion=%q, want %q", chart, appVersion, releaseTag)
		}
	}

	for _, relPath := range []string{
		"README.md",
		filepath.Join("docs", "api-stability.md"),
		filepath.Join("docs", "install.md"),
	} {
		path := filepath.Join(root, relPath)
		if !strings.Contains(readText(t, path), releaseTag) {
			t.Fatalf("%s does not mention current release tag %s", path, releaseTag)
		}
	}
}

func TestAPICommentsUseCurrentPublicResourceNames(t *testing.T) {
	root := repoRoot(t)
	stale := []*regexp.Regexp{
		regexp.MustCompile(`kubectl[^\n]*(backendprofile|fleetcluster|promotiontarget|promotiontrigger|pluginregistration|promotionplan|promotionsource)s?\b`),
		regexp.MustCompile(`kapro\.io/managed-by=fleetclustertemplate\b`),
	}
	err := filepath.WalkDir(filepath.Join(root, "api", "v1alpha2"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if filepath.Ext(path) != ".go" || strings.HasSuffix(base, "_test.go") || base == "zz_generated.deepcopy.go" {
			return nil
		}
		for _, line := range strings.Split(readText(t, path), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, re := range stale {
				if match := re.FindString(line); match != "" {
					t.Fatalf("%s contains stale public API comment %q", path, match)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	return string(readBytes(t, path))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func crdBaseNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		t.Fatalf("no CRD YAML files found in %s", dir)
	}
	return names
}

func helmChartVersionPair(t *testing.T, path string) (version, appVersion string) {
	t.Helper()
	data := readText(t, path)
	version = helmChartScalar(t, path, data, "version")
	appVersion = helmChartScalar(t, path, data, "appVersion")
	return version, appVersion
}

func helmChartScalar(t *testing.T, path, data, key string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*"?([^"\n#]+)"?`)
	match := re.FindStringSubmatch(data)
	if match == nil {
		t.Fatalf("%s missing %s", path, key)
	}
	return strings.TrimSpace(match[1])
}

func checkCRDServedVersion(t *testing.T, path, data string) {
	t.Helper()
	if strings.Contains(data, "name: v1alpha1") {
		t.Fatalf("%s still contains a v1alpha1 CRD version", path)
	}
	for _, want := range []string{"name: v1alpha2", "served: true", "storage: true"} {
		if !strings.Contains(data, want) {
			t.Fatalf("%s missing CRD version field %q", path, want)
		}
	}
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

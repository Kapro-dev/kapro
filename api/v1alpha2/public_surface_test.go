package v1alpha2_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type resourceContract struct {
	Kind     string
	Singular string
	Plural   string
}

var kaproResources = []resourceContract{
	{Kind: "Approval", Singular: "approval", Plural: "approvals"},
	{Kind: "Backend", Singular: "backend", Plural: "backends"},
	{Kind: "Cluster", Singular: "cluster", Plural: "clusters"},
	{Kind: "ClusterTemplate", Singular: "clustertemplate", Plural: "clustertemplates"},
	{Kind: "Fleet", Singular: "fleet", Plural: "fleets"},
	{Kind: "Plan", Singular: "plan", Plural: "plans"},
	{Kind: "Plugin", Singular: "plugin", Plural: "plugins"},
	{Kind: "Policy", Singular: "policy", Plural: "policies"},
	{Kind: "Promotion", Singular: "promotion", Plural: "promotions"},
	{Kind: "PromotionRun", Singular: "promotionrun", Plural: "promotionruns"},
	{Kind: "Source", Singular: "source", Plural: "sources"},
	{Kind: "Target", Singular: "target", Plural: "targets"},
	{Kind: "Trigger", Singular: "trigger", Plural: "triggers"},
}

var kaproPluralSet = func() map[string]bool {
	out := map[string]bool{}
	for _, resource := range kaproResources {
		out[resource.Plural] = true
		out[resource.Plural+"/status"] = true
		out[resource.Plural+"/finalizers"] = true
	}
	return out
}()

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

func TestCRDNamesMatchPublicContract(t *testing.T) {
	root := repoRoot(t)
	configDir := filepath.Join(root, "config", "crd", "bases")
	want := map[string]resourceContract{}
	for _, resource := range kaproResources {
		want[resource.Plural] = resource
	}

	got := map[string]resourceContract{}
	for _, name := range crdBaseNames(t, configDir) {
		path := filepath.Join(configDir, name)
		var crd struct {
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Kind     string `json:"kind"`
					Singular string `json:"singular"`
					Plural   string `json:"plural"`
				} `json:"names"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(readBytes(t, path), &crd); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if crd.Spec.Group != "kapro.io" {
			t.Fatalf("%s group=%q, want kapro.io", path, crd.Spec.Group)
		}
		got[crd.Spec.Names.Plural] = resourceContract{
			Kind:     crd.Spec.Names.Kind,
			Singular: crd.Spec.Names.Singular,
			Plural:   crd.Spec.Names.Plural,
		}
	}

	if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
		t.Fatalf("CRD resource set drifted\ngot:  %v\nwant: %v", sortedKeys(got), sortedKeys(want))
	}
	for plural, wantResource := range want {
		if gotResource := got[plural]; gotResource != wantResource {
			t.Fatalf("CRD contract for %s drifted: got %+v, want %+v", plural, gotResource, wantResource)
		}
	}
}

func TestKaproRBACResourcesExist(t *testing.T) {
	root := repoRoot(t)
	for _, relPath := range []string{
		filepath.Join("charts", "kapro-operator", "templates", "rbac.yaml"),
		filepath.Join("examples", "rbac", "recommended-roles.yaml"),
	} {
		for _, resource := range kaproRBACResources(t, filepath.Join(root, relPath)) {
			if !kaproPluralSet[resource] {
				t.Fatalf("%s grants kapro.io resource %q, which is not one of %v", relPath, resource, sortedBoolKeys(kaproPluralSet))
			}
		}
	}
}

func TestWebhookResourcesMatchPublicContract(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "charts", "kapro-operator", "templates", "validating-webhook.yaml")
	data := readText(t, path)
	matches := regexp.MustCompile(`resources:\s*\[([^\]]+)\]`).FindAllStringSubmatch(data, -1)
	if len(matches) == 0 {
		t.Fatalf("%s does not define webhook resources", path)
	}
	for _, match := range matches {
		for _, resource := range commaList(match[1]) {
			if !kaproPluralSet[resource] {
				t.Fatalf("%s references unknown webhook resource %q", path, resource)
			}
		}
	}
}

func TestControllerSelectionUsesRegisteredPublicNames(t *testing.T) {
	root := repoRoot(t)
	known := registeredControllerNames(t, filepath.Join(root, "pkg", "controllermanager", "controllers.go"))
	for _, controller := range helmDefaultControllers(t, filepath.Join(root, "charts", "kapro-operator", "values.yaml")) {
		if controller == "*" {
			t.Fatalf("chart must not default to wildcard controllers; opt-in controllers can require extra configuration")
		}
		if !known[controller] {
			t.Fatalf("chart default controller %q is not registered; known=%v", controller, sortedBoolKeys(known))
		}
	}

	for _, relPath := range []string{
		filepath.Join("docs", "install.md"),
		filepath.Join("examples", "kind-demo", "operator", "manager-env-patch.yaml"),
		filepath.Join("scripts", "argo-e2e.sh"),
	} {
		path := filepath.Join(root, relPath)
		for _, controller := range controllerNameReferences(readText(t, path)) {
			if controller == "*" || strings.HasPrefix(controller, "-") {
				controller = strings.TrimPrefix(controller, "-")
			}
			if controller == "" || controller == "*" {
				continue
			}
			if !known[controller] {
				t.Fatalf("%s references unknown controller %q; known=%v", relPath, controller, sortedBoolKeys(known))
			}
		}
	}
}

func TestUserFacingDiagnosticsUseCurrentFields(t *testing.T) {
	root := repoRoot(t)
	stale := []*regexp.Regexp{
		regexp.MustCompile(`"[^"]*(spec\.promotionPlans|spec\.promotionPlan|spec\.kaproRef|kapro\.io/v1alpha1)[^"]*"`),
	}
	for _, relDir := range []string{
		filepath.Join("internal", "lint"),
		filepath.Join("internal", "webhook", "admission"),
		filepath.Join("cmd", "kapro"),
	} {
		err := filepath.WalkDir(filepath.Join(root, relDir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data := readText(t, path)
			for _, re := range stale {
				if match := re.FindString(data); match != "" {
					t.Fatalf("%s contains stale user-facing diagnostic string %q", path, match)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloudEventsSourceUsesServedAPI(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "pkg", "events", "types.go")
	data := readText(t, path)
	if !strings.Contains(data, `Source:          "/apis/kapro.io/v1alpha2/promotions/" + e.PromotionName`) {
		t.Fatalf("%s must render CloudEvents source with kapro.io/v1alpha2 Promotion paths", path)
	}
	if strings.Contains(data, "subsequent v1alpha1 release") {
		t.Fatalf("%s contains stale CloudEvents version policy text", path)
	}
	if strings.Contains(data, "FleetCluster") {
		t.Fatalf("%s contains stale public events resource name FleetCluster", path)
	}
}

func TestControllersApplyCurrentRuntimeKinds(t *testing.T) {
	root := repoRoot(t)
	for _, relPath := range []string{
		filepath.Join("internal", "controller", "fleet_controller.go"),
		filepath.Join("internal", "controller", "clustertemplate_controller.go"),
	} {
		path := filepath.Join(root, relPath)
		data := readText(t, path)
		for _, stale := range []string{
			`Kind: "FleetCluster"`,
			`Kind: "PromotionPlan"`,
			`Kind: "PromotionSource"`,
			`Kind: "PromotionTarget"`,
			`Kind: "PromotionTrigger"`,
			`"FleetCluster/"`,
			`"PromotionPlan/"`,
			`"PromotionSource/"`,
			`"PromotionTarget/"`,
			`"PromotionTrigger/"`,
		} {
			if strings.Contains(data, stale) {
				t.Fatalf("%s contains stale runtime public kind marker %q", relPath, stale)
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

func kaproRBACResources(t *testing.T, path string) []string {
	t.Helper()
	data := readText(t, path)
	if strings.Contains(path, filepath.Join("templates", "rbac.yaml")) {
		start := strings.Index(data, "rules:")
		if start < 0 {
			t.Fatalf("%s does not contain a ClusterRole rules block", path)
		}
		end := strings.Index(data[start:], "\n---")
		if end < 0 {
			t.Fatalf("%s does not contain an isolated ClusterRole rules block", path)
		}
		data = data[start : start+end]
	}

	var docs []string
	if strings.Contains(data, "\n---") {
		docs = strings.Split(data, "\n---")
	} else {
		docs = []string{data}
	}
	var resources []string
	for _, doc := range docs {
		var role struct {
			Rules []struct {
				APIGroups []string `json:"apiGroups"`
				Resources []string `json:"resources"`
			} `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
			t.Fatalf("parse RBAC %s: %v", path, err)
		}
		for _, rule := range role.Rules {
			if !contains(rule.APIGroups, "kapro.io") {
				continue
			}
			resources = append(resources, rule.Resources...)
		}
	}
	return resources
}

func registeredControllerNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	re := regexp.MustCompile(`Register\("([^"]+)"`)
	for _, match := range re.FindAllStringSubmatch(readText(t, path), -1) {
		out[match[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s did not register any controllers", path)
	}
	return out
}

func helmDefaultControllers(t *testing.T, path string) []string {
	t.Helper()
	var values struct {
		Controllers []string `json:"controllers"`
	}
	if err := yaml.Unmarshal(readBytes(t, path), &values); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(values.Controllers) == 0 {
		t.Fatalf("%s does not define default controllers", path)
	}
	return values.Controllers
}

func controllerNameReferences(data string) []string {
	var refs []string
	var previousLine string
	envRe := regexp.MustCompile(`KAPRO_CONTROLLERS=([A-Za-z0-9*,_-]+(?:,[A-Za-z0-9*_-]+)*)`)
	valueRe := regexp.MustCompile(`value:\s*"?([A-Za-z0-9*,_-]+(?:,[A-Za-z0-9*_-]+)*)"?`)
	for _, line := range strings.Split(data, "\n") {
		if match := envRe.FindStringSubmatch(line); match != nil {
			refs = append(refs, commaList(match[1])...)
		}
		if strings.Contains(previousLine, "KAPRO_CONTROLLERS") {
			if match := valueRe.FindStringSubmatch(line); match != nil {
				refs = append(refs, commaList(match[1])...)
			}
		}
		previousLine = line
	}
	return refs
}

func commaList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(strings.Trim(item, `"'`))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedBoolKeys(values map[string]bool) []string {
	return sortedKeys(values)
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

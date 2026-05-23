package v1alpha2_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
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

type crdDocument struct {
	Spec struct {
		Names struct {
			Kind       string   `json:"kind"`
			Singular   string   `json:"singular"`
			Plural     string   `json:"plural"`
			ShortNames []string `json:"shortNames"`
			Categories []string `json:"categories"`
		} `json:"names"`
		Versions []crdVersion `json:"versions"`
	} `json:"spec"`
}

type crdVersion struct {
	Name                     string `json:"name"`
	AdditionalPrinterColumns []struct {
		Name     string `json:"name"`
		JSONPath string `json:"jsonPath"`
		Priority int32  `json:"priority"`
	} `json:"additionalPrinterColumns"`
	Schema struct {
		OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
	} `json:"schema"`
}

var kaproResources = []resourceContract{
	{Kind: "AdapterPolicy", Singular: "adapterpolicy", Plural: "adapterpolicies"},
	{Kind: "Approval", Singular: "approval", Plural: "approvals"},
	{Kind: "Backend", Singular: "backend", Plural: "backends"},
	{Kind: "Cluster", Singular: "cluster", Plural: "clusters"},
	{Kind: "ClusterTemplate", Singular: "clustertemplate", Plural: "clustertemplates"},
	{Kind: "Fleet", Singular: "fleet", Plural: "fleets"},
	{Kind: "GateExpression", Singular: "gateexpression", Plural: "gateexpressions"},
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

func TestDefaultTrueBoolsCanRepresentExplicitFalse(t *testing.T) {
	root := repoRoot(t)
	re := regexp.MustCompile("(?m)\\+kubebuilder:default=true(?:\\n[ \\t]*//[^\\n]*)*\\n[ \\t]*([A-Za-z0-9_]+)[ \\t]+bool[ \\t]+`json:\"([^\"]*omitempty[^\"]*)\"`")

	err := filepath.WalkDir(filepath.Join(root, "api", "v1alpha2"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(base, "_test.go") || base == "zz_generated.deepcopy.go" {
			return nil
		}
		for _, match := range re.FindAllStringSubmatch(readText(t, path), -1) {
			t.Errorf("%s: default=true bool field %s uses omitempty JSON tag %q; use *bool or a non-omitempty tag so explicit false survives client serialization", path, match[1], match[2])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedCRDDescriptionsDoNotPublishPreviewPromises(t *testing.T) {
	root := repoRoot(t)
	bad := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bnot yet wired\b`),
		regexp.MustCompile(`(?i)\bplanned:\b`),
		regexp.MustCompile(`(?i)\bverified artifact changes\b`),
		regexp.MustCompile(`(?i)\bfuture AI agents\b`),
		regexp.MustCompile(`(?i)\blights up\s+when spec\.rollbackTo is wired\b`),
		regexp.MustCompile(`(?i)\bALL is accepted; other operators are reserved\b`),
	}

	err := filepath.WalkDir(filepath.Join(root, "config", "crd", "bases"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		data := readText(t, path)
		for _, re := range bad {
			if match := re.FindString(data); match != "" {
				t.Errorf("%s contains public CRD description wording %q that over-promises preview behavior", path, match)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTriggerCRDDefaultTrueSuspensionFields(t *testing.T) {
	root := repoRoot(t)
	for _, relDir := range []string{
		filepath.Join("config", "crd", "bases"),
		filepath.Join("charts", "kapro-operator", "crds"),
		filepath.Join("internal", "bootstrap", "kaprocrds"),
	} {
		path := filepath.Join(root, relDir, "kapro.io_triggers.yaml")
		crd := readCRD(t, path)
		version := servedCRDVersion(t, path, crd)
		for _, jsonPath := range []string{".spec.suspended", ".spec.promotionTemplate.suspended"} {
			node := schemaNodeForJSONPath(t, path, version.Schema.OpenAPIV3Schema, jsonPath)
			got, ok := node["default"].(bool)
			if !ok || !got {
				t.Fatalf("%s schema %s default=%#v, want true", path, jsonPath, node["default"])
			}
		}
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

func TestStaticCRDsDoNotDeclareUntrustedConversionWebhook(t *testing.T) {
	root := repoRoot(t)
	for _, relDir := range []string{
		filepath.Join("config", "crd", "bases"),
		filepath.Join("charts", "kapro-operator", "crds"),
		filepath.Join("internal", "bootstrap", "kaprocrds"),
	} {
		for _, name := range crdBaseNames(t, filepath.Join(root, relDir)) {
			path := filepath.Join(root, relDir, name)
			data := readText(t, path)
			if strings.Contains(data, "strategy: Webhook") && !strings.Contains(data, "caBundle:") {
				t.Fatalf("%s declares a conversion webhook without a trusted caBundle; static CRDs cannot use chart-generated webhook CAs", path)
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
	defaultControllers := helmDefaultControllers(t, filepath.Join(root, "charts", "kapro-operator", "values.yaml"))
	wantDefaultControllers := []string{"fleet", "plan", "promotion", "promotionrun", "cluster"}
	if !sameStringSet(defaultControllers, wantDefaultControllers) {
		t.Fatalf("chart default controllers = %v, want ADR-0010 core set %v", defaultControllers, wantDefaultControllers)
	}
	for _, controller := range defaultControllers {
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

func TestPromotionRunCRDUsesSummaryNotPersistedTargets(t *testing.T) {
	root := repoRoot(t)
	wantPrintColumns := map[string]string{
		"Targets": ".status.summary.totalTargets",
		"Synced":  ".status.summary.syncedTargets",
		"Failed":  ".status.summary.failedTargets",
	}
	for _, relPath := range []string{
		filepath.Join("config", "crd", "bases", "kapro.io_promotionruns.yaml"),
		filepath.Join("charts", "kapro-operator", "crds", "kapro.io_promotionruns.yaml"),
		filepath.Join("internal", "bootstrap", "kaprocrds", "kapro.io_promotionruns.yaml"),
	} {
		path := filepath.Join(root, relPath)
		crd := readCRD(t, path)
		version := servedCRDVersion(t, path, crd)
		statusNode := crdSubtreeNode(t, path, version.Schema.OpenAPIV3Schema, "status")
		if required, ok := statusNode["required"].([]any); ok && containsAny(required, "summary") {
			t.Fatalf("%s requires PromotionRun.status.summary; summary must stay optional for status merge patches", path)
		}
		statusProps := crdSubtreeProperties(t, path, version.Schema.OpenAPIV3Schema, "status")
		if _, ok := statusProps["targets"]; ok {
			t.Fatalf("%s exposes removed PromotionRun.status.targets; per-target state must live in child Target objects", path)
		}
		summary, ok := statusProps["summary"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing PromotionRun.status.summary", path)
		}
		summaryProps, _ := summary["properties"].(map[string]any)
		for _, want := range []string{"totalTargets", "syncedTargets", "failedTargets", "pendingTargets", "convergedAt"} {
			if _, ok := summaryProps[want]; !ok {
				t.Fatalf("%s missing PromotionRun.status.summary.%s", path, want)
			}
		}
		printColumns := map[string]string{}
		for _, column := range version.AdditionalPrinterColumns {
			printColumns[column.Name] = column.JSONPath
		}
		for name, wantPath := range wantPrintColumns {
			if got := printColumns[name]; got != wantPath {
				t.Fatalf("%s printcolumn %s JSONPath=%q, want %q", path, name, got, wantPath)
			}
		}
		for _, column := range version.AdditionalPrinterColumns {
			if _, ok := wantPrintColumns[column.Name]; ok && column.Priority != 0 {
				t.Fatalf("%s printcolumn %s priority=%d, want default-visible priority 0", path, column.Name, column.Priority)
			}
		}
	}
}

func TestPromotionRunStatusHasNoRuntimeOnlyJSONFields(t *testing.T) {
	root := repoRoot(t)
	typesText := readText(t, filepath.Join(root, "api", "v1alpha2", "promotionrun_types.go"))
	body := structBody(t, typesText, "PromotionRunStatus")
	if strings.Contains(body, `json:"-"`) {
		t.Fatalf("PromotionRunStatus contains json:\"-\" runtime-only fields; keep controller scratch state out of the exported API struct")
	}
	if strings.Contains(body, "RuntimeTargets") {
		t.Fatalf("PromotionRunStatus contains RuntimeTargets; child Target objects must be the only per-target state surface")
	}
}

func TestPromotionRunSummaryStaysAggregateOnly(t *testing.T) {
	root := repoRoot(t)
	typesText := readText(t, filepath.Join(root, "api", "v1alpha2", "promotionrun_types.go"))
	got := jsonTagsForStruct(t, typesText, "PromotionRunSummary")
	want := []string{"totalTargets", "syncedTargets", "failedTargets", "pendingTargets", "convergedAt"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("PromotionRunSummary JSON fields = %v, want aggregate-only fields %v", got, want)
	}
	for _, field := range got {
		if strings.Contains(strings.ToLower(field), "targetname") ||
			strings.Contains(strings.ToLower(field), "cluster") ||
			strings.Contains(strings.ToLower(field), "stage") ||
			strings.Contains(strings.ToLower(field), "message") {
			t.Fatalf("PromotionRunSummary field %q looks like per-target detail; keep detail in child Target objects", field)
		}
	}
}

func TestCRDPrintColumnsResolveToSchemaFields(t *testing.T) {
	root := repoRoot(t)
	for _, relDir := range []string{
		filepath.Join("config", "crd", "bases"),
		filepath.Join("charts", "kapro-operator", "crds"),
		filepath.Join("internal", "bootstrap", "kaprocrds"),
	} {
		for _, name := range crdBaseNames(t, filepath.Join(root, relDir)) {
			path := filepath.Join(root, relDir, name)
			crd := readCRD(t, path)
			version := servedCRDVersion(t, path, crd)
			for _, column := range version.AdditionalPrinterColumns {
				if column.JSONPath == "" || strings.HasPrefix(column.JSONPath, ".metadata.") {
					continue
				}
				if !schemaHasJSONPath(version.Schema.OpenAPIV3Schema, column.JSONPath) {
					t.Fatalf("%s printcolumn %q JSONPath %q does not resolve in CRD schema", path, column.Name, column.JSONPath)
				}
			}
		}
	}
}

func TestCRDShortNamesAndCategoriesUseExpectedAliases(t *testing.T) {
	root := repoRoot(t)
	expectedShortNames := map[string][]string{
		"kapro.io_adapterpolicies.yaml":  {"adp"},
		"kapro.io_approvals.yaml":        {"ap"},
		"kapro.io_backends.yaml":         {"be"},
		"kapro.io_clusters.yaml":         {"cl"},
		"kapro.io_clustertemplates.yaml": {"ct"},
		"kapro.io_fleets.yaml":           {"flt"},
		"kapro.io_gateexpressions.yaml":  {"gex"},
		"kapro.io_plans.yaml":            {"pl"},
		"kapro.io_plugins.yaml":          {"plug"},
		"kapro.io_policies.yaml":         {"pol"},
		"kapro.io_promotionruns.yaml":    {"prun"},
		"kapro.io_promotions.yaml":       {"promo"},
		"kapro.io_sources.yaml":          {"src"},
		"kapro.io_targets.yaml":          {"tgt"},
		"kapro.io_triggers.yaml":         {"trig"},
	}
	for _, relDir := range []string{
		filepath.Join("config", "crd", "bases"),
		filepath.Join("charts", "kapro-operator", "crds"),
		filepath.Join("internal", "bootstrap", "kaprocrds"),
	} {
		seenShortNames := map[string]string{}
		for file, wantShortNames := range expectedShortNames {
			path := filepath.Join(root, relDir, file)
			crd := readCRD(t, path)
			if got := crd.Spec.Names.ShortNames; fmt.Sprint(got) != fmt.Sprint(wantShortNames) {
				t.Fatalf("%s shortNames=%v, want %v", path, got, wantShortNames)
			}
			for _, shortName := range crd.Spec.Names.ShortNames {
				if previous := seenShortNames[shortName]; previous != "" {
					t.Fatalf("%s shortName %q duplicates %s", path, shortName, previous)
				}
				seenShortNames[shortName] = file
			}
			if got := crd.Spec.Names.Categories; fmt.Sprint(got) != fmt.Sprint([]string{"kapro-all"}) {
				t.Fatalf("%s categories=%v, want [kapro-all]", path, got)
			}
		}
	}
}

func TestGateExpressionCRDPublishesCompletedAlgebra(t *testing.T) {
	root := repoRoot(t)
	crdPath := filepath.Join(root, "config", "crd", "bases", "kapro.io_gateexpressions.yaml")
	crd := readCRD(t, crdPath)
	version := servedCRDVersion(t, crdPath, crd)
	operator := schemaNodeForJSONPath(t, crdPath, version.Schema.OpenAPIV3Schema, ".spec.operator")
	enum, _ := operator["enum"].([]any)
	want := []any{"ALL", "ANY", "NOT", "WEIGHTED_SUM", "THRESHOLD", "DELAY"}
	if fmt.Sprint(enum) != fmt.Sprint(want) {
		t.Fatalf("%s spec.operator enum=%v, want %v", crdPath, enum, want)
	}
	_ = schemaNodeForJSONPath(t, crdPath, version.Schema.OpenAPIV3Schema, ".status.firstObservedAt")
}

func TestGatePolicyExpressionRefIsReservedInCRDSchema(t *testing.T) {
	root := repoRoot(t)
	crdPath := filepath.Join(root, "config", "crd", "bases", "kapro.io_plans.yaml")
	crd := readCRD(t, crdPath)
	version := servedCRDVersion(t, crdPath, crd)
	gate := schemaNodeForJSONPath(t, crdPath, version.Schema.OpenAPIV3Schema, ".spec.stages.gate")
	if !hasValidationRule(gate, "!has(self.expressionRef)") {
		t.Fatalf("%s stage gate is missing expressionRef reserved validation", crdPath)
	}
}

func TestKaproResourceAttributeLiteralsMatchCRDPlurals(t *testing.T) {
	root := repoRoot(t)
	decisionAPI := readText(t, filepath.Join(root, "internal", "webhook", "decision_api.go"))
	attrRE := regexp.MustCompile(`kaproAttrs\("[^"]+",\s*"([^"]+)"`)
	for _, match := range attrRE.FindAllStringSubmatch(decisionAPI, -1) {
		if !kaproPluralSet[match[1]] {
			t.Fatalf("Decision API uses unknown kapro.io resource %q", match[1])
		}
	}
	subresourceRE := regexp.MustCompile(`kaproSubresourceAttrs\("[^"]+",\s*"([^"]+)",\s*"([^"]+)"`)
	for _, match := range subresourceRE.FindAllStringSubmatch(decisionAPI, -1) {
		resource := match[1] + "/" + match[2]
		if !kaproPluralSet[resource] {
			t.Fatalf("Decision API uses unknown kapro.io subresource %q", resource)
		}
	}

	bootstrap := readText(t, filepath.Join(root, "internal", "controller", "cluster_bootstrap_helpers.go"))
	kaproRuleRE := regexp.MustCompile(`(?s)APIGroups:\s*\[\]string\{"kapro\.io"\},\s*Resources:\s*\[\]string\{([^}]+)\}`)
	for _, match := range kaproRuleRE.FindAllStringSubmatch(bootstrap, -1) {
		for _, resource := range commaList(match[1]) {
			if !kaproPluralSet[resource] {
				t.Fatalf("cluster bootstrap RBAC uses unknown kapro.io resource %q", resource)
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

func TestPreStableReleaseTrainMarkersStayDocumented(t *testing.T) {
	root := repoRoot(t)
	releaseTrain := readText(t, filepath.Join(root, "docs", "release-train.md"))
	roadmap := readText(t, filepath.Join(root, "docs", "pre-stable-roadmap.md"))
	v023Scope := readText(t, filepath.Join(root, "docs", "v0.2.3-scope.md"))
	apiStability := readText(t, filepath.Join(root, "docs", "api-stability.md"))
	sdkVersioning := readText(t, filepath.Join(root, "docs", "adr", "0013-sdk-versioning-policy.md"))

	for _, want := range []string{"0.x.x", "v0.2.4", "v0.4.7", "v0.4.20"} {
		if !strings.Contains(releaseTrain, want) {
			t.Fatalf("docs/release-train.md does not mention %s", want)
		}
		if !strings.Contains(roadmap, want) {
			t.Fatalf("docs/pre-stable-roadmap.md does not mention %s", want)
		}
	}
	if !strings.Contains(roadmap, "The first version digit remains\n`0` for roadmap work.") {
		t.Fatalf("docs/pre-stable-roadmap.md does not keep roadmap work on the 0.x.x train")
	}
	for _, want := range []string{"v0.2.4", "v0.4.7", "v0.4.20"} {
		if !strings.Contains(releaseTrain, want) {
			t.Fatalf("docs/release-train.md does not mention exact milestone example %s", want)
		}
		if !strings.Contains(roadmap, want) {
			t.Fatalf("docs/pre-stable-roadmap.md does not mention exact milestone example %s", want)
		}
	}
	for _, bad := range []string{"`v0.6`", "`v0.10.0`"} {
		if !strings.Contains(releaseTrain, bad) || !strings.Contains(roadmap, bad) {
			t.Fatalf("release train docs should explicitly reject shorthand milestone %s", bad)
		}
	}
	for _, want := range []string{"v0.2.3", "0.x.x", "ClusterClassifier", "delivery promotion"} {
		if !strings.Contains(v023Scope, want) {
			t.Fatalf("docs/v0.2.3-scope.md does not mention %s", want)
		}
	}
	for _, body := range []string{releaseTrain, roadmap, apiStability, sdkVersioning} {
		if !strings.Contains(body, "0.<capability-line>.<feature-increment>") {
			t.Fatalf("pre-stable release docs do not define the capability-line/feature-increment numbering strategy")
		}
	}
	for path, body := range map[string]string{
		"docs/api-stability.md":                  apiStability,
		"docs/adr/0013-sdk-versioning-policy.md": sdkVersioning,
	} {
		for _, want := range []string{"0.x.x", "1.0.0"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s does not mention %s", path, want)
			}
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

func TestExportedDocsDoNotReferenceRemovedPromotionRunTargets(t *testing.T) {
	root := repoRoot(t)
	stale := []*regexp.Regexp{
		regexp.MustCompile(`PromotionRun\.Status\.Targets`),
		regexp.MustCompile(`PromotionRun\.status\.targets`),
		regexp.MustCompile(`promotionrun\.status\.targets`),
	}
	for _, relDir := range []string{
		filepath.Join("api", "v1alpha2"),
		"pkg",
	} {
		err := filepath.WalkDir(filepath.Join(root, relDir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			base := filepath.Base(path)
			if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(base, "_test.go") || base == "zz_generated.deepcopy.go" {
				return nil
			}
			for _, re := range stale {
				if match := re.FindString(readText(t, path)); match != "" {
					t.Fatalf("%s contains removed PromotionRun targets contract %q", path, match)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestCRDPropertiesMatchGoJSONTags asserts that every JSON tag declared on a
// top-level CRD struct's Spec / Status (Fleet, Promotion, PromotionRun,
// Cluster, Plan, Source, Trigger, Target, Backend, Plugin, Policy,
// ClusterTemplate, Approval) appears as a property in the corresponding
// CRD's openAPIv3Schema. Catches drift where a Go field was renamed but the
// CRD wasn't regenerated, or where a JSON tag was hand-edited and the
// generator wasn't rerun.
func TestCRDPropertiesMatchGoJSONTags(t *testing.T) {
	root := repoRoot(t)
	// Map Kind → (typesFile, specStructName, statusStructName?).
	// statusStructName is empty for Kinds whose Status is opaque
	// (e.g. Plan has no status).
	type fixture struct {
		typesFile  string
		specStruct string
		statusName string
	}
	fixtures := map[string]fixture{
		"AdapterPolicy":   {"adapterpolicy_types.go", "AdapterPolicySpec", "AdapterPolicyStatus"},
		"Approval":        {"approval_types.go", "ApprovalSpec", "ApprovalStatus"},
		"Backend":         {"backend_types.go", "BackendSpec", "BackendStatus"},
		"Cluster":         {"cluster_types.go", "ClusterSpec", "ClusterStatus"},
		"ClusterTemplate": {"clustertemplate_types.go", "ClusterTemplateSpec", "ClusterTemplateStatus"},
		"Fleet":           {"fleet_types.go", "FleetSpec", "FleetStatus"},
		"GateExpression":  {"gateexpression_types.go", "GateExpressionSpec", "GateExpressionStatus"},
		"Plan":            {"promotionrun_types.go", "PlanSpec", ""},
		"Plugin":          {"plugin_types.go", "PluginSpec", "PluginStatus"},
		"Policy":          {"policy_types.go", "PolicySpec", "PolicyStatus"},
		"Promotion":       {"promotion_types.go", "PromotionSpec", "PromotionStatus"},
		"PromotionRun":    {"promotionrun_types.go", "PromotionRunSpec", "PromotionRunStatus"},
		"Source":          {"source_types.go", "SourceSpec", ""},
		"Target":          {"promotionrun_types.go", "TargetSpec", "TargetStatus"},
		"Trigger":         {"trigger_types.go", "TriggerSpec", "TriggerStatus"},
	}

	for _, contract := range kaproResources {
		fix, ok := fixtures[contract.Kind]
		if !ok {
			t.Errorf("no test fixture for Kind %s — please add one when introducing a new CRD", contract.Kind)
			continue
		}
		typesPath := filepath.Join(root, "api", "v1alpha2", fix.typesFile)
		typesText := readText(t, typesPath)

		crdName := "kapro.io_" + contract.Plural + ".yaml"
		crdPath := filepath.Join(root, "config", "crd", "bases", crdName)
		crd := readCRD(t, crdPath)
		version := servedCRDVersion(t, crdPath, crd)

		specTags := jsonTagsForStruct(t, typesText, fix.specStruct)
		assertTagsInCRDSubtree(t, crdName, "spec", version.Schema.OpenAPIV3Schema, specTags)

		if fix.statusName != "" {
			statusTags := jsonTagsForStruct(t, typesText, fix.statusName)
			assertTagsInCRDSubtree(t, crdName, "status", version.Schema.OpenAPIV3Schema, statusTags)
		}
	}
}

// jsonTagsForStruct returns the JSON-tag names (the part before the first
// comma) of every field on the named struct in typesText. Inlined embedded
// fields (`json:",inline"`) and tags equal to "-" are skipped.
func jsonTagsForStruct(t *testing.T, typesText, structName string) []string {
	t.Helper()
	body := structBody(t, typesText, structName)
	tagRE := regexp.MustCompile("`json:\"([^\"]+)\"`")
	var tags []string
	for line := range strings.SplitSeq(body, "\n") {
		tagMatch := tagRE.FindStringSubmatch(line)
		if tagMatch == nil {
			continue
		}
		raw := tagMatch[1]
		name := strings.SplitN(raw, ",", 2)[0]
		if name == "" || name == "-" {
			// `json:",inline"` — promoted; embedded fields are not testable
			// here without true reflect, and they don't add to the spec
			// surface independently. Skip safely.
			continue
		}
		tags = append(tags, name)
	}
	return tags
}

func structBody(t *testing.T, typesText, structName string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?ms)^type\s+` + regexp.QuoteMeta(structName) + `\s+struct\s*\{(.*?)\n\}`)
	match := pattern.FindStringSubmatch(typesText)
	if match == nil {
		t.Fatalf("could not find struct %s in supplied types file", structName)
	}
	return match[1]
}

// assertTagsInCRDSubtree verifies every tag appears as a property under
// the given subtree key (spec or status) of the CRD's openAPIv3Schema.
func assertTagsInCRDSubtree(t *testing.T, crdName, subtree string, schema map[string]any, tags []string) {
	t.Helper()
	props := crdSubtreeProperties(t, crdName, schema, subtree)
	if props == nil && len(tags) > 0 {
		t.Fatalf("%s: .%s has no properties but Go struct has %d JSON tags", crdName, subtree, len(tags))
	}
	for _, tag := range tags {
		if _, ok := props[tag]; !ok {
			t.Errorf("%s: Go JSON tag .%s.%s has no corresponding property in CRD openAPIv3Schema (run `make manifests sync-crds`)", crdName, subtree, tag)
		}
	}
}

func crdSubtreeProperties(t *testing.T, crdName string, schema map[string]any, subtree string) map[string]any {
	t.Helper()
	node := crdSubtreeNode(t, crdName, schema, subtree)
	props, _ := node["properties"].(map[string]any)
	return props
}

func crdSubtreeNode(t *testing.T, crdName string, schema map[string]any, subtree string) map[string]any {
	t.Helper()
	subtreeNode, _ := schema["properties"].(map[string]any)
	if subtreeNode == nil {
		t.Fatalf("%s: schema has no properties", crdName)
	}
	node, _ := subtreeNode[subtree].(map[string]any)
	if node == nil {
		t.Fatalf("%s: schema has no .%s properties", crdName, subtree)
	}
	return node
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasValidationRule(schema map[string]any, wantSubstring string) bool {
	validations, _ := schema["x-kubernetes-validations"].([]any)
	for _, validation := range validations {
		entry, _ := validation.(map[string]any)
		rule, _ := entry["rule"].(string)
		if strings.Contains(rule, wantSubstring) {
			return true
		}
	}
	return false
}

// TestSpokeRBACRulesUseCurrentCRDPlurals asserts every resource name in the
// hardcoded RBAC PolicyRule list inside internal/controller/cluster_bootstrap_helpers.go
// matches a current v1alpha2 CRD plural. Catches drift like requesting
// `fleetclusters` (which no longer exists) after the v1alpha1 → v1alpha2
// rename — a Role grant on a nonexistent resource silently fails closed in
// production.
func TestSpokeRBACRulesUseCurrentCRDPlurals(t *testing.T) {
	root := repoRoot(t)
	helpersPath := filepath.Join(root, "internal", "controller", "cluster_bootstrap_helpers.go")
	helpers := readText(t, helpersPath)

	// Find every `Resources: []string{"x", "y/status", ...}` block and
	// extract every quoted string inside it.
	blockRE := regexp.MustCompile(`(?s)Resources:\s*\[\]string\{([^}]*)\}`)
	stringRE := regexp.MustCompile(`"([^"]+)"`)

	for _, block := range blockRE.FindAllStringSubmatch(helpers, -1) {
		for _, lit := range stringRE.FindAllStringSubmatch(block[1], -1) {
			res := lit[1]
			// Allow non-kapro.io resource literals (e.g. "leases",
			// "selfsubjectaccessreviews") — they're scoped to other API
			// groups elsewhere in the same rule block. We only care that
			// stale kapro.io plurals like "fleetclusters" aren't present.
			if !looksLikeKaproPlural(res) {
				continue
			}
			if !kaproPluralSet[res] {
				t.Errorf("cluster_bootstrap_helpers.go: stale kapro.io RBAC resource literal %q — not a current CRD plural (compare to kaproResources)", res)
			}
		}
	}
}

// legacyKaproPlurals are the v1alpha1 plurals we want to fail loudly on.
// If any persist as RBAC resource literals, the granted role does nothing
// at runtime because the resource no longer exists in v1alpha2.
var legacyKaproPlurals = []string{
	"kaproes", "fleetclusters", "fleetclustertemplates",
	"agentpolicies", "promotionsources", "promotiontriggers",
	"promotionplans", "promotiontargets", "backendprofiles",
	"pluginregistrations",
}

// looksLikeKaproPlural returns true for strings that should be checked
// against the current CRD plural set — i.e. anything that is either a
// current kapro.io plural OR a known legacy v1alpha1 plural we still want
// the test to flag.
func looksLikeKaproPlural(s string) bool {
	if kaproPluralSet[s] {
		return true
	}
	base := strings.TrimSuffix(strings.TrimSuffix(s, "/status"), "/finalizers")
	return slices.Contains(legacyKaproPlurals, base)
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
	if filepath.Base(path) == "migration-v1alpha1-to-v1alpha2.md" {
		// This page is the only user-facing document allowed to spell the
		// legacy API surface because it is the explicit migration guide.
		return
	}
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

func readCRD(t *testing.T, path string) crdDocument {
	t.Helper()
	var crd crdDocument
	if err := yaml.Unmarshal(readBytes(t, path), &crd); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return crd
}

func servedCRDVersion(t *testing.T, path string, crd crdDocument) crdVersion {
	t.Helper()
	for _, version := range crd.Spec.Versions {
		if version.Name == "v1alpha2" {
			return version
		}
	}
	t.Fatalf("%s missing v1alpha2 CRD version", path)
	return crd.Spec.Versions[0]
}

func schemaHasJSONPath(schema map[string]any, jsonPath string) bool {
	return schemaNodeForJSONPathNoFail(schema, jsonPath) != nil
}

func schemaNodeForJSONPath(t *testing.T, path string, schema map[string]any, jsonPath string) map[string]any {
	t.Helper()
	node := schemaNodeForJSONPathNoFail(schema, jsonPath)
	if node == nil {
		t.Fatalf("%s JSONPath %s does not resolve in CRD schema", path, jsonPath)
	}
	return node
}

func schemaNodeForJSONPathNoFail(schema map[string]any, jsonPath string) map[string]any {
	parts := normalizedJSONPathParts(jsonPath)
	current := schema
	for i, part := range parts {
		props, ok := current["properties"].(map[string]any)
		if !ok {
			return nil
		}
		nextAny, ok := props[part]
		if !ok {
			return nil
		}
		next, ok := nextAny.(map[string]any)
		if !ok {
			return nil
		}
		if i < len(parts)-1 {
			if items, ok := next["items"].(map[string]any); ok {
				next = items
			}
		}
		current = next
	}
	return current
}

func normalizedJSONPathParts(jsonPath string) []string {
	path := strings.TrimPrefix(jsonPath, ".")
	if strings.HasPrefix(path, "metadata.") {
		return nil
	}
	path = regexp.MustCompile(`\[\?\([^\]]+\)\]`).ReplaceAllString(path, "")
	path = strings.TrimSuffix(path, ".length()")
	path = strings.ReplaceAll(path, `\.`, "\x00")
	raw := strings.Split(path, ".")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.ReplaceAll(part, "\x00", ".")
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
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

func sameStringSet(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
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

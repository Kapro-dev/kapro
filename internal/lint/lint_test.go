package lint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// findIssue returns the first issue whose Path or Message contains
// substring s. Tests assert on substrings instead of full equality so
// message wording can evolve without breaking every fixture.
func findIssue(t *testing.T, issues []Issue, s string) *Issue {
	t.Helper()
	for i := range issues {
		if strings.Contains(issues[i].Path, s) || strings.Contains(issues[i].Message, s) {
			return &issues[i]
		}
	}
	return nil
}

func TestLintPromotion_RejectsMissingRequired(t *testing.T) {
	p := &kaprov1alpha1.Promotion{}
	issues := LintPromotion(p)

	for _, want := range []string{"metadata.name", "fleet", "unit", "version"} {
		hit := findIssue(t, issues, want)
		if hit == nil {
			t.Errorf("missing expected issue for %q; got %+v", want, issues)
			continue
		}
		if hit.Severity != SeverityError {
			t.Errorf("issue %q should be ERROR, got %s", want, hit.Severity)
		}
	}
}

func TestLintDeliveryUnit_RejectsInvalidSourceAndTriggers(t *testing.T) {
	du := &kaprov1alpha1.DeliveryUnit{
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{
					{Name: "api"},
					{Name: "api"},
					{},
				},
			},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{
				{
					Source: kaprov1alpha1.TriggerSource{Type: "oci"},
				},
				{
					Source: kaprov1alpha1.TriggerSource{Type: "git"},
				},
			},
		},
	}
	issues := LintDeliveryUnit(du)

	for _, want := range []string{
		"metadata.name",
		"duplicate unit",
		"unit name",
		"trigger requires fleet",
		"source.oci",
		"duplicate derived Trigger suffix",
		"unsupported trigger source type",
	} {
		hit := findIssue(t, issues, want)
		if hit == nil {
			t.Errorf("missing expected DeliveryUnit issue for %q; got %+v", want, issues)
			continue
		}
		if hit.Severity != SeverityError {
			t.Errorf("issue %q should be ERROR, got %s", want, hit.Severity)
		}
	}
}

func TestLintFile_DeliveryUnitDispatch(t *testing.T) {
	issues := LintFile("du.yaml", []byte(`apiVersion: kapro.io/v1alpha1
kind: DeliveryUnit
metadata:
  name: checkout
spec:
  source:
    units:
      - name: api
  defaultFleet: checkout
  triggers:
    - source:
        type: oci
        oci:
          repository: ghcr.io/example/checkout
          tagPattern: '^v'
`))
	if len(issues) != 0 {
		t.Fatalf("expected valid DeliveryUnit, got %+v", issues)
	}
}

func TestLintPromotion_WarnsOnNoTimeout(t *testing.T) {
	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha1.PromotionSpec{DeliveryUnitRef: "du", FleetRef: "k", Version: "v1"},
	}
	issues := LintPromotion(p)
	hit := findIssue(t, issues, "timeout")
	if hit == nil {
		t.Fatalf("expected timeout warning; got %+v", issues)
	}
	if hit.Severity != SeverityWarn {
		t.Errorf("timeout should be WARN, got %s", hit.Severity)
	}
}

func TestLintPromotion_DuplicateScopeTargetsWarn(t *testing.T) {
	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec: kaprov1alpha1.PromotionSpec{
			FleetRef:        "k",
			DeliveryUnitRef: "du",
			Version:         "v1",
			Timeout:         "30m",
			Scope: &kaprov1alpha1.PromotionRunScope{
				Targets: []string{"de-prod", "de-prod"},
			},
		},
	}
	issues := LintPromotion(p)
	if findIssue(t, issues, "duplicate") == nil {
		t.Fatalf("expected duplicate-target warning; got %+v", issues)
	}
}

func TestLintPromotionPlan_DuplicateStageNamesAreErrors(t *testing.T) {
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "canary"},
				{Name: "canary"},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	hit := findIssue(t, issues, "duplicate stage")
	if hit == nil || hit.Severity != SeverityError {
		t.Fatalf("expected duplicate-stage ERROR; got %+v", issues)
	}
}

func TestLintPromotionPlan_DanglingDependsOn(t *testing.T) {
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "canary"},
				{Name: "prod", DependsOn: []kaprov1alpha1.StageDependency{{Stage: "ghost"}}},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	hit := findIssue(t, issues, "unknown stage")
	if hit == nil || hit.Severity != SeverityError {
		t.Fatalf("expected dangling-dependsOn ERROR; got %+v", issues)
	}
}

func TestLintPromotionPlan_DependsOnSelfIsError(t *testing.T) {
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "loop", DependsOn: []kaprov1alpha1.StageDependency{{Stage: "loop"}}},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	if findIssue(t, issues, "depend on itself") == nil {
		t.Fatalf("expected self-dependency ERROR; got %+v", issues)
	}
}

func TestLintPromotionPlan_CycleDetected(t *testing.T) {
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "a", DependsOn: []kaprov1alpha1.StageDependency{{Stage: "b"}}},
				{Name: "b", DependsOn: []kaprov1alpha1.StageDependency{{Stage: "c"}}},
				{Name: "c", DependsOn: []kaprov1alpha1.StageDependency{{Stage: "a"}}},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	if findIssue(t, issues, "cycle") == nil {
		t.Fatalf("expected cycle ERROR; got %+v", issues)
	}
}

func TestLintPromotionPlan_ManualGateWithoutApproversWarns(t *testing.T) {
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "prod", Gate: &kaprov1alpha1.GatePolicySpec{
					Mode:     kaprov1alpha1.GateModeManual,
					Approval: &kaprov1alpha1.ApprovalConfig{Required: true},
				}},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	hit := findIssue(t, issues, "no approvers")
	if hit == nil || hit.Severity != SeverityWarn {
		t.Fatalf("expected no-approvers WARN; got %+v", issues)
	}
}

func TestLintFile_UnknownKaproKindSkipsSilently(t *testing.T) {
	// Other Kapro kinds (FleetCluster, SubstrateProfile, etc.) have
	// no rules yet; the linter must NOT flag them just because
	// they passed through. Running `kapro lint **/*.yaml` should
	// only surface real issues, not noise.
	issues := LintFile("x.yaml", []byte(`
apiVersion: kapro.io/v1alpha1
kind: FleetCluster
metadata:
  name: c
`))
	if len(issues) != 0 {
		t.Fatalf("expected no issues for unknown kind; got %+v", issues)
	}
}

func TestLintFile_MultiDocSplitsCleanly(t *testing.T) {
	issues := LintFile("multi.yaml", []byte(`apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: a
spec:
  unit: du
  fleet: k
  version: v1
  timeout: 30m
---
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: b
spec:
  unit: du
  fleet: k
  # missing version
`))
	// First doc: clean.
	// Second doc: should produce a version error tagged with doc index 1.
	hit := findIssue(t, issues, "version")
	if hit == nil {
		t.Fatalf("expected version ERROR in second doc; got %+v", issues)
	}
	if hit.DocIndex != 1 {
		t.Errorf("expected DocIndex=1, got %d (issue=%+v)", hit.DocIndex, *hit)
	}
}

func TestLintFile_GarbledYAMLIsAnError(t *testing.T) {
	issues := LintFile("bad.yaml", []byte(`apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: a
spec:
  scope:
    targets:
      - this is a string
      - {but: this is a map, mixed: types}
`))
	// At least one issue should surface — could be parse or could be
	// schema (depends on yaml lib's leniency); the important contract
	// is that we don't silently accept this.
	if len(issues) == 0 {
		t.Fatalf("expected at least one issue; got none")
	}
}

func TestExampleYAMLHasNoKaproLintErrors(t *testing.T) {
	root := lintRepoRoot(t)
	examplesDir := filepath.Join(root, "examples")
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml":
		default:
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, issue := range LintFile(filepath.ToSlash(rel), data) {
			if issue.Severity == SeverityError {
				t.Errorf("%s", issue.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuickstartYAMLIsStrictLintClean(t *testing.T) {
	root := lintRepoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "examples", "01-quickstarts", "00-flux", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no quickstart YAML files found")
	}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for _, issue := range LintFile(filepath.ToSlash(rel), data) {
			t.Errorf("%s", issue.String())
		}
	}
}

// ---- LintKapro -------------------------------------------------------------

func TestLintKapro_NilSourceDoesNotPanic(t *testing.T) {
	// Regression guard. FleetSpec.Source is *SourceSpec and is
	// nil whenever the user does not declare an inline source — i.e.
	// every Kapro that uses sourceRef. An earlier version of LintKapro
	// dereferenced k.Spec.Source.Units unconditionally and panicked.
	k := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.FleetSpec{
			SourceRef: "shared-catalog",
			Substrate: kaprov1alpha1.SubstrateBindingSpec{Ref: "flux"},
			Clusters:  []kaprov1alpha1.ClusterRef{{Name: "c1"}},
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LintKapro panicked with nil Source: %v", r)
		}
	}()
	issues := LintKapro(k)
	for _, i := range issues {
		if i.Severity == SeverityError {
			t.Errorf("nil-Source + valid sourceRef should not error; got %+v", i)
		}
	}
}

func TestLintKapro_SourceAndSourceRefCompatibility(t *testing.T) {
	cases := []struct {
		name      string
		sourceRef string
		source    *kaprov1alpha1.SourceSpec
		wantErr   bool
	}{
		{name: "target-set fleet without legacy source"},
		{name: "only sourceRef", sourceRef: "shared"},
		{
			name:   "only inline source",
			source: &kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "u"}}},
		},
		{
			name:      "both set",
			sourceRef: "shared",
			source:    &kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "u"}}},
			wantErr:   true,
		},
		{
			name:   "source non-nil but empty units",
			source: &kaprov1alpha1.SourceSpec{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := &kaprov1alpha1.Fleet{
				ObjectMeta: metav1.ObjectMeta{Name: "k1"},
				Spec: kaprov1alpha1.FleetSpec{
					SourceRef: tc.sourceRef,
					Source:    tc.source,
					Substrate: kaprov1alpha1.SubstrateBindingSpec{Ref: "flux"},
					Clusters:  []kaprov1alpha1.ClusterRef{{Name: "c1"}},
				},
			}
			issues := LintKapro(k)
			got := findIssue(t, issues, "source")
			if tc.wantErr {
				if got == nil || got.Severity != SeverityError {
					t.Fatalf("expected source/sourceRef ERROR; got %+v", issues)
				}
			} else if got != nil && got.Severity == SeverityError {
				t.Fatalf("did not expect source/sourceRef ERROR; got %+v", got)
			}
		})
	}
}

func TestLintKapro_MissingSubstrateIsError(t *testing.T) {
	k := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.FleetSpec{
			SourceRef: "shared",
			Clusters:  []kaprov1alpha1.ClusterRef{{Name: "c1"}},
		},
	}
	issues := LintKapro(k)
	hit := findIssue(t, issues, "spec.delivery.ref")
	if hit == nil || hit.Severity != SeverityError {
		t.Fatalf("expected spec.delivery.ref ERROR; got %+v", issues)
	}
}

func TestLintKapro_NoClustersWarn(t *testing.T) {
	k := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.FleetSpec{
			SourceRef: "shared",
			Substrate: kaprov1alpha1.SubstrateBindingSpec{Ref: "flux"},
		},
	}
	issues := LintKapro(k)
	hit := findIssue(t, issues, "clusters")
	if hit == nil || hit.Severity != SeverityWarn {
		t.Fatalf("expected clusters WARN; got %+v", issues)
	}
}

// ---- Manual-gate severity upgrade ------------------------------------------

func TestLintPromotionPlan_ManualGateWithRequiredFalseIsError(t *testing.T) {
	// approval.required=false on a manual gate materially breaks the
	// user's intent ("wait for a human"). Reviewer flagged this as
	// CHANGELOG/code drift in PR #96 — upgraded from WARN to ERROR.
	pp := &kaprov1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{
				{Name: "prod", Gate: &kaprov1alpha1.GatePolicySpec{
					Mode:     kaprov1alpha1.GateModeManual,
					Approval: &kaprov1alpha1.ApprovalConfig{Required: false},
				}},
			},
		},
	}
	issues := LintPromotionPlan(pp)
	hit := findIssue(t, issues, "will NOT wait for a human")
	if hit == nil {
		t.Fatalf("expected required=false ERROR; got %+v", issues)
	}
	if hit.Severity != SeverityError {
		t.Errorf("severity = %s, want ERROR", hit.Severity)
	}
}

// ---- LintFile edge cases ---------------------------------------------------

func TestLintFile_NonKaproApiVersionSkippedSilently(t *testing.T) {
	// A Deployment in a mixed YAML tree must not produce a warning —
	// `kapro lint **/*.yaml` is meant to be safe on heterogeneous repos.
	issues := LintFile("deploy.yaml", []byte(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
`))
	if len(issues) != 0 {
		t.Fatalf("non-Kapro manifest should be skipped; got %+v", issues)
	}
}

func TestLintFile_CommentOnlyDocSkipsSilently(t *testing.T) {
	issues := LintFile("comments.yaml", []byte(`
# this file is intentionally only comments
# generated by codegen — do not delete
`))
	if len(issues) != 0 {
		t.Fatalf("comment-only doc should be skipped; got %+v", issues)
	}
}

func TestLintFile_ExplicitNullDocSkipsSilently(t *testing.T) {
	// A multi-doc stream with a `null` separator should not produce a
	// "missing kind" error on the null doc.
	issues := LintFile("stream.yaml", []byte(`null
---
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: ok
spec:
  unit: du
  fleet: k
  version: v1
  timeout: 30m
`))
	for _, i := range issues {
		if i.Severity == SeverityError {
			t.Fatalf("null doc produced ERROR: %+v (all issues: %+v)", i, issues)
		}
	}
}

func TestHasErrors_StrictUpgrades(t *testing.T) {
	warn := []Issue{{Severity: SeverityWarn}}
	if HasErrors(warn, false) {
		t.Fatal("WARN should not be an error in lenient mode")
	}
	if !HasErrors(warn, true) {
		t.Fatal("WARN should be an error in strict mode")
	}

	errs := []Issue{{Severity: SeverityError}}
	if !HasErrors(errs, false) {
		t.Fatal("ERROR should always be an error")
	}
}

func lintRepoRoot(t *testing.T) string {
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

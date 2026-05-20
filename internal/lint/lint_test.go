package lint

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
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

	for _, want := range []string{"metadata.name", "kaproRef", "version"} {
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

func TestLintPromotion_WarnsOnNoTimeout(t *testing.T) {
	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha1.PromotionSpec{KaproRef: "k", Version: "v1"},
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
			KaproRef: "k",
			Version:  "v1",
			Timeout:  "30m",
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
	// Other Kapro kinds (FleetCluster, BackendProfile, etc.) have
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
  kaproRef: k
  version: v1
  timeout: 30m
---
apiVersion: kapro.io/v1alpha1
kind: Promotion
metadata:
  name: b
spec:
  kaproRef: k
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

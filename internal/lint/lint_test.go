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

// ---- LintKapro -------------------------------------------------------------

func TestLintKapro_NilSourceDoesNotPanic(t *testing.T) {
	// Regression guard. KaproSpec.Source is *PromotionSourceSpec and is
	// nil whenever the user does not declare an inline source — i.e.
	// every Kapro that uses sourceRef. An earlier version of LintKapro
	// dereferenced k.Spec.Source.Units unconditionally and panicked.
	k := &kaprov1alpha1.Kapro{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.KaproSpec{
			SourceRef: "shared-catalog",
			Delivery:  kaprov1alpha1.DeliverySpec{BackendRef: "flux"},
			Clusters:  []kaprov1alpha1.KaproCluster{{Name: "c1"}},
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

func TestLintKapro_ExactlyOneOfSourceSourceRef(t *testing.T) {
	cases := []struct {
		name     string
		sourceRef string
		source   *kaprov1alpha1.PromotionSourceSpec
		wantErr  bool
	}{
		{name: "both unset", wantErr: true},
		{name: "only sourceRef", sourceRef: "shared"},
		{
			name:   "only inline source",
			source: &kaprov1alpha1.PromotionSourceSpec{Units: []kaprov1alpha1.PromotionUnit{{Name: "u"}}},
		},
		{
			name:      "both set",
			sourceRef: "shared",
			source:    &kaprov1alpha1.PromotionSourceSpec{Units: []kaprov1alpha1.PromotionUnit{{Name: "u"}}},
			wantErr:   true,
		},
		{
			name:   "source non-nil but empty units",
			source: &kaprov1alpha1.PromotionSourceSpec{},
			// No sourceRef, no actual units → still "neither set".
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := &kaprov1alpha1.Kapro{
				ObjectMeta: metav1.ObjectMeta{Name: "k1"},
				Spec: kaprov1alpha1.KaproSpec{
					SourceRef: tc.sourceRef,
					Source:    tc.source,
					Delivery:  kaprov1alpha1.DeliverySpec{BackendRef: "flux"},
					Clusters:  []kaprov1alpha1.KaproCluster{{Name: "c1"}},
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

func TestLintKapro_MissingBackendIsError(t *testing.T) {
	k := &kaprov1alpha1.Kapro{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.KaproSpec{
			SourceRef: "shared",
			Clusters:  []kaprov1alpha1.KaproCluster{{Name: "c1"}},
		},
	}
	issues := LintKapro(k)
	hit := findIssue(t, issues, "backendRef")
	if hit == nil || hit.Severity != SeverityError {
		t.Fatalf("expected backendRef ERROR; got %+v", issues)
	}
}

func TestLintKapro_NoClustersWarn(t *testing.T) {
	k := &kaprov1alpha1.Kapro{
		ObjectMeta: metav1.ObjectMeta{Name: "k1"},
		Spec: kaprov1alpha1.KaproSpec{
			SourceRef: "shared",
			Delivery:  kaprov1alpha1.DeliverySpec{BackendRef: "flux"},
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
	pp := &kaprov1alpha1.PromotionPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "pp"},
		Spec: kaprov1alpha1.PromotionPlanSpec{
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
  kaproRef: k
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

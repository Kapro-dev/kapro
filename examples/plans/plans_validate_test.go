// Package plans_test ensures the reference PromotionPlan library
// parses cleanly into the v1alpha1 Go types. It is the cheapest
// possible canary that catches schema drift between the documentation
// (these YAMLs) and the CRD source-of-truth (api/v1alpha1).
package plans_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// plansFileRe matches NN-<slug>.yaml: two leading digits, dash, then a
// non-empty slug, then the .yaml extension. Anchored at both ends.
var plansFileRe = regexp.MustCompile(`^[0-9]{2}-[a-z0-9][a-z0-9-]*\.yaml$`)

func TestEveryPlanParsesAsPromotionPlan(t *testing.T) {
	matches, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no *.yaml files found in examples/plans/ — did the library move?")
	}

	for _, path := range matches {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var plan kaprov1alpha1.PromotionPlan
			if err := yaml.Unmarshal(data, &plan); err != nil {
				t.Fatalf("unmarshal %s: %v", path, err)
			}

			// Sanity invariants every reference plan must satisfy.
			if plan.Kind != "PromotionPlan" {
				t.Errorf("%s: kind = %q, want PromotionPlan", path, plan.Kind)
			}
			if plan.APIVersion != "kapro.io/v1alpha1" {
				t.Errorf("%s: apiVersion = %q, want kapro.io/v1alpha1", path, plan.APIVersion)
			}
			if plan.Name == "" {
				t.Errorf("%s: metadata.name is empty", path)
			}
			if len(plan.Spec.Stages) == 0 {
				t.Errorf("%s: spec.stages is empty — a reference plan must have at least one stage", path)
			}

			// Every dependsOn[].stage must reference a real stage name in
			// this plan. The admission webhook enforces this at runtime;
			// catching it here keeps the docs honest without needing a
			// cluster.
			known := map[string]bool{}
			for _, s := range plan.Spec.Stages {
				known[s.Name] = true
			}
			for _, s := range plan.Spec.Stages {
				for _, dep := range s.DependsOn {
					if !known[dep.Stage] {
						t.Errorf("%s: stage %q dependsOn unknown stage %q",
							path, s.Name, dep.Stage)
					}
				}
			}

			// File name should match NN-<slug>.yaml (two leading digits,
			// then a dash, then a slug). Enforced via a regex so the
			// numbered listing in README stays in sync with disk order.
			base := filepath.Base(path)
			if !plansFileRe.MatchString(base) {
				t.Errorf("%s: file should be named NN-<slug>.yaml (regex %s)",
					path, plansFileRe.String())
			}
		})
	}
}

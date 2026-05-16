package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PromotionPlanValidator validates PromotionPlan objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. At least one stage must be defined.
//  2. All stage names must be unique.
//  3. All stage dependsOn references must name existing stages.
//  4. The stage DAG must be acyclic (DFS cycle detection).
type PromotionPlanValidator struct {
	decoder admission.Decoder
}

// NewPromotionPlanValidator returns a configured PromotionPlanValidator.
func NewPromotionPlanValidator(decoder admission.Decoder) *PromotionPlanValidator {
	return &PromotionPlanValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *PromotionPlanValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var promotionplan kaprov1alpha1.PromotionPlan
	if err := v.decoder.DecodeRaw(req.Object, &promotionplan); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validatePromotionPlan(&promotionplan); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validatePromotionPlan(p *kaprov1alpha1.PromotionPlan) error {
	if err := validateMetricPresets(p); err != nil {
		return err
	}
	return validateStageDAG(p.Spec.Stages)
}

func validateMetricPresets(p *kaprov1alpha1.PromotionPlan) error {
	for name, preset := range p.Spec.MetricPresets {
		if preset.Provider == "" {
			return fmt.Errorf("promotionplan.spec.metricPresets[%q].provider must be set", name)
		}
		if preset.Query == "" {
			return fmt.Errorf("promotionplan.spec.metricPresets[%q].query must be set", name)
		}
	}
	for stageIndex, stage := range p.Spec.Stages {
		if stage.Gate == nil {
			continue
		}
		for metricIndex, metric := range stage.Gate.Gate.Metrics {
			if metric.Preset == "" {
				if metric.Provider == "" {
					return fmt.Errorf("promotionplan.spec.stages[%d].gate.gate.metrics[%d].provider must be set when preset is empty", stageIndex, metricIndex)
				}
				if metric.Query == "" {
					return fmt.Errorf("promotionplan.spec.stages[%d].gate.gate.metrics[%d].query must be set when preset is empty", stageIndex, metricIndex)
				}
				continue
			}
			if _, ok := p.Spec.MetricPresets[metric.Preset]; !ok {
				return fmt.Errorf("promotionplan.spec.stages[%d].gate.gate.metrics[%d].preset: unknown metric preset %q", stageIndex, metricIndex, metric.Preset)
			}
		}
	}
	return nil
}

// validateStageDAG checks that the flat Stage DAG is a valid directed acyclic graph.
func validateStageDAG(stages []kaprov1alpha1.Stage) error {
	if len(stages) == 0 {
		return fmt.Errorf("promotionplan.spec.stages must contain at least one stage")
	}

	index := make(map[string]int, len(stages))
	for i, s := range stages {
		if s.Name == "" {
			return fmt.Errorf("promotionplan.spec.stages[%d].name must be set", i)
		}
		if _, exists := index[s.Name]; exists {
			return fmt.Errorf("promotionplan.spec.stages: duplicate stage name %q", s.Name)
		}
		index[s.Name] = i
	}

	// Validate all dependsOn references name existing stages.
	for _, s := range stages {
		for _, dep := range s.DependsOn {
			if dep.Stage == "" {
				return fmt.Errorf("promotionplan.spec.stages[%q].dependsOn.stage must be set", s.Name)
			}
			if _, exists := index[dep.Stage]; !exists {
				return fmt.Errorf("promotionplan.spec.stages[%q].dependsOn: unknown stage %q", s.Name, dep.Stage)
			}
			if dep.Strategy != "" && dep.Strategy != kaprov1alpha1.StageDependencyAll && dep.Strategy != kaprov1alpha1.StageDependencyAny {
				return fmt.Errorf("promotionplan.spec.stages[%q].dependsOn[%q].strategy: unsupported value %q", s.Name, dep.Stage, dep.Strategy)
			}
			if dep.RequiredSoakTime != nil && dep.RequiredSoakTime.Duration < 0 {
				return fmt.Errorf("promotionplan.spec.stages[%q].dependsOn[%q].requiredSoakTime must be non-negative", s.Name, dep.Stage)
			}
		}
	}

	// DFS cycle detection on the stage DAG.
	if cycle := detectCycle(index, func(name string) []string {
		return stageDependencyNames(stages[index[name]].DependsOn)
	}); cycle != "" {
		return fmt.Errorf("promotionplan.spec.stages: cycle detected: %s", cycle)
	}

	return nil
}

// detectCycle runs iterative DFS and returns the cycle path as "a→b→c→a" or "".
func detectCycle(nodes map[string]int, deps func(string) []string) string {
	const (
		unvisited = 0
		inStack   = 1
		visited   = 2
	)
	state := make(map[string]int, len(nodes))
	path := make([]string, 0)

	var dfs func(node string) string
	dfs = func(node string) string {
		state[node] = inStack
		path = append(path, node)
		for _, dep := range deps(node) {
			switch state[dep] {
			case inStack:
				// Cycle found — build the path string.
				cycle := ""
				for _, n := range path {
					cycle += n + "→"
				}
				return cycle + dep
			case unvisited:
				if result := dfs(dep); result != "" {
					return result
				}
			}
		}
		path = path[:len(path)-1]
		state[node] = visited
		return ""
	}

	for name := range nodes {
		if state[name] == unvisited {
			if cycle := dfs(name); cycle != "" {
				return cycle
			}
		}
	}
	return ""
}

// stageDependencyNames extracts stage names from StageDependency for DAG traversal.
func stageDependencyNames(deps []kaprov1alpha1.StageDependency) []string {
	names := make([]string, len(deps))
	for i, d := range deps {
		names[i] = d.Stage
	}
	return names
}

// ValidatePromotionPlan is an exported test helper that exposes the internal validation logic.
func ValidatePromotionPlan(p *kaprov1alpha1.PromotionPlan) error {
	return validatePromotionPlan(p)
}

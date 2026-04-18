package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PipelineValidator validates Pipeline objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. At least one batch must be defined.
//  2. All batch dependsOn references must name existing batches.
//  3. The batch DAG must be acyclic (DFS cycle detection).
//  4. All promotion step dependsOn references must name existing steps.
//  5. The promotion step DAG must be acyclic (DFS cycle detection).
type PipelineValidator struct {
	decoder admission.Decoder
}

// NewPipelineValidator returns a configured PipelineValidator.
func NewPipelineValidator(decoder admission.Decoder) *PipelineValidator {
	return &PipelineValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *PipelineValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var pipeline kaprov1alpha1.Pipeline
	if err := v.decoder.DecodeRaw(req.Object, &pipeline); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validatePipeline(&pipeline); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validatePipeline(p *kaprov1alpha1.Pipeline) error {
	if err := validateBatchDAG(p.Spec.Progression.Batches); err != nil {
		return err
	}
	if err := validatePromotionStepDAG(p.Spec.Promotion.Steps); err != nil {
		return err
	}
	return nil
}

// validateBatchDAG checks that the batch progression graph is a valid DAG.
func validateBatchDAG(batches []kaprov1alpha1.Batch) error {
	if len(batches) == 0 {
		return fmt.Errorf("pipeline.spec.progression.batches must contain at least one batch")
	}

	index := make(map[string]int, len(batches))
	for i, b := range batches {
		if b.Name == "" {
			return fmt.Errorf("pipeline.spec.progression.batches[%d].name must be set", i)
		}
		if _, exists := index[b.Name]; exists {
			return fmt.Errorf("pipeline.spec.progression.batches: duplicate batch name %q", b.Name)
		}
		index[b.Name] = i
	}

	// Validate all dependsOn references exist.
	for _, b := range batches {
		for _, dep := range b.DependsOn {
			if _, exists := index[dep]; !exists {
				return fmt.Errorf("pipeline.spec.progression.batches[%q].dependsOn: unknown batch %q", b.Name, dep)
			}
		}
	}

	// DFS cycle detection on batch DAG.
	if cycle := detectCycle(index, func(name string) []string {
		return batches[index[name]].DependsOn
	}); cycle != "" {
		return fmt.Errorf("pipeline.spec.progression.batches: cycle detected: %s", cycle)
	}

	return nil
}

// validatePromotionStepDAG checks that the promotion step graph is a valid DAG.
func validatePromotionStepDAG(steps []kaprov1alpha1.PromotionStep) error {
	if len(steps) == 0 {
		return nil // promotion steps are optional
	}

	index := make(map[string]int, len(steps))
	for i, s := range steps {
		if s.Name == "" {
			return fmt.Errorf("pipeline.spec.promotion.steps[%d].name must be set", i)
		}
		if _, exists := index[s.Name]; exists {
			return fmt.Errorf("pipeline.spec.promotion.steps: duplicate step name %q", s.Name)
		}
		index[s.Name] = i
	}

	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if _, exists := index[dep]; !exists {
				return fmt.Errorf("pipeline.spec.promotion.steps[%q].dependsOn: unknown step %q", s.Name, dep)
			}
		}
	}

	if cycle := detectCycle(index, func(name string) []string {
		return steps[index[name]].DependsOn
	}); cycle != "" {
		return fmt.Errorf("pipeline.spec.promotion.steps: cycle detected: %s", cycle)
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

// ValidatePipeline is an exported test helper that exposes the internal validation logic.
func ValidatePipeline(p *kaprov1alpha1.Pipeline) error {
return validatePipeline(p)
}

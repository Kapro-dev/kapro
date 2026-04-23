package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ReleaseValidator validates Release objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.artifact must be non-empty.
//  2. spec.pipelines must have at least one pipeline reference.
//  3. Each ReleasePipelineRef must have a non-empty name and pipeline.
type ReleaseValidator struct {
	decoder admission.Decoder
}

// NewReleaseValidator returns a configured ReleaseValidator.
func NewReleaseValidator(decoder admission.Decoder) *ReleaseValidator {
	return &ReleaseValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *ReleaseValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var release kaprov1alpha1.Release
	if err := v.decoder.DecodeRaw(req.Object, &release); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateRelease(&release); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validateRelease(r *kaprov1alpha1.Release) error {
	if r.Spec.Artifact == "" {
		return fmt.Errorf("release.spec.artifact must be set")
	}

	if len(r.Spec.Pipelines) == 0 {
		return fmt.Errorf("release.spec.pipelines must have at least one pipeline reference")
	}

	index := make(map[string]int, len(r.Spec.Pipelines))
	for i, ref := range r.Spec.Pipelines {
		if ref.Name == "" {
			return fmt.Errorf("release.spec.pipelines[%d].name must be set", i)
		}
		if ref.Pipeline == "" {
			return fmt.Errorf("release.spec.pipelines[%d].pipeline must be set", i)
		}
		if _, exists := index[ref.Name]; exists {
			return fmt.Errorf("release.spec.pipelines: duplicate pipeline node name %q", ref.Name)
		}
		index[ref.Name] = i
	}

	// Validate all dependsOn references name existing pipeline nodes.
	for _, ref := range r.Spec.Pipelines {
		for _, dep := range ref.DependsOn {
			if _, exists := index[dep]; !exists {
				return fmt.Errorf("release.spec.pipelines[%q].dependsOn: unknown pipeline node %q", ref.Name, dep)
			}
		}
	}

	// DFS cycle detection on the pipeline node DAG.
	if cycle := detectCycle(index, func(name string) []string {
		return r.Spec.Pipelines[index[name]].DependsOn
	}); cycle != "" {
		return fmt.Errorf("release.spec.pipelines: cycle detected: %s", cycle)
	}

	return nil
}

// ValidateRelease is an exported test helper that exposes the internal validation logic.
func ValidateRelease(r *kaprov1alpha1.Release) error {
	return validateRelease(r)
}

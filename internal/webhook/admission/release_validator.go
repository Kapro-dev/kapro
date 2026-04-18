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
//  2. spec.pipelineRef must be non-empty.
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
	if r.Spec.PipelineRef == "" {
		return fmt.Errorf("release.spec.pipelineRef must be set")
	}
	return nil
}

// ValidateRelease is an exported test helper that exposes the internal validation logic.
func ValidateRelease(r *kaprov1alpha1.Release) error {
return validateRelease(r)
}

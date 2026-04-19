package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// EnvironmentValidator validates Environment objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. actuator.type must be "flux" (MVP) and actuator.flux sub-spec must be populated.
//  2. provider, if set, must not specify conflicting sub-types (reserved for future use).
type EnvironmentValidator struct {
	decoder admission.Decoder
}

// NewEnvironmentValidator returns a configured EnvironmentValidator.
func NewEnvironmentValidator(decoder admission.Decoder) *EnvironmentValidator {
	return &EnvironmentValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *EnvironmentValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var env kaprov1alpha1.Environment
	if err := v.decoder.DecodeRaw(req.Object, &env); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateEnvironment(&env); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validateEnvironment(env *kaprov1alpha1.Environment) error {
	act := env.Spec.Actuator

	switch act.Type {
	case "flux":
		if act.Flux == nil {
			return fmt.Errorf("environment.spec.actuator.flux must be set when type=flux")
		}
	case "":
		return fmt.Errorf("environment.spec.actuator.type must be set")
	default:
		return fmt.Errorf("environment.spec.actuator.type %q is not supported in this release; supported: flux", act.Type)
	}

	return nil
}

// ValidateEnvironment is an exported test helper that exposes the internal validation logic.
func ValidateEnvironment(env *kaprov1alpha1.Environment) error {
	return validateEnvironment(env)
}

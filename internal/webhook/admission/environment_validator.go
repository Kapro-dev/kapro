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
//  1. actuator.type must match the populated actuator sub-spec.
//  2. At most one provider sub-spec may be set.
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
	case "argocd", "sveltos", "ocm", "kserve":
		// sub-spec validation deferred to plugin registration
	case "":
		return fmt.Errorf("environment.spec.actuator.type must be set")
	}

	// At most one provider sub-spec may be populated.
	if env.Spec.Provider != nil {
		populated := 0
		prov := env.Spec.Provider
		if prov.CAPI != nil {
			populated++
		}
		if prov.OCM != nil {
			populated++
		}
		if prov.Rancher != nil {
			populated++
		}
		if populated > 1 {
			return fmt.Errorf("environment.spec.provider: at most one provider sub-spec may be set (got %d)", populated)
		}
	}

	return nil
}

// ValidateEnvironment is an exported test helper that exposes the internal validation logic.
func ValidateEnvironment(env *kaprov1alpha1.Environment) error {
return validateEnvironment(env)
}

package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// MemberClusterValidator validates MemberCluster objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. actuator.type must be "flux" (MVP) and actuator.flux sub-spec must be populated.
type MemberClusterValidator struct {
	decoder admission.Decoder
}

// NewMemberClusterValidator returns a configured MemberClusterValidator.
func NewMemberClusterValidator(decoder admission.Decoder) *MemberClusterValidator {
	return &MemberClusterValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *MemberClusterValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var mc kaprov1alpha1.MemberCluster
	if err := v.decoder.DecodeRaw(req.Object, &mc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateMemberCluster(&mc); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validateMemberCluster(mc *kaprov1alpha1.MemberCluster) error {
	return validateActuator(mc)
}

func validateActuator(mc *kaprov1alpha1.MemberCluster) error {
	act := mc.Spec.Actuator
	if act.Mode == "" {
		return fmt.Errorf("membercluster.spec.actuator.mode must be set")
	}
	if act.Backend == "" {
		return fmt.Errorf("membercluster.spec.actuator.backend must be set")
	}
	switch act.Backend {
	case "flux":
		if act.Mode == "pull" {
			if act.Pull == nil {
				return fmt.Errorf("membercluster.spec.actuator.pull must be set when mode=pull and backend=flux")
			}
			if act.Pull.OCIRepository == "" && len(act.Pull.OCIRepositories) == 0 {
				return fmt.Errorf("membercluster.spec.actuator.pull.ociRepository or ociRepositories must be set when mode=pull and backend=flux")
			}
		}
	default:
		return fmt.Errorf("membercluster.spec.actuator.backend %q is not supported in this release; supported: flux", act.Backend)
	}
	return nil
}

// ValidateMemberCluster is an exported test helper that exposes the internal validation logic.
func ValidateMemberCluster(mc *kaprov1alpha1.MemberCluster) error {
	return validateMemberCluster(mc)
}

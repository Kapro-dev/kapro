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
//  1. delivery.mode and delivery.backendRef must be set.
//  2. built-in Flux profiles must include the backend-specific parameter needed
//     by the selected mode.
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
	act := mc.Spec.Delivery
	if act.Mode == "" {
		return fmt.Errorf("membercluster.spec.delivery.mode must be set")
	}
	if act.BackendRef == "" {
		return fmt.Errorf("membercluster.spec.delivery.backendRef must be set")
	}
	switch act.BackendRef {
	case "flux":
		if act.Mode == kaprov1alpha1.DeliveryModePull && act.Param("ociRepository", "") == "" {
			return fmt.Errorf("membercluster.spec.delivery.parameters.ociRepository must be set when mode=pull and backendRef=flux")
		}
		if act.Mode == kaprov1alpha1.DeliveryModePush && act.Param("resourceSet", "") == "" {
			return fmt.Errorf("membercluster.spec.delivery.parameters.resourceSet must be set when mode=push and backendRef=flux")
		}
	case "argo":
		return nil
	default:
		return nil
	}
	return nil
}

// ValidateMemberCluster is an exported test helper that exposes the internal validation logic.
func ValidateMemberCluster(mc *kaprov1alpha1.MemberCluster) error {
	return validateMemberCluster(mc)
}

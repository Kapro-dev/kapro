package admission

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// FleetClusterValidator validates FleetCluster objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. delivery.mode and delivery.backendRef must be set.
//  2. built-in Flux profiles must include the backend-specific parameter needed
//     by the selected mode.
type FleetClusterValidator struct {
	decoder admission.Decoder
}

// NewFleetClusterValidator returns a configured FleetClusterValidator.
func NewFleetClusterValidator(decoder admission.Decoder) *FleetClusterValidator {
	return &FleetClusterValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *FleetClusterValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var mc kaprov1alpha1.FleetCluster
	if err := v.decoder.DecodeRaw(req.Object, &mc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateFleetCluster(&mc); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("")
}

func validateFleetCluster(mc *kaprov1alpha1.FleetCluster) error {
	return validateActuator(mc)
}

func validateActuator(mc *kaprov1alpha1.FleetCluster) error {
	act := mc.Spec.Delivery
	if act.Mode == "" {
		return fmt.Errorf("fleetcluster.spec.delivery.mode must be set")
	}
	if act.BackendRef == "" {
		return fmt.Errorf("fleetcluster.spec.delivery.backendRef must be set")
	}
	switch act.BackendRef {
	case "flux":
		if act.Mode == kaprov1alpha1.DeliveryModePull && act.Param("ociRepository", "") == "" {
			return fmt.Errorf("fleetcluster.spec.delivery.parameters.ociRepository must be set when mode=pull and backendRef=flux")
		}
		if act.Mode == kaprov1alpha1.DeliveryModePush && act.Param("resourceSet", "") == "" {
			return fmt.Errorf("fleetcluster.spec.delivery.parameters.resourceSet must be set when mode=push and backendRef=flux")
		}
	case "argo":
		return nil
	default:
		return nil
	}
	return nil
}

// ValidateFleetCluster is an exported test helper that exposes the internal validation logic.
func ValidateFleetCluster(mc *kaprov1alpha1.FleetCluster) error {
	return validateFleetCluster(mc)
}

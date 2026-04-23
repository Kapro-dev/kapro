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
//  2. provider, if set, must not specify conflicting sub-types (reserved for future use).
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
	if err := validateActuator(mc); err != nil {
		return err
	}
	return validateProvider(mc)
}

func validateActuator(mc *kaprov1alpha1.MemberCluster) error {
	act := mc.Spec.Actuator
	switch act.Type {
	case "flux":
		if act.Flux == nil {
			return fmt.Errorf("membercluster.spec.actuator.flux must be set when type=flux")
		}
	case "":
		return fmt.Errorf("membercluster.spec.actuator.type must be set")
	default:
		return fmt.Errorf("membercluster.spec.actuator.type %q is not supported in this release; supported: flux", act.Type)
	}
	return nil
}

func validateProvider(mc *kaprov1alpha1.MemberCluster) error {
	p := mc.Spec.Provider
	if p == nil {
		// Provider block is optional; absence means CRD path (Path A).
		return nil
	}

	switch p.Type {
	case "", "crd":
		// Path A: no further validation needed.
	case "gke":
		gke := p.GKE
		if gke == nil {
			return fmt.Errorf("membercluster.spec.provider.gke must be set when type=gke")
		}
		if gke.Project == "" {
			return fmt.Errorf("membercluster.spec.provider.gke.project must be set when type=gke")
		}
		if gke.Location == "" {
			return fmt.Errorf("membercluster.spec.provider.gke.location must be set when type=gke")
		}
		if gke.ClusterName == "" {
			return fmt.Errorf("membercluster.spec.provider.gke.clusterName must be set when type=gke")
		}
	default:
		return fmt.Errorf("membercluster.spec.provider.type %q is not supported in this release; supported: \"\", crd, gke", p.Type)
	}
	return nil
}

// ValidateMemberCluster is an exported test helper that exposes the internal validation logic.
func ValidateMemberCluster(mc *kaprov1alpha1.MemberCluster) error {
	return validateMemberCluster(mc)
}

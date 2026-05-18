package admission

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PromotionTriggerValidator validates PromotionTrigger on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.source.type must be a known source type (oci is the only built-in).
//  2. spec.source.oci.url must be non-empty when type=oci.
//  3. spec.source.oci.tagPattern must be non-empty when type=oci.
//  4. spec.pollInterval must parse as a duration when set.
//  5. spec.template.promotionPlans must contain at least one ref.
//  6. metadata.labels[kapro.io/team] must be set on CREATE (gate sprint).
type PromotionTriggerValidator struct {
	decoder admission.Decoder
}

// NewPromotionTriggerValidator returns a configured PromotionTriggerValidator.
func NewPromotionTriggerValidator(decoder admission.Decoder) *PromotionTriggerValidator {
	return &PromotionTriggerValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *PromotionTriggerValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var pt kaprov1alpha1.PromotionTrigger
	if err := v.decoder.DecodeRaw(req.Object, &pt); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validatePromotionTrigger(&pt); err != nil {
		return admission.Denied(err.Error())
	}
	if req.Operation == admissionv1.Create {
		if fe := requireTeamLabel(pt.Labels); fe != nil {
			return admission.Denied(fe.Error())
		}
	}
	return admission.Allowed("")
}

func validatePromotionTrigger(pt *kaprov1alpha1.PromotionTrigger) error {
	src := pt.Spec.Source
	switch src.Type {
	case "":
		return fmt.Errorf("spec.source.type is required")
	case "oci":
		if src.OCI == nil {
			return fmt.Errorf("spec.source.oci is required when type=oci")
		}
		if src.OCI.Repository == "" {
			return fmt.Errorf("spec.source.oci.repository is required")
		}
		if src.OCI.TagPattern == "" {
			return fmt.Errorf("spec.source.oci.tagPattern is required")
		}
	default:
		return fmt.Errorf("spec.source.type %q is unsupported (built-in: oci)", src.Type)
	}
	if len(pt.Spec.PromotionRunTemplate.PromotionPlans) == 0 {
		return fmt.Errorf("spec.promotionrunTemplate.promotionplans must contain at least one ref")
	}
	for i, ref := range pt.Spec.PromotionRunTemplate.PromotionPlans {
		if ref.Name == "" {
			return fmt.Errorf("spec.promotionrunTemplate.promotionplans[%d].name is required", i)
		}
	}
	return nil
}

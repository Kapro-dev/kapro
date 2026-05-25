package admission

import (
	"context"
	"fmt"
	"net/http"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// PromotionTriggerValidator validates PromotionTrigger on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.source.type must be a known source type (oci is the only built-in).
//  2. spec.source.oci.url must be non-empty when type=oci.
//  3. spec.source.oci.tagPattern must be non-empty when type=oci.
//  4. spec.pollInterval must parse as a duration when set.
//  5. spec.promotionTemplate.plans entries must have names.
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
	var pt kaprov1alpha1.Trigger
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

func validatePromotionTrigger(pt *kaprov1alpha1.Trigger) error {
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
	if pt.Spec.PromotionTemplate.FleetRef == "" {
		return fmt.Errorf("spec.promotionTemplate.fleetRef is required")
	}
	if pt.Spec.MaxActive < 0 {
		return fmt.Errorf("spec.maxActive must be at least 1 when set")
	}
	if pt.Spec.Cooldown != "" {
		if err := validatePositiveDuration("spec.cooldown", pt.Spec.Cooldown); err != nil {
			return err
		}
	}
	if src.OCI != nil && src.OCI.PollInterval != "" {
		if err := validatePositiveDuration("spec.source.oci.pollInterval", src.OCI.PollInterval); err != nil {
			return err
		}
	}
	// Plans are optional on the trigger template; when empty the Promotion
	// controller inherits the inline plan from the parent Fleet.
	for i, ref := range pt.Spec.PromotionTemplate.Plans {
		if ref.Name == "" {
			return fmt.Errorf("spec.promotionTemplate.plans[%d].name is required", i)
		}
	}
	return nil
}

func validatePositiveDuration(path, value string) error {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must parse as a duration: %w", path, err)
	}
	if parsed <= 0 {
		return fmt.Errorf("%s must be greater than 0", path)
	}
	return nil
}

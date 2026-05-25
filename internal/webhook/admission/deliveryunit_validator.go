package admission

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// DeliveryUnitValidator validates DeliveryUnit objects on CREATE and UPDATE.
type DeliveryUnitValidator struct {
	decoder admission.Decoder
	Reader  client.Reader
}

// NewDeliveryUnitValidator returns a configured DeliveryUnitValidator. The
// reader is optional; pass mgr.GetAPIReader() in production to reject Source
// name conflicts with pre-existing user-authored Sources.
func NewDeliveryUnitValidator(decoder admission.Decoder, reader client.Reader) *DeliveryUnitValidator {
	return &DeliveryUnitValidator{decoder: decoder, Reader: reader}
}

// Handle implements admission.Handler.
func (v *DeliveryUnitValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var du kaprov1alpha1.DeliveryUnit
	if err := v.decoder.DecodeRaw(req.Object, &du); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validateDeliveryUnit(&du); err != nil {
		return admission.Denied(err.Error())
	}
	if v.Reader != nil {
		if err := validateDeliveryUnitSourceConflict(ctx, v.Reader, &du); err != nil {
			return admission.Denied(err.Error())
		}
	}
	return admission.Allowed("")
}

func validateDeliveryUnit(du *kaprov1alpha1.DeliveryUnit) error {
	if du.Name == "" {
		return fmt.Errorf("deliveryunit.metadata.name must be non-empty")
	}
	if len(du.Spec.Source.Units) == 0 {
		return fmt.Errorf("deliveryunit.spec.source.units must include at least one unit")
	}
	seenUnits := map[string]int{}
	for i, unit := range du.Spec.Source.Units {
		if strings.TrimSpace(unit.Name) == "" {
			return fmt.Errorf("deliveryunit.spec.source.units[%d].name must be non-empty", i)
		}
		if first, ok := seenUnits[unit.Name]; ok {
			return fmt.Errorf("deliveryunit.spec.source.units[%d].name duplicates spec.source.units[%d].name %q", i, first, unit.Name)
		}
		seenUnits[unit.Name] = i
	}

	seenTriggers := map[string]int{}
	for i, trigger := range du.Spec.Triggers {
		suffix := strings.TrimSpace(trigger.Name)
		if suffix == "" {
			suffix = "default"
		}
		if errs := validation.IsDNS1123Label(suffix); len(errs) > 0 {
			return fmt.Errorf("deliveryunit.spec.triggers[%d].name must be a DNS-1123 label: %s", i, strings.Join(errs, "; "))
		}
		derivedName := du.Name + "-" + suffix
		if errs := validation.IsDNS1123Subdomain(derivedName); len(errs) > 0 {
			return fmt.Errorf("deliveryunit.spec.triggers[%d].name derives invalid Trigger name %q: %s", i, derivedName, strings.Join(errs, "; "))
		}
		if first, ok := seenTriggers[derivedName]; ok {
			return fmt.Errorf("deliveryunit.spec.triggers[%d].name derives duplicate Trigger %q also declared by spec.triggers[%d]", i, derivedName, first)
		}
		seenTriggers[derivedName] = i

		fleetRef := firstNonEmpty(trigger.FleetRef, du.Spec.DefaultFleetRef)
		if fleetRef == "" {
			return fmt.Errorf("deliveryunit.spec.triggers[%d].fleetRef requires fleetRef or spec.defaultFleetRef", i)
		}
		if err := validatePromotionTrigger(&kaprov1alpha1.Trigger{
			Spec: kaprov1alpha1.TriggerSpec{
				Source: trigger.Source,
				PromotionTemplate: kaprov1alpha1.TriggerTemplate{
					FleetRef: fleetRef,
				},
			},
		}); err != nil {
			return fmt.Errorf("deliveryunit.spec.triggers[%d]: %w", i, err)
		}
	}
	return nil
}

func validateDeliveryUnitSourceConflict(ctx context.Context, reader client.Reader, du *kaprov1alpha1.DeliveryUnit) error {
	var source kaprov1alpha1.Source
	if err := reader.Get(ctx, client.ObjectKey{Name: du.Name}, &source); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deliveryunit source conflict lookup failed: %w", err)
	}
	for _, ref := range source.OwnerReferences {
		if ref.APIVersion == kaprov1alpha1.GroupVersion.String() && ref.Kind == "DeliveryUnit" && ref.Name == du.Name && du.UID != "" && ref.UID == du.UID {
			return nil
		}
	}
	return fmt.Errorf("deliveryunit.metadata.name %q conflicts with an existing Source that is not owned by this DeliveryUnit", du.Name)
}

// ValidateDeliveryUnit is an exported test helper that exposes the internal validation logic.
func ValidateDeliveryUnit(du *kaprov1alpha1.DeliveryUnit) error {
	return validateDeliveryUnit(du)
}

// ValidateDeliveryUnitSourceConflict is an exported test helper for the Source
// conflict reference check.
func ValidateDeliveryUnitSourceConflict(ctx context.Context, reader client.Reader, du *kaprov1alpha1.DeliveryUnit) error {
	return validateDeliveryUnitSourceConflict(ctx, reader, du)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

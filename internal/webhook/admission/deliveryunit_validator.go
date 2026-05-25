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
// reader is optional; pass mgr.GetAPIReader() in production to reject name
// conflicts with pre-existing user-authored derived objects.
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
		if err := validateDeliveryUnitDerivedObjectConflicts(ctx, v.Reader, &du); err != nil {
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
	if len(du.Spec.Triggers) > 0 {
		if fe := requireTeamLabel(du.Labels); fe != nil {
			return fmt.Errorf("deliveryunit with spec.triggers requires %s: %w", LabelKaproTeam, fe)
		}
	}
	seenUnits := map[string]int{}
	for i, unit := range du.Spec.Source.Units {
		name := strings.TrimSpace(unit.Name)
		if name == "" {
			return fmt.Errorf("deliveryunit.spec.source.units[%d].name must be non-empty", i)
		}
		if name != unit.Name {
			return fmt.Errorf("deliveryunit.spec.source.units[%d].name must not have leading or trailing whitespace", i)
		}
		if first, ok := seenUnits[name]; ok {
			return fmt.Errorf("deliveryunit.spec.source.units[%d].name duplicates spec.source.units[%d].name %q", i, first, name)
		}
		seenUnits[name] = i
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
				Source:     trigger.Source,
				Cooldown:   trigger.Cooldown,
				MaxActive:  trigger.MaxActive,
				DryRun:     trigger.DryRun,
				Parameters: copyStringMap(trigger.Parameters),
				PromotionTemplate: kaprov1alpha1.TriggerTemplate{
					DeliveryUnitRef: du.Name,
					FleetRef:        fleetRef,
					PlanRef:         firstNonEmpty(trigger.PlanRef, du.Spec.DefaultPlanRef),
					Suspended:       trigger.Suspended,
					Labels:          copyStringMap(trigger.Labels),
					Annotations:     copyStringMap(trigger.Annotations),
				},
			},
		}); err != nil {
			return fmt.Errorf("deliveryunit.spec.triggers[%d]: %w", i, err)
		}
	}
	return nil
}

func validateDeliveryUnitDerivedObjectConflicts(ctx context.Context, reader client.Reader, du *kaprov1alpha1.DeliveryUnit) error {
	var source kaprov1alpha1.Source
	if err := reader.Get(ctx, client.ObjectKey{Name: du.Name}, &source); err != nil {
		if apierrors.IsNotFound(err) {
			source = kaprov1alpha1.Source{}
		} else {
			return fmt.Errorf("deliveryunit source conflict lookup failed: %w", err)
		}
	} else if !isOwnedByDeliveryUnit(&source, du) {
		return fmt.Errorf("deliveryunit.metadata.name %q conflicts with an existing Source that is not owned by this DeliveryUnit", du.Name)
	}

	for _, trigger := range du.Spec.Triggers {
		suffix := strings.TrimSpace(trigger.Name)
		if suffix == "" {
			suffix = "default"
		}
		name := du.Name + "-" + suffix
		var existing kaprov1alpha1.Trigger
		if err := reader.Get(ctx, client.ObjectKey{Name: name}, &existing); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("deliveryunit trigger conflict lookup failed for %q: %w", name, err)
		}
		if !isOwnedByDeliveryUnit(&existing, du) {
			return fmt.Errorf("deliveryunit trigger %q conflicts with an existing Trigger that is not owned by this DeliveryUnit", name)
		}
	}
	return nil
}

func isOwnedByDeliveryUnit(obj client.Object, du *kaprov1alpha1.DeliveryUnit) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.APIVersion == kaprov1alpha1.GroupVersion.String() && ref.Kind == "DeliveryUnit" && ref.Name == du.Name && du.UID != "" && ref.UID == du.UID {
			return true
		}
	}
	return false
}

// ValidateDeliveryUnit is an exported test helper that exposes the internal validation logic.
func ValidateDeliveryUnit(du *kaprov1alpha1.DeliveryUnit) error {
	return validateDeliveryUnit(du)
}

// ValidateDeliveryUnitSourceConflict is an exported test helper for derived
// object conflict checks. The name is kept stable for existing tests.
func ValidateDeliveryUnitSourceConflict(ctx context.Context, reader client.Reader, du *kaprov1alpha1.DeliveryUnit) error {
	return validateDeliveryUnitDerivedObjectConflicts(ctx, reader, du)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

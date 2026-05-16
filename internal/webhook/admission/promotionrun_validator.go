package admission

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// PromotionRunValidator validates PromotionRun objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.version or spec.versions must be non-empty.
//  2. spec.promotionplans must have at least one promotionplan reference.
//  3. Each PromotionPlanRef must have a non-empty name and promotionplan.
type PromotionRunValidator struct {
	decoder admission.Decoder
}

// NewPromotionRunValidator returns a configured PromotionRunValidator.
func NewPromotionRunValidator(decoder admission.Decoder) *PromotionRunValidator {
	return &PromotionRunValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *PromotionRunValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	var promotionrun kaprov1alpha1.PromotionRun
	if err := v.decoder.DecodeRaw(req.Object, &promotionrun); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validatePromotionRun(&promotionrun); err != nil {
		return admission.Denied(err.Error())
	}
	if req.Operation == admissionv1.Update {
		var old kaprov1alpha1.PromotionRun
		if err := v.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if err := validatePromotionRunUpdate(&old, &promotionrun); err != nil {
			return admission.Denied(err.Error())
		}
	}
	return admission.Allowed("")
}

func validatePromotionRun(r *kaprov1alpha1.PromotionRun) error {
	var allErrs field.ErrorList
	if r.Spec.Version == "" && len(r.Spec.Versions) == 0 {
		allErrs = append(allErrs, field.Required(field.NewPath("spec"), "version or versions is required"))
	}
	for unit, version := range r.Spec.Versions {
		if unit == "" {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "versions"), unit, "unit key must be non-empty"))
		}
		if version == "" {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "versions").Key(unit), version, "version must be non-empty"))
		}
	}
	if len(allErrs) > 0 {
		return fmt.Errorf("%s", allErrs.ToAggregate().Error())
	}

	if len(r.Spec.PromotionPlans) == 0 {
		return fmt.Errorf("promotionrun.spec.promotionplans must have at least one promotionplan reference")
	}

	index := make(map[string]int, len(r.Spec.PromotionPlans))
	for i, ref := range r.Spec.PromotionPlans {
		if ref.Name == "" {
			return fmt.Errorf("promotionrun.spec.promotionplans[%d].name must be set", i)
		}
		if ref.PromotionPlan == "" {
			return fmt.Errorf("promotionrun.spec.promotionplans[%d].promotionplan must be set", i)
		}
		if _, exists := index[ref.Name]; exists {
			return fmt.Errorf("promotionrun.spec.promotionplans: duplicate promotionplan node name %q", ref.Name)
		}
		index[ref.Name] = i
	}

	// Validate all dependsOn references name existing promotionplan nodes.
	for _, ref := range r.Spec.PromotionPlans {
		for _, dep := range ref.DependsOn {
			if _, exists := index[dep]; !exists {
				return fmt.Errorf("promotionrun.spec.promotionplans[%q].dependsOn: unknown promotionplan node %q", ref.Name, dep)
			}
		}
	}

	// DFS cycle detection on the promotionplan node DAG.
	if cycle := detectCycle(index, func(name string) []string {
		return r.Spec.PromotionPlans[index[name]].DependsOn
	}); cycle != "" {
		return fmt.Errorf("promotionrun.spec.promotionplans: cycle detected: %s", cycle)
	}

	return nil
}

func validatePromotionRunUpdate(old, new *kaprov1alpha1.PromotionRun) error {
	if old.Spec.Version != new.Spec.Version {
		return fmt.Errorf("promotionrun.spec.version is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.Versions, new.Spec.Versions) {
		return fmt.Errorf("promotionrun.spec.versions is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.PromotionPlans, new.Spec.PromotionPlans) {
		return fmt.Errorf("promotionrun.spec.promotionplans is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.Scope, new.Spec.Scope) {
		return fmt.Errorf("promotionrun.spec.scope is immutable after creation")
	}
	return nil
}

// ValidatePromotionRun is an exported test helper that exposes the internal validation logic.
func ValidatePromotionRun(r *kaprov1alpha1.PromotionRun) error {
	return validatePromotionRun(r)
}

// ValidatePromotionRunUpdate is an exported test helper for update immutability rules.
func ValidatePromotionRunUpdate(old, new *kaprov1alpha1.PromotionRun) error {
	return validatePromotionRunUpdate(old, new)
}

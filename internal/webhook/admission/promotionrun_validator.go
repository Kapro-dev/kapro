package admission

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// PromotionRun create/update is restricted to the Kapro controller service
// account. Normal users should author a Promotion and let the controller
// stamp a PromotionRun; direct PromotionRun writes are a break-glass path.
//
// AllowedPromotionRunWriters is the default set of usernames allowed to
// write PromotionRun objects. Override via KAPRO_PROMOTIONRUN_WRITERS
// (comma-separated). The system:masters group is always allowed as a
// break-glass override.
var AllowedPromotionRunWriters = []string{
	"system:serviceaccount:kapro-system:kapro-operator",
}

// breakGlassGroup is allowed regardless of username.
const breakGlassGroup = "system:masters"

func allowedWriters() []string {
	if env := os.Getenv("KAPRO_PROMOTIONRUN_WRITERS"); env != "" {
		out := strings.Split(env, ",")
		for i := range out {
			out[i] = strings.TrimSpace(out[i])
		}
		return out
	}
	return AllowedPromotionRunWriters
}

// isAllowedPromotionRunWriter reports whether the given user identity is
// permitted to create or update PromotionRun objects.
func isAllowedPromotionRunWriter(user authenticationv1.UserInfo) bool {
	for _, g := range user.Groups {
		if g == breakGlassGroup {
			return true
		}
	}
	for _, allowed := range allowedWriters() {
		if user.Username == allowed {
			return true
		}
	}
	return false
}

// LabelKaproTeam is the ownership label every promotion-affecting CR must
// carry. Enforced at CREATE by the admission validators per
// docs/rbac-tenancy.md. Use a constant so adding the same check to a new
// CRD validator is a one-line change.
const LabelKaproTeam = "kapro.io/team"

// requireTeamLabel returns a field.Error when the supplied labels map does
// not contain a non-empty kapro.io/team value. Use on CREATE only — UPDATE
// flows do not enforce the label change so existing objects keep working
// when a tenancy policy is introduced mid-life.
func requireTeamLabel(labels map[string]string) *field.Error {
	if labels == nil || labels[LabelKaproTeam] == "" {
		return field.Required(
			field.NewPath("metadata", "labels").Key(LabelKaproTeam),
			"is required (multi-tenancy ownership label; see docs/rbac-tenancy.md)",
		)
	}
	return nil
}

// PromotionRunValidator validates PromotionRun objects on CREATE and UPDATE.
//
// Rules enforced:
//  1. spec.version or spec.versions must be non-empty.
//  2. spec.plans must have at least one promotionPlan reference.
//  3. Each PromotionPlanRef must have a non-empty name and promotionPlan.
//  4. metadata.labels[kapro.io/team] must be set on CREATE (gate sprint).
type PromotionRunValidator struct {
	decoder admission.Decoder
}

// NewPromotionRunValidator returns a configured PromotionRunValidator.
func NewPromotionRunValidator(decoder admission.Decoder) *PromotionRunValidator {
	return &PromotionRunValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *PromotionRunValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	// Gate writes to the controller. Users author Promotion; the controller
	// stamps PromotionRun. Break-glass via system:masters group.
	if req.Operation == admissionv1.Create || req.Operation == admissionv1.Update {
		if !isAllowedPromotionRunWriter(req.UserInfo) {
			return admission.Denied(fmt.Sprintf(
				"PromotionRun is controller-managed; create or update a Promotion instead "+
					"(requester %q is not in the allowed writer list)",
				req.UserInfo.Username,
			))
		}
	}

	var promotionRun kaprov1alpha2.PromotionRun
	if err := v.decoder.DecodeRaw(req.Object, &promotionRun); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := validatePromotionRun(&promotionRun); err != nil {
		return admission.Denied(err.Error())
	}
	if req.Operation == admissionv1.Create {
		if fe := requireTeamLabel(promotionRun.Labels); fe != nil {
			return admission.Denied(fe.Error())
		}
	}
	if req.Operation == admissionv1.Update {
		var old kaprov1alpha2.PromotionRun
		if err := v.decoder.DecodeRaw(req.OldObject, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if err := validatePromotionRunUpdate(&old, &promotionRun); err != nil {
			return admission.Denied(err.Error())
		}
	}
	return admission.Allowed("")
}

func validatePromotionRun(r *kaprov1alpha2.PromotionRun) error {
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
		return fmt.Errorf("promotionRun.spec.plans must have at least one promotionPlan reference")
	}

	index := make(map[string]int, len(r.Spec.PromotionPlans))
	for i, ref := range r.Spec.PromotionPlans {
		if ref.Name == "" {
			return fmt.Errorf("promotionRun.spec.plans[%d].name must be set", i)
		}
		if ref.Plan == "" {
			return fmt.Errorf("promotionRun.spec.plans[%d].promotionPlan must be set", i)
		}
		if _, exists := index[ref.Name]; exists {
			return fmt.Errorf("promotionRun.spec.plans: duplicate promotionPlan node name %q", ref.Name)
		}
		index[ref.Name] = i
	}

	// Validate all dependsOn references name existing promotionPlan nodes.
	for _, ref := range r.Spec.PromotionPlans {
		for _, dep := range ref.DependsOn {
			if _, exists := index[dep]; !exists {
				return fmt.Errorf("promotionRun.spec.plans[%q].dependsOn: unknown promotionPlan node %q", ref.Name, dep)
			}
		}
	}

	// DFS cycle detection on the promotionPlan node DAG.
	if cycle := detectCycle(index, func(name string) []string {
		return r.Spec.PromotionPlans[index[name]].DependsOn
	}); cycle != "" {
		return fmt.Errorf("promotionRun.spec.plans: cycle detected: %s", cycle)
	}

	return nil
}

func validatePromotionRunUpdate(old, new *kaprov1alpha2.PromotionRun) error {
	if old.Spec.Version != new.Spec.Version {
		return fmt.Errorf("promotionRun.spec.version is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.Versions, new.Spec.Versions) {
		return fmt.Errorf("promotionRun.spec.versions is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.PromotionPlans, new.Spec.PromotionPlans) {
		return fmt.Errorf("promotionRun.spec.plans is immutable after creation")
	}
	if !reflect.DeepEqual(old.Spec.Scope, new.Spec.Scope) {
		return fmt.Errorf("promotionRun.spec.scope is immutable after creation")
	}
	return nil
}

// ValidatePromotionRun is an exported test helper that exposes the internal validation logic.
func ValidatePromotionRun(r *kaprov1alpha2.PromotionRun) error {
	return validatePromotionRun(r)
}

// ValidatePromotionRunUpdate is an exported test helper for update immutability rules.
func ValidatePromotionRunUpdate(old, new *kaprov1alpha2.PromotionRun) error {
	return validatePromotionRunUpdate(old, new)
}

package admission

import (
	"testing"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func validTrigger() *kaprov1alpha2.Trigger {
	return &kaprov1alpha2.Trigger{
		Spec: kaprov1alpha2.TriggerSpec{
			Source: kaprov1alpha2.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha2.OCITriggerSource{
					Repository: "oci://example.com/repo",
					TagPattern: "v.*",
				},
			},
			PromotionTemplate: kaprov1alpha2.TriggerTemplate{
				FleetRef:       "checkout",
				PromotionPlans: []kaprov1alpha2.PlanRef{{Name: "default-plan"}},
			},
		},
	}
}

func TestValidatePromotionTrigger_Happy(t *testing.T) {
	if err := validatePromotionTrigger(validTrigger()); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestValidatePromotionTrigger_MissingSourceType(t *testing.T) {
	pt := validTrigger()
	pt.Spec.Source.Type = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing source type")
	}
}

func TestValidatePromotionTrigger_UnknownSourceType(t *testing.T) {
	pt := validTrigger()
	pt.Spec.Source.Type = "git"
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for unsupported source type")
	}
}

func TestValidatePromotionTrigger_OCIMissingRepository(t *testing.T) {
	pt := validTrigger()
	pt.Spec.Source.OCI.Repository = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing oci.repository")
	}
}

func TestValidatePromotionTrigger_OCIMissingTagPattern(t *testing.T) {
	pt := validTrigger()
	pt.Spec.Source.OCI.TagPattern = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing oci.tagPattern")
	}
}

func TestValidatePromotionTrigger_OCIBlockMissing(t *testing.T) {
	pt := validTrigger()
	pt.Spec.Source.OCI = nil
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing oci block")
	}
}

func TestValidatePromotionTrigger_TemplateMissingPlansIsValid(t *testing.T) {
	// PromotionPlans is optional in the trigger template; the Promotion
	// controller inherits the inline plan from the parent Kapro when empty.
	pt := validTrigger()
	pt.Spec.PromotionTemplate.PromotionPlans = nil
	if err := validatePromotionTrigger(pt); err != nil {
		t.Fatalf("expected nil error for empty promotionPlans (controller falls back to Kapro inline plan), got %v", err)
	}
}

func TestValidatePromotionTrigger_TemplateMissingFleetRef(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.FleetRef = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing fleetRef")
	}
}

func TestValidatePromotionTrigger_PromotionPlanMissingName(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.PromotionPlans = []kaprov1alpha2.PlanRef{{Name: ""}}
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for empty promotionPlan name")
	}
}

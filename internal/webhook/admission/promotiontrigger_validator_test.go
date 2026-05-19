package admission

import (
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func validTrigger() *kaprov1alpha1.PromotionTrigger {
	return &kaprov1alpha1.PromotionTrigger{
		Spec: kaprov1alpha1.PromotionTriggerSpec{
			Source: kaprov1alpha1.PromotionTriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIPromotionTriggerSource{
					Repository: "oci://example.com/repo",
					TagPattern: "v.*",
				},
			},
			PromotionTemplate: kaprov1alpha1.PromotionTriggerTemplate{
				KaproRef:       "checkout",
				PromotionPlans: []kaprov1alpha1.PromotionPlanRef{{Name: "default-plan"}},
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

func TestValidatePromotionTrigger_TemplateMissingKaproRef(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.KaproRef = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing kaproRef")
	}
}

func TestValidatePromotionTrigger_PromotionPlanMissingName(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.PromotionPlans = []kaprov1alpha1.PromotionPlanRef{{Name: ""}}
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for empty promotionPlan name")
	}
}

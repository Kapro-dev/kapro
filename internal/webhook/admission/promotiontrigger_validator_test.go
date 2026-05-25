package admission

import (
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func validTrigger() *kaprov1alpha1.Trigger {
	return &kaprov1alpha1.Trigger{
		Spec: kaprov1alpha1.TriggerSpec{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCITriggerSource{
					Repository: "oci://example.com/repo",
					TagPattern: "v.*",
				},
			},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{
				DeliveryUnitRef: "checkout",
				FleetRef:        "checkout",
				Plans:           []kaprov1alpha1.PlanRef{{Name: "default-plan"}},
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
	// Plans is optional in the trigger template; the Promotion
	// controller inherits the inline plan from the parent Fleet when empty.
	pt := validTrigger()
	pt.Spec.PromotionTemplate.Plans = nil
	if err := validatePromotionTrigger(pt); err != nil {
		t.Fatalf("expected nil error for empty plans (controller falls back to Fleet inline plan), got %v", err)
	}
}

func TestValidatePromotionTrigger_TemplateMissingFleetRef(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.FleetRef = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing fleetRef")
	}
}

func TestValidatePromotionTrigger_TemplateMissingDeliveryUnitRef(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.DeliveryUnitRef = ""
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for missing deliveryUnitRef")
	}
}

func TestValidatePromotionTrigger_PromotionPlanMissingName(t *testing.T) {
	pt := validTrigger()
	pt.Spec.PromotionTemplate.Plans = []kaprov1alpha1.PlanRef{{Name: ""}}
	if err := validatePromotionTrigger(pt); err == nil {
		t.Fatal("expected error for empty plan name")
	}
}

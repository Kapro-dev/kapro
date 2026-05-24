package v1alpha1

import (
	"encoding/json"
	"testing"
)

func TestTriggerSuspendedExplicitFalseSerializes(t *testing.T) {
	trigger := Trigger{
		Spec: TriggerSpec{
			Suspended: boolPtr(false),
			Source: TriggerSource{
				Type: "oci",
				OCI: &OCITriggerSource{
					Repository: "oci://registry.example.com/app",
					TagPattern: "^v",
				},
			},
			PromotionTemplate: TriggerTemplate{
				FleetRef:  "checkout",
				Suspended: boolPtr(false),
			},
		},
	}

	data, err := json.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	spec := body["spec"].(map[string]any)
	if got, ok := spec["suspended"].(bool); !ok || got {
		t.Fatalf("spec.suspended = %#v, want explicit false in JSON body %s", spec["suspended"], data)
	}
	template := spec["promotionTemplate"].(map[string]any)
	if got, ok := template["suspended"].(bool); !ok || got {
		t.Fatalf("spec.promotionTemplate.suspended = %#v, want explicit false in JSON body %s", template["suspended"], data)
	}
}

func TestTriggerSuspendedNilOmitsForAPIServerDefaulting(t *testing.T) {
	trigger := Trigger{
		Spec: TriggerSpec{
			Source: TriggerSource{
				Type: "oci",
				OCI: &OCITriggerSource{
					Repository: "oci://registry.example.com/app",
					TagPattern: "^v",
				},
			},
			PromotionTemplate: TriggerTemplate{
				FleetRef: "checkout",
			},
		},
	}

	data, err := json.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	spec := body["spec"].(map[string]any)
	if _, ok := spec["suspended"]; ok {
		t.Fatalf("nil spec.suspended should be omitted so CRD defaulting can apply: %s", data)
	}
	template := spec["promotionTemplate"].(map[string]any)
	if _, ok := template["suspended"]; ok {
		t.Fatalf("nil spec.promotionTemplate.suspended should be omitted so CRD defaulting can apply: %s", data)
	}
}

func TestTriggerSuspendedDeepCopyDoesNotAliasPointers(t *testing.T) {
	original := &Trigger{
		Spec: TriggerSpec{
			Suspended: boolPtr(false),
			PromotionTemplate: TriggerTemplate{
				Suspended: boolPtr(false),
			},
		},
	}

	copied := original.DeepCopy()
	*copied.Spec.Suspended = true
	*copied.Spec.PromotionTemplate.Suspended = true

	if *original.Spec.Suspended {
		t.Fatal("TriggerSpec.Suspended pointer aliased after DeepCopy")
	}
	if *original.Spec.PromotionTemplate.Suspended {
		t.Fatal("TriggerTemplate.Suspended pointer aliased after DeepCopy")
	}
}

func boolPtr(value bool) *bool {
	return &value
}

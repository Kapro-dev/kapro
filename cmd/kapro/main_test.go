package main

import (
	"testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestPromotionIntentLabelsPropagateDeliveryUnitTeam(t *testing.T) {
	labels := promotionIntentLabels("checkout", map[string]string{
		kaprov1alpha1.LabelTeam: "platform",
		"custom":                "kept",
	})

	if labels[kaprov1alpha1.LabelUnit] != "checkout" {
		t.Fatalf("unit label = %q", labels[kaprov1alpha1.LabelUnit])
	}
	if labels[kaprov1alpha1.LabelManagedBy] != kaprov1alpha1.ManagedByKapro {
		t.Fatalf("managed-by label = %q", labels[kaprov1alpha1.LabelManagedBy])
	}
	if labels[kaprov1alpha1.LabelTeam] != "platform" {
		t.Fatalf("team label = %q", labels[kaprov1alpha1.LabelTeam])
	}
	if labels["custom"] != "kept" {
		t.Fatalf("custom label = %q", labels["custom"])
	}
}

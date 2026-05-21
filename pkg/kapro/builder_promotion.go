package kapro

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// PromotionBuilder constructs a kapro.io/v1alpha2 Promotion intent.
type PromotionBuilder struct {
	name, fleetRef, version string
}

// NewPromotion starts a Promotion builder.
func NewPromotion(name string) *PromotionBuilder {
	return &PromotionBuilder{name: name}
}

// ForFleet sets spec.fleetRef.
func (b *PromotionBuilder) ForFleet(fleetRef string) *PromotionBuilder {
	b.fleetRef = fleetRef
	return b
}

// AtVersion sets spec.version.
func (b *PromotionBuilder) AtVersion(version string) *PromotionBuilder {
	b.version = version
	return b
}

// Build returns a new Promotion object.
func (b *PromotionBuilder) Build() *kaprov1alpha2.Promotion {
	return &kaprov1alpha2.Promotion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha2.GroupVersion.String(),
			Kind:       "Promotion",
		},
		ObjectMeta: metav1.ObjectMeta{Name: b.name},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef: b.fleetRef,
			Version:  b.version,
		},
	}
}

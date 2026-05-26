package kapro

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// PromotionBuilder constructs a kapro.io/v1alpha1 Promotion intent.
type PromotionBuilder struct {
	name, fleetRef, version string
}

// NewPromotion starts a Promotion builder.
func NewPromotion(name string) *PromotionBuilder {
	return &PromotionBuilder{name: name}
}

// ForFleet sets spec.fleet.
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
func (b *PromotionBuilder) Build() *kaprov1alpha1.Promotion {
	return &kaprov1alpha1.Promotion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha1.GroupVersion.String(),
			Kind:       "Promotion",
		},
		ObjectMeta: metav1.ObjectMeta{Name: b.name},
		Spec: kaprov1alpha1.PromotionSpec{
			FleetRef: b.fleetRef,
			Version:  b.version,
		},
	}
}

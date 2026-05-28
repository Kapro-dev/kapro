package collector

import "kapro.io/kapro/pkg/pri"

// ReferenceBinding describes Kapro's PRI v0.1 reference binding.
func ReferenceBinding() *pri.Binding {
	return &pri.Binding{
		TypeMeta: pri.TypeMeta{APIVersion: pri.APIVersion, Kind: pri.KindBinding},
		Metadata: pri.Metadata{
			Name: "kapro-reference",
			Labels: map[string]string{
				"app.kubernetes.io/name": "kapro",
			},
		},
		Spec: pri.BindingSpec{
			Category:      "platform",
			Summary:       "Kapro emits PRI v0.1 Promotion, PromotionRun, and Evidence records from Kapro runtime objects.",
			PRIVersions:   []string{"v0.1"},
			AdoptionModes: []string{"emission", "bridge"},
			RoundTrip:     "lossy",
			Mappings: &pri.BindingMappings{
				Objects: []pri.BindingMapping{
					{PRI: "Promotion", External: "kapro.io/Promotion or synthesized from runtime.kapro.io/PromotionRun"},
					{PRI: "PromotionRun", External: "runtime.kapro.io/PromotionRun"},
					{PRI: "TargetResult", External: "runtime.kapro.io/Target.status"},
					{PRI: "Evidence", External: "runtime.kapro.io/PromotionRun.status.auditTrail"},
				},
				Fields: []pri.BindingMapping{
					{PRI: "Promotion.spec.unit", External: "PromotionRun.spec.deliveryUnitRef or kapro.io/unit label"},
					{PRI: "Promotion.spec.artifacts", External: "PromotionRun.spec.version, spec.versions, and status.resolvedVersion"},
					{PRI: "PromotionRun.status.phase", External: "PromotionRun.status.phase", Notes: "Kapro phases are mapped to PRI's portable closed enum; native value is preserved as implementationPhase."},
					{PRI: "PromotionRun.status.targetResults", External: "Target.status.phase"},
				},
			},
			Unsupported: []string{
				"Lossless reconstruction of every Kapro planning field from PRI v0.1 alone",
				"Portable CheckResult emission before PRI defines a shared Kapro gate-result mapping",
			},
			References: []pri.BindingReference{
				{Title: "Kapro", URI: "https://github.com/Kapro-dev/kapro"},
				{Title: "OpenPromotions PRI", URI: "https://github.com/openpromotions/promotion-spec"},
			},
		},
	}
}

// ReferenceConformanceProfile describes this implementation's v0.1 support.
func ReferenceConformanceProfile() *pri.ConformanceProfile {
	return &pri.ConformanceProfile{
		TypeMeta: pri.TypeMeta{APIVersion: pri.APIVersion, Kind: pri.KindConformanceProfile},
		Metadata: pri.Metadata{
			Name: "kapro-reference",
			Labels: map[string]string{
				"app.kubernetes.io/name": "kapro",
			},
		},
		Spec: pri.ConformanceProfileSpec{
			PRIVersion:   "v0.1",
			AdoptionMode: "emission",
			Conformance: pri.ConformanceStatement{
				Document: true,
				Runtime:  true,
				Decision: false,
			},
		},
	}
}

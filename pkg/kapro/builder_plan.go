package kapro

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// PlanBuilder constructs a reusable kapro.io/v1alpha2 Plan.
type PlanBuilder struct {
	name   string
	stages []kaprov1alpha2.Stage
}

// NewPlan starts a Plan builder.
func NewPlan(name string) *PlanBuilder {
	return &PlanBuilder{name: name}
}

// WithStage appends a fully specified stage.
func (b *PlanBuilder) WithStage(stage kaprov1alpha2.Stage) *PlanBuilder {
	b.stages = append(b.stages, *stage.DeepCopy())
	return b
}

// Build returns a new Plan object.
func (b *PlanBuilder) Build() *kaprov1alpha2.Plan {
	stages := make([]kaprov1alpha2.Stage, 0, len(b.stages))
	for _, stage := range b.stages {
		stages = append(stages, *stage.DeepCopy())
	}
	return &kaprov1alpha2.Plan{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha2.GroupVersion.String(),
			Kind:       "Plan",
		},
		ObjectMeta: metav1.ObjectMeta{Name: b.name},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: stages,
		},
	}
}

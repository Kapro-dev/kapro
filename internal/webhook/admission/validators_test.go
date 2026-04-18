package admission_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

// ---- EnvironmentValidator ---------------------------------------------------

func TestValidateEnvironment_MissingActuatorType(t *testing.T) {
	env := &kaprov1alpha1.Environment{
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: ""},
		},
	}
	if err := envValidate(env); err == nil {
		t.Fatal("expected error for missing actuator type")
	}
}

func TestValidateEnvironment_FluxMissingSubSpec(t *testing.T) {
	env := &kaprov1alpha1.Environment{
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux", Flux: nil},
		},
	}
	if err := envValidate(env); err == nil {
		t.Fatal("expected error for flux type without flux sub-spec")
	}
}

func TestValidateEnvironment_FluxValid(t *testing.T) {
	env := &kaprov1alpha1.Environment{
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	if err := envValidate(env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEnvironment_MultipleProviders(t *testing.T) {
	env := &kaprov1alpha1.Environment{
		Spec: kaprov1alpha1.EnvironmentSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux", Flux: &kaprov1alpha1.FluxActuator{}},
			Provider: &kaprov1alpha1.ProviderSpec{
				CAPI: &kaprov1alpha1.CAPIProviderSpec{ClusterName: "a"},
				OCM:  &kaprov1alpha1.OCMProviderSpec{ClusterName: "b"},
			},
		},
	}
	if err := envValidate(env); err == nil {
		t.Fatal("expected error for multiple provider sub-specs")
	}
}

// ---- ReleaseValidator -------------------------------------------------------

func TestValidateRelease_MissingArtifact(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{Artifact: "", PipelineRef: "p"},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for missing artifact")
	}
}

func TestValidateRelease_MissingPipelineRef(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{Artifact: "art-v1", PipelineRef: ""},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for missing pipelineRef")
	}
}

func TestValidateRelease_Valid(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{Artifact: "art-v1", PipelineRef: "standard"},
	}
	if err := releaseValidate(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- PipelineValidator -------------------------------------------------------

func TestValidatePipeline_EmptyBatches(t *testing.T) {
	p := buildPipeline(nil, nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for empty batches")
	}
}

func TestValidatePipeline_UnknownDependency(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1", DependsOn: []string{"does-not-exist"}},
	}, nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for unknown batch dependency")
	}
}

func TestValidatePipeline_BatchCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1", DependsOn: []string{"b2"}},
		{Name: "b2", DependsOn: []string{"b1"}},
	}, nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for cycle in batch DAG")
	}
}

func TestValidatePipeline_SelfCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1", DependsOn: []string{"b1"}},
	}, nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for self-cycle")
	}
}

func TestValidatePipeline_ValidLinearDAG(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1"},
		{Name: "b2", DependsOn: []string{"b1"}},
		{Name: "b3", DependsOn: []string{"b2"}},
	}, nil)
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePipeline_ValidDiamondDAG(t *testing.T) {
	// b1 → b2, b1 → b3, b2+b3 → b4
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1"},
		{Name: "b2", DependsOn: []string{"b1"}},
		{Name: "b3", DependsOn: []string{"b1"}},
		{Name: "b4", DependsOn: []string{"b2", "b3"}},
	}, nil)
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error for diamond DAG: %v", err)
	}
}

func TestValidatePipeline_PromotionStepCycle(t *testing.T) {
	p := buildPipeline(
		[]kaprov1alpha1.Batch{{Name: "b1"}},
		[]kaprov1alpha1.PromotionStep{
			{Name: "dev", DependsOn: []string{"prod"}},
			{Name: "prod", DependsOn: []string{"dev"}},
		},
	)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for cycle in promotion steps")
	}
}

func TestValidatePipeline_DuplicateBatchName(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Batch{
		{Name: "b1"},
		{Name: "b1"},
	}, nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for duplicate batch name")
	}
}

// ---- helpers ----------------------------------------------------------------

// envValidate is a test shim that calls the internal validateEnvironment via exported helper.
// We test the internal logic directly by calling the exported constructor with a nil decoder
// and then the validator function through the package-level exported test helper.
//
// Since validateEnvironment is unexported, we expose it via a test helper.
// In this test file we use the exported validateXxx wrappers added to the admission package.
func envValidate(env *kaprov1alpha1.Environment) error {
	return admission.ValidateEnvironment(env)
}

func releaseValidate(r *kaprov1alpha1.Release) error {
	return admission.ValidateRelease(r)
}

func pipelineValidate(p *kaprov1alpha1.Pipeline) error {
	return admission.ValidatePipeline(p)
}

func buildPipeline(batches []kaprov1alpha1.Batch, steps []kaprov1alpha1.PromotionStep) *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipeline", Namespace: "default"},
		Spec: kaprov1alpha1.PipelineSpec{
			Progression: kaprov1alpha1.PipelineProgression{Batches: batches},
			Promotion:   kaprov1alpha1.PipelinePromotion{Steps: steps},
		},
	}
}

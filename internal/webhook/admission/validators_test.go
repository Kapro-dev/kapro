package admission_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

// ---- MemberClusterValidator ---------------------------------------------------

func TestValidateMemberCluster_MissingActuatorType(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: ""},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for missing actuator type")
	}
}

func TestValidateMemberCluster_FluxMissingSubSpec(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux", Flux: nil},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for flux type without flux sub-spec")
	}
}

func TestValidateMemberCluster_FluxValid(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMemberCluster_UnsupportedActuatorType(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "kserve"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for unsupported actuator type")
	}
}

func TestValidateMemberCluster_GKEMissingSubSpec(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
			Provider: &kaprov1alpha1.ProviderSpec{Type: "gke"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for type=gke without provider.gke sub-spec")
	}
}

func TestValidateMemberCluster_GKEMissingProject(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
			Provider: &kaprov1alpha1.ProviderSpec{
				Type: "gke",
				GKE:  &kaprov1alpha1.GKEProviderSpec{Location: "europe-west3", ClusterName: "spoke-de"},
			},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for type=gke with empty project")
	}
}

func TestValidateMemberCluster_GKEValid(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
			Provider: &kaprov1alpha1.ProviderSpec{
				Type: "gke",
				GKE: &kaprov1alpha1.GKEProviderSpec{
					Project:     "lidl-de-spoke",
					Location:    "europe-west3",
					ClusterName: "spoke-de",
				},
			},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error for valid type=gke: %v", err)
	}
}

func TestValidateMemberCluster_UnsupportedProviderType(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
			Provider: &kaprov1alpha1.ProviderSpec{Type: "eks"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for unsupported provider type eks")
	}
}

func TestValidateMemberCluster_CRDProviderValid(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{Namespace: "flux-system"},
			},
			Provider: &kaprov1alpha1.ProviderSpec{Type: "crd"},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error for type=crd: %v", err)
	}
}

// ---- ReleaseValidator -------------------------------------------------------

func TestValidateRelease_MissingArtifact(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: "pipe-1"},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for missing artifact")
	}
}

func TestValidateRelease_MissingPipelines(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:  "art-v1",
			Pipelines: nil,
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for missing pipelines")
	}
}

func TestValidateRelease_PipelineRefMissingName(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "", Pipeline: "standard-rollout"},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for pipeline ref with empty name")
	}
}

func TestValidateRelease_PipelineRefMissingPipeline(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: ""},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for pipeline ref with empty pipeline")
	}
}

func TestValidateRelease_Valid(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: "standard-rollout"},
			},
		},
	}
	if err := releaseValidate(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRelease_ValidMultiPipelineDAG(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "canary", Pipeline: "canary-rollout"},
				{Name: "stable", Pipeline: "stable-rollout", DependsOn: []string{"canary"}},
			},
		},
	}
	if err := releaseValidate(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRelease_DuplicatePipelineName(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout"},
				{Name: "wave1", Pipeline: "rollout"},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for duplicate pipeline node name")
	}
}

func TestValidateRelease_UnknownDependency(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout", DependsOn: []string{"does-not-exist"}},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for unknown pipeline node dependency")
	}
}

func TestValidateRelease_PipelineCycle(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "a", Pipeline: "rollout", DependsOn: []string{"b"}},
				{Name: "b", Pipeline: "rollout", DependsOn: []string{"a"}},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for cycle in pipeline DAG")
	}
}

func TestValidateRelease_SelfCycle(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout", DependsOn: []string{"wave1"}},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for self-cycle in pipeline DAG")
	}
}

// ---- PipelineValidator -------------------------------------------------------

func TestValidatePipeline_EmptyStages(t *testing.T) {
	p := buildPipeline(nil)
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for empty stages")
	}
}

func TestValidatePipeline_UnknownDependency(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: []string{"does-not-exist"}},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for unknown stage dependency")
	}
}

func TestValidatePipeline_StageCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: []string{"s2"}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: []string{"s1"}},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for cycle in stage DAG")
	}
}

func TestValidatePipeline_SelfCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: []string{"s1"}},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for self-cycle")
	}
}

func TestValidatePipeline_ValidLinearDAG(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}}, DependsOn: []string{"s1"}},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: []string{"s2"}},
	})
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePipeline_ValidDiamondDAG(t *testing.T) {
	// s1 → s2, s1 → s3, s2+s3 → s4
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "eu"}}, DependsOn: []string{"s1"}},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "us"}}, DependsOn: []string{"s1"}},
		{Name: "s4", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "global"}}, DependsOn: []string{"s2", "s3"}},
	})
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error for diamond DAG: %v", err)
	}
}

func TestValidatePipeline_DuplicateStageName(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for duplicate stage name")
	}
}

// ---- helpers ----------------------------------------------------------------

func mcValidate(mc *kaprov1alpha1.MemberCluster) error {
	return admission.ValidateMemberCluster(mc)
}

func releaseValidate(r *kaprov1alpha1.Release) error {
	return admission.ValidateRelease(r)
}

func pipelineValidate(p *kaprov1alpha1.Pipeline) error {
	return admission.ValidatePipeline(p)
}

func buildPipeline(stages []kaprov1alpha1.Stage) *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipeline"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: stages,
		},
	}
}

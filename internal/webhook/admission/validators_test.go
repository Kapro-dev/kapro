package admission_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

// deps converts a list of stage names into StageDependency values for test brevity.
func deps(names ...string) []kaprov1alpha1.StageDependency {
	out := make([]kaprov1alpha1.StageDependency, len(names))
	for i, n := range names {
		out[i] = kaprov1alpha1.StageDependency{Stage: n}
	}
	return out
}

// ---- MemberClusterValidator ---------------------------------------------------

func TestValidateMemberCluster_MissingMode(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Mode: "", Backend: "flux"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for missing actuator mode")
	}
}

func TestValidateMemberCluster_MissingBackend(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Mode: "pull", Backend: ""},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for missing actuator backend")
	}
}

func TestValidateMemberCluster_FluxMissingSubSpec(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Mode: "pull", Backend: "flux", Pull: nil},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for pull/flux without flux sub-spec")
	}
}

func TestValidateMemberCluster_FluxValid(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Mode: "pull", Backend: "flux",
				Pull: &kaprov1alpha1.PullConfig{Namespace: "flux-system", OCIRepository: "cluster-a"},
			},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMemberCluster_UnsupportedBackend(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Mode: "pull", Backend: "kserve"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for unsupported actuator backend")
	}
}

// ---- ReleaseValidator -------------------------------------------------------

func TestValidateRelease_MissingVersion(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: "pipe-1"},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestValidateRelease_MissingPipelines(t *testing.T) {
	r := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version:   "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
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
			Version: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout", DependsOn: []string{"wave1"}},
			},
		},
	}
	if err := releaseValidate(r); err == nil {
		t.Fatal("expected error for self-cycle in pipeline DAG")
	}
}

func TestValidateReleaseUpdate_VersionImmutable(t *testing.T) {
	old := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Version = "art-v2"
	if err := admission.ValidateReleaseUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable version update")
	}
}

func TestValidateReleaseUpdate_PipelinesImmutable(t *testing.T) {
	old := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Pipelines = append(new.Spec.Pipelines, kaprov1alpha1.ReleasePipelineRef{Name: "wave2", Pipeline: "rollout-2"})
	if err := admission.ValidateReleaseUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable pipelines update")
	}
}

func TestValidateReleaseUpdate_ScopeImmutable(t *testing.T) {
	old := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout"},
			},
			Scope: &kaprov1alpha1.ReleaseScope{Targets: []string{"cluster-a"}},
		},
	}
	new := old.DeepCopy()
	new.Spec.Scope = &kaprov1alpha1.ReleaseScope{Targets: []string{"cluster-b"}}
	if err := admission.ValidateReleaseUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable scope update")
	}
}

func TestValidateReleaseUpdate_SuspendedMutable(t *testing.T) {
	old := &kaprov1alpha1.Release{
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "art-v1",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "wave1", Pipeline: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Suspended = true
	if err := admission.ValidateReleaseUpdate(old, new); err != nil {
		t.Fatalf("unexpected error for mutable suspended update: %v", err)
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
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("does-not-exist")},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for unknown stage dependency")
	}
}

func TestValidatePipeline_InvalidDependencyStrategy(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{
			Name:     "s2",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			DependsOn: []kaprov1alpha1.StageDependency{
				{Stage: "s1", Strategy: "some"},
			},
		},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for invalid dependency strategy")
	}
}

func TestValidatePipeline_NegativeDependencySoak(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{
			Name:     "s2",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			DependsOn: []kaprov1alpha1.StageDependency{
				{Stage: "s1", RequiredSoakTime: &metav1.Duration{Duration: -1}},
			},
		},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for negative dependency soak")
	}
}

func TestValidatePipeline_StageCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("s2")},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: deps("s1")},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for cycle in stage DAG")
	}
}

func TestValidatePipeline_SelfCycle(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("s1")},
	})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected error for self-cycle")
	}
}

func TestValidatePipeline_ValidLinearDAG(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}}, DependsOn: deps("s1")},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: deps("s2")},
	})
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePipeline_MetricPresetReference(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{{
		Name:     "s1",
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
		Gate: &kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{Preset: "error-rate"}},
			},
		},
	}})
	p.Spec.MetricPresets = map[string]kaprov1alpha1.MetricGate{
		"error-rate": {Provider: "prometheus", Query: "up", Threshold: 0.01},
	}
	if err := pipelineValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePipeline_UnknownMetricPresetReference(t *testing.T) {
	p := buildPipeline([]kaprov1alpha1.Stage{{
		Name:     "s1",
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
		Gate: &kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{Preset: "missing"}},
			},
		},
	}})
	if err := pipelineValidate(p); err == nil {
		t.Fatal("expected unknown metric preset error")
	}
}

func TestValidatePipeline_ValidDiamondDAG(t *testing.T) {
	// s1 → s2, s1 → s3, s2+s3 → s4
	p := buildPipeline([]kaprov1alpha1.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "eu"}}, DependsOn: deps("s1")},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "us"}}, DependsOn: deps("s1")},
		{Name: "s4", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "global"}}, DependsOn: deps("s2", "s3")},
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

func TestValidateApproval_RefRequired(t *testing.T) {
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-target-a"},
		Spec: kaprov1alpha1.ApprovalSpec{
			Release:    "rel-1",
			Target:     "target-a",
			ApprovedBy: "alice",
		},
	}
	if err := approvalValidate(approval); err == nil {
		t.Fatal("expected error for missing approval ref")
	}
}

func TestValidateApproval_NameMatchesReleaseAndRef(t *testing.T) {
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-ref-a"},
		Spec: kaprov1alpha1.ApprovalSpec{
			Release:    "rel-1",
			Target:     "target-a",
			Ref:        "ref-a",
			ApprovedBy: "alice",
		},
	}
	if err := approvalValidate(approval); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func approvalValidate(a *kaprov1alpha1.Approval) error {
	return admission.ValidateApproval(a)
}

func buildPipeline(stages []kaprov1alpha1.Stage) *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pipeline"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: stages,
		},
	}
}

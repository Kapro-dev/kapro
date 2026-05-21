package admission_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/webhook/admission"
)

// deps converts a list of stage names into StageDependency values for test brevity.
func deps(names ...string) []kaprov1alpha2.StageDependency {
	out := make([]kaprov1alpha2.StageDependency, len(names))
	for i, n := range names {
		out[i] = kaprov1alpha2.StageDependency{Stage: n}
	}
	return out
}

// ---- FleetClusterValidator ---------------------------------------------------

func TestValidateFleetCluster_MissingMode(t *testing.T) {
	mc := &kaprov1alpha2.Cluster{
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "", BackendRef: "flux"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for missing actuator mode")
	}
}

func TestValidateFleetCluster_MissingBackend(t *testing.T) {
	mc := &kaprov1alpha2.Cluster{
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: ""},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for missing actuator backend")
	}
}

func TestValidateFleetCluster_FluxMissingSubSpec(t *testing.T) {
	mc := &kaprov1alpha2.Cluster{
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}
	if err := mcValidate(mc); err == nil {
		t.Fatal("expected error for pull/flux without ociRepository parameter")
	}
}

func TestValidateFleetCluster_FluxValid(t *testing.T) {
	mc := &kaprov1alpha2.Cluster{
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode: "pull", BackendRef: "flux",
				Parameters: map[string]string{"namespace": "flux-system", "ociRepository": "cluster-a"},
			},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFleetCluster_CustomBackendAllowed(t *testing.T) {
	mc := &kaprov1alpha2.Cluster{
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "kserve"},
		},
	}
	if err := mcValidate(mc); err != nil {
		t.Fatalf("unexpected error for external backendRef: %v", err)
	}
}

// ---- PromotionRunValidator -------------------------------------------------------

func TestValidatePromotionRun_MissingVersion(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "initial", Plan: "pipe-1"},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestValidatePromotionRun_ValidVersionsMap(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Versions: map[string]string{
				"api":    "main@sha256:abc",
				"worker": "main@sha256:def",
			},
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "initial", Plan: "pipe-1"},
			},
		},
	}
	if err := promotionrunValidate(r); err != nil {
		t.Fatalf("expected versions map promotionrun to be valid: %v", err)
	}
}

func TestValidatePromotionRun_MissingPlans(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans:   nil,
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for missing promotionplans")
	}
}

func TestValidatePromotionRun_PromotionPlanRefMissingName(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "", Plan: "standard-rollout"},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for promotionplan ref with empty name")
	}
}

func TestValidatePromotionRun_PromotionPlanRefMissingPromotionPlan(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "initial", Plan: ""},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for promotionplan ref with empty promotionplan")
	}
}

func TestValidatePromotionRun_Valid(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "initial", Plan: "standard-rollout"},
			},
		},
	}
	if err := promotionrunValidate(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePromotionRun_ValidMultiPromotionPlanDAG(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "canary", Plan: "canary-rollout"},
				{Name: "stable", Plan: "stable-rollout", DependsOn: []string{"canary"}},
			},
		},
	}
	if err := promotionrunValidate(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePromotionRun_DuplicatePromotionPlanName(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
				{Name: "wave1", Plan: "rollout"},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for duplicate promotionplan node name")
	}
}

func TestValidatePromotionRun_UnknownDependency(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout", DependsOn: []string{"does-not-exist"}},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for unknown promotionplan node dependency")
	}
}

func TestValidatePromotionRun_PromotionPlanCycle(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "a", Plan: "rollout", DependsOn: []string{"b"}},
				{Name: "b", Plan: "rollout", DependsOn: []string{"a"}},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for cycle in promotionplan DAG")
	}
}

func TestValidatePromotionRun_SelfCycle(t *testing.T) {
	r := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout", DependsOn: []string{"wave1"}},
			},
		},
	}
	if err := promotionrunValidate(r); err == nil {
		t.Fatal("expected error for self-cycle in promotionplan DAG")
	}
}

func TestValidatePromotionRunUpdate_VersionImmutable(t *testing.T) {
	old := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Version = "art-v2"
	if err := admission.ValidatePromotionRunUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable version update")
	}
}

func TestValidatePromotionRunUpdate_VersionsImmutable(t *testing.T) {
	old := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Versions: map[string]string{"api": "main@sha256:abc"},
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Versions["api"] = "main@sha256:def"
	if err := admission.ValidatePromotionRunUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable versions update")
	}
}

func TestValidatePromotionRunUpdate_PlansImmutable(t *testing.T) {
	old := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Plans = append(new.Spec.Plans, kaprov1alpha2.PlanRef{Name: "wave2", Plan: "rollout-2"})
	if err := admission.ValidatePromotionRunUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable promotionplans update")
	}
}

func TestValidatePromotionRunUpdate_ScopeImmutable(t *testing.T) {
	old := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
			},
			Scope: &kaprov1alpha2.PromotionRunScope{Targets: []string{"cluster-a"}},
		},
	}
	new := old.DeepCopy()
	new.Spec.Scope = &kaprov1alpha2.PromotionRunScope{Targets: []string{"cluster-b"}}
	if err := admission.ValidatePromotionRunUpdate(old, new); err == nil {
		t.Fatal("expected error for immutable scope update")
	}
}

func TestValidatePromotionRunUpdate_SuspendedMutable(t *testing.T) {
	old := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "art-v1",
			Plans: []kaprov1alpha2.PlanRef{
				{Name: "wave1", Plan: "rollout"},
			},
		},
	}
	new := old.DeepCopy()
	new.Spec.Suspended = true
	if err := admission.ValidatePromotionRunUpdate(old, new); err != nil {
		t.Fatalf("unexpected error for mutable suspended update: %v", err)
	}
}

// ---- PromotionPlanValidator -------------------------------------------------------

func TestValidatePromotionPlan_EmptyStages(t *testing.T) {
	p := buildPromotionPlan(nil)
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for empty stages")
	}
}

func TestValidatePromotionPlan_UnknownDependency(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("does-not-exist")},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for unknown stage dependency")
	}
}

func TestValidatePromotionPlan_InvalidDependencyStrategy(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{
			Name:     "s2",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			DependsOn: []kaprov1alpha2.StageDependency{
				{Stage: "s1", Strategy: "some"},
			},
		},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for invalid dependency strategy")
	}
}

func TestValidatePromotionPlan_NegativeDependencySoak(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{
			Name:     "s2",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			DependsOn: []kaprov1alpha2.StageDependency{
				{Stage: "s1", RequiredSoakTime: &metav1.Duration{Duration: -1}},
			},
		},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for negative dependency soak")
	}
}

func TestValidatePromotionPlan_StageCycle(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("s2")},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: deps("s1")},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for cycle in stage DAG")
	}
}

func TestValidatePromotionPlan_SelfCycle(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}, DependsOn: deps("s1")},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for self-cycle")
	}
}

func TestValidatePromotionPlan_ValidLinearDAG(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}}, DependsOn: deps("s1")},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}, DependsOn: deps("s2")},
	})
	if err := promotionplanValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePromotionPlan_MetricPresetReference(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{{
		Name:     "s1",
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{Preset: "error-rate"}},
			},
		},
	}})
	p.Spec.MetricPresets = map[string]kaprov1alpha2.MetricGate{
		"error-rate": {Provider: "prometheus", Query: "up", Threshold: float64Ptr(0.01)},
	}
	if err := promotionplanValidate(p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePromotionPlan_MetricWithoutPresetRequiresProviderAndQuery(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{{
		Name:     "s1",
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{Provider: "prometheus"}},
			},
		},
	}})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected metric query validation error")
	}
}

func TestValidatePromotionPlan_UnknownMetricPresetReference(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{{
		Name:     "s1",
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{Preset: "missing"}},
			},
		},
	}})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected unknown metric preset error")
	}
}

func TestValidatePromotionPlan_ValidDiamondDAG(t *testing.T) {
	// s1 → s2, s1 → s3, s2+s3 → s4
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}}},
		{Name: "s2", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "eu"}}, DependsOn: deps("s1")},
		{Name: "s3", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"region": "us"}}, DependsOn: deps("s1")},
		{Name: "s4", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "global"}}, DependsOn: deps("s2", "s3")},
	})
	if err := promotionplanValidate(p); err != nil {
		t.Fatalf("unexpected error for diamond DAG: %v", err)
	}
}

func TestValidatePromotionPlan_DuplicateStageName(t *testing.T) {
	p := buildPromotionPlan([]kaprov1alpha2.Stage{
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}}},
		{Name: "s1", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}},
	})
	if err := promotionplanValidate(p); err == nil {
		t.Fatal("expected error for duplicate stage name")
	}
}

func TestValidateApproval_RefRequired(t *testing.T) {
	approval := &kaprov1alpha2.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-target-a"},
		Spec: kaprov1alpha2.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-a",
			ApprovedBy:   "alice",
		},
	}
	if err := approvalValidate(approval); err == nil {
		t.Fatal("expected error for missing approval ref")
	}
}

func TestValidateApproval_NameMatchesPromotionRunAndRef(t *testing.T) {
	approval := &kaprov1alpha2.Approval{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-ref-a"},
		Spec: kaprov1alpha2.ApprovalSpec{
			PromotionRun: "rel-1",
			Target:       "target-a",
			Ref:          "ref-a",
			ApprovedBy:   "alice",
		},
	}
	if err := approvalValidate(approval); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- helpers ----------------------------------------------------------------

func mcValidate(mc *kaprov1alpha2.Cluster) error {
	return admission.ValidateFleetCluster(mc)
}

func promotionrunValidate(r *kaprov1alpha2.PromotionRun) error {
	return admission.ValidatePromotionRun(r)
}

func promotionplanValidate(p *kaprov1alpha2.Plan) error {
	return admission.ValidatePromotionPlan(p)
}

func approvalValidate(a *kaprov1alpha2.Approval) error {
	return admission.ValidateApproval(a)
}

func float64Ptr(v float64) *float64 {
	return &v
}

func buildPromotionPlan(stages []kaprov1alpha2.Stage) *kaprov1alpha2.Plan {
	return &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "test-promotionplan"},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: stages,
		},
	}
}

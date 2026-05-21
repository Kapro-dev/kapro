package planner

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestFrameworkFiltersScoresAndPermitsTargets(t *testing.T) {
	plugin := &testPlugin{}
	targets := []kaprov1alpha2.Cluster{
		target("cluster-b", "10"),
		target("cluster-a", "90"),
		target("cluster-c", "50", "skip", "true"),
		target("cluster-d", "100", "permit", "false"),
	}

	got, err := NewFramework(plugin).Plan(context.Background(), Request{Stage: kaprov1alpha2.Stage{Name: "canary"}}, targets)
	if err != nil {
		t.Fatal(err)
	}
	names := targetNames(got)
	want := []string{"cluster-a", "cluster-b"}
	if len(names) != len(want) {
		t.Fatalf("targets = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("targets = %v, want %v", names, want)
		}
	}
	if !plugin.preFiltered || len(plugin.reserved) != 3 {
		t.Fatalf("plugin phases not run: preFiltered=%v reserved=%v", plugin.preFiltered, plugin.reserved)
	}
}

func TestFrameworkDefaultOrderingIsDeterministic(t *testing.T) {
	got, err := NewFramework().Plan(context.Background(), Request{}, []kaprov1alpha2.Cluster{
		target("cluster-c", ""),
		target("cluster-a", ""),
		target("cluster-b", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	names := targetNames(got)
	want := []string{"cluster-a", "cluster-b", "cluster-c"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("targets = %v, want %v", names, want)
		}
	}
}

func TestFrameworkPlanWithResultRecordsSkippedTargets(t *testing.T) {
	result, err := NewFramework(&testPlugin{}).PlanWithResult(context.Background(), Request{}, []kaprov1alpha2.Cluster{
		target("cluster-a", "10", "skip", "true"),
		target("cluster-b", "10", "permit", "false"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %v, want none", targetNames(result.Targets))
	}
	if len(result.Decisions) != 2 {
		t.Fatalf("decisions = %#v, want 2", result.Decisions)
	}
	if result.Decisions[0].Target != "cluster-a" || result.Decisions[0].Phase != "Filter" {
		t.Fatalf("first decision = %#v", result.Decisions[0])
	}
	if result.Decisions[1].Target != "cluster-b" || result.Decisions[1].Phase != "Permit" {
		t.Fatalf("second decision = %#v", result.Decisions[1])
	}
}

func TestDefaultFrameworkSkipsNotReadyAndDifferentActivePromotionRun(t *testing.T) {
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "promotionrun-a"}}
	ready := target("cluster-a", "")
	notReady := target("cluster-b", "")
	notReady.Status.Conditions = []metav1.Condition{{
		Type:   "Ready",
		Status: metav1.ConditionFalse,
		Reason: "Disconnected",
	}}
	busy := target("cluster-c", "")
	busy.Status.ActivePromotionRun = "promotionrun-b"

	result, err := NewDefaultFramework().PlanWithResult(context.Background(), Request{PromotionRun: promotionrun}, []kaprov1alpha2.Cluster{
		busy,
		notReady,
		ready,
	})
	if err != nil {
		t.Fatal(err)
	}
	names := targetNames(result.Targets)
	if len(names) != 1 || names[0] != "cluster-a" {
		t.Fatalf("targets = %v, want [cluster-a]", names)
	}
	if len(result.Decisions) != 2 {
		t.Fatalf("decisions = %#v, want 2", result.Decisions)
	}
	reasons := map[string]string{}
	for _, decision := range result.Decisions {
		reasons[decision.Target] = decision.Reason
	}
	if reasons["cluster-b"] != "ClusterNotReady" {
		t.Fatalf("cluster-b reason = %q", reasons["cluster-b"])
	}
	if reasons["cluster-c"] != "DifferentActivePromotionRun" {
		t.Fatalf("cluster-c reason = %q", reasons["cluster-c"])
	}
}

type testPlugin struct {
	preFiltered bool
	reserved    []string
}

func (p *testPlugin) Name() string { return "test" }

func (p *testPlugin) PreFilter(context.Context, *CycleState, Request, []kaprov1alpha2.Cluster) *Status {
	p.preFiltered = true
	return nil
}

func (p *testPlugin) Filter(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha2.Cluster) *Status {
	if target.Labels["skip"] == "true" {
		return NewStatus(Skip, "skipped by test")
	}
	return nil
}

func (p *testPlugin) Score(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha2.Cluster) (int64, *Status) {
	switch target.Labels["score"] {
	case "90":
		return 90, nil
	case "50":
		return 50, nil
	case "100":
		return 100, nil
	default:
		return 10, nil
	}
}

func (p *testPlugin) Reserve(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha2.Cluster) *Status {
	p.reserved = append(p.reserved, target.Name)
	return nil
}

func (p *testPlugin) Permit(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha2.Cluster) *Status {
	if target.Labels["permit"] == "false" {
		return NewStatus(Skip, "blocked by permit")
	}
	return nil
}

func target(name, score string, labels ...string) kaprov1alpha2.Cluster {
	allLabels := map[string]string{}
	if score != "" {
		allLabels["score"] = score
	}
	for i := 0; i+1 < len(labels); i += 2 {
		allLabels[labels[i]] = labels[i+1]
	}
	return kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: allLabels}}
}

func targetNames(targets []kaprov1alpha2.Cluster) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}

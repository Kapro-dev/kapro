package planner

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestFrameworkFiltersScoresAndPermitsTargets(t *testing.T) {
	plugin := &testPlugin{}
	targets := []kaprov1alpha1.MemberCluster{
		target("cluster-b", "10"),
		target("cluster-a", "90"),
		target("cluster-c", "50", "skip", "true"),
		target("cluster-d", "100", "permit", "false"),
	}

	got, err := NewFramework(plugin).Plan(context.Background(), Request{Stage: kaprov1alpha1.Stage{Name: "canary"}}, targets)
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
	got, err := NewFramework().Plan(context.Background(), Request{}, []kaprov1alpha1.MemberCluster{
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

type testPlugin struct {
	preFiltered bool
	reserved    []string
}

func (p *testPlugin) Name() string { return "test" }

func (p *testPlugin) PreFilter(context.Context, *CycleState, Request, []kaprov1alpha1.MemberCluster) *Status {
	p.preFiltered = true
	return nil
}

func (p *testPlugin) Filter(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha1.MemberCluster) *Status {
	if target.Labels["skip"] == "true" {
		return NewStatus(Skip, "skipped by test")
	}
	return nil
}

func (p *testPlugin) Score(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha1.MemberCluster) (int64, *Status) {
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

func (p *testPlugin) Reserve(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha1.MemberCluster) *Status {
	p.reserved = append(p.reserved, target.Name)
	return nil
}

func (p *testPlugin) Permit(_ context.Context, _ *CycleState, _ Request, target kaprov1alpha1.MemberCluster) *Status {
	if target.Labels["permit"] == "false" {
		return NewStatus(Skip, "blocked by permit")
	}
	return nil
}

func target(name, score string, labels ...string) kaprov1alpha1.MemberCluster {
	allLabels := map[string]string{}
	if score != "" {
		allLabels["score"] = score
	}
	for i := 0; i+1 < len(labels); i += 2 {
		allLabels[labels[i]] = labels[i+1]
	}
	return kaprov1alpha1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: allLabels}}
}

func targetNames(targets []kaprov1alpha1.MemberCluster) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}

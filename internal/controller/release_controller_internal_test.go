package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestStageDependencySatisfied_AnyUnlocksFromOneConvergedTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				},
				{
					Target:      "cluster-b",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseHealthCheck,
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:            "canary",
		Strategy:         kaprov1alpha1.StageDependencyAny,
		RequiredSoakTime: &metav1.Duration{Duration: time.Hour},
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if !satisfied {
		t.Fatalf("expected any dependency to be satisfied, wait=%s", wait)
	}
	if wait != 0 {
		t.Fatalf("expected no remaining wait, got %s", wait)
	}
}

func TestStageDependencySatisfied_AllRequiresEveryTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				},
				{
					Target:      "cluster-b",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseApplying,
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:    "canary",
		Strategy: kaprov1alpha1.StageDependencyAll,
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if satisfied {
		t.Fatal("expected all dependency to wait for every target")
	}
	if wait != 0 {
		t.Fatalf("expected no timer wait while target is still running, got %s", wait)
	}
}

func TestStageDependencySatisfied_ReturnsRemainingSoakTime(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:            "canary",
		RequiredSoakTime: &metav1.Duration{Duration: time.Hour},
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if satisfied {
		t.Fatal("expected dependency to wait for soak time")
	}
	if wait <= 0 {
		t.Fatalf("expected positive remaining soak wait, got %s", wait)
	}
	if wait > time.Hour {
		t.Fatalf("expected wait below one hour, got %s", wait)
	}
}

func memberClusterForStage(name, stage string) *kaprov1alpha1.MemberCluster {
	return &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"stage": stage},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
	}
}

func pipelineWithCanaryStage() *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "pipeline"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{
					Name:     "canary",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"stage": "canary"}},
				},
			},
		},
	}
}

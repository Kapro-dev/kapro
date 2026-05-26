package runtimeview

import (
	"testing"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewPromotionSnapshotDeepCopiesInputs(t *testing.T) {
	promotion := &kaprov1alpha1.Promotion{ObjectMeta: metav1.ObjectMeta{Name: "checkout"}}
	run := &kaproruntimev1alpha1.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "checkout-1"}}
	targets := []kaproruntimev1alpha1.Target{{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-prod", Labels: map[string]string{"tier": "prod"}},
		Spec:       kaprov1alpha1.TargetSpec{Target: "prod"},
	}}
	clusters := []kaprov1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Labels: map[string]string{"region": "eu"}},
	}}
	substrates := []kaprov1alpha1.Substrate{{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeSpokePull},
		},
	}}
	classes := []kaprov1alpha1.SubstrateClass{{
		ObjectMeta: metav1.ObjectMeta{Name: "flux", Labels: map[string]string{"owner": "platform"}},
	}}

	snap := NewPromotionSnapshot(promotion, run, targets, clusters, substrates, classes, time.Unix(10, 0))

	promotion.Name = "mutated"
	run.Name = "mutated"
	targets[0].Labels["tier"] = "dev"
	clusters[0].Labels["region"] = "us"
	substrates[0].Spec.ClassRef.Name = "argo"
	classes[0].Labels["owner"] = "mutated"

	if snap.Promotion.Name != "checkout" || snap.Run.Name != "checkout-1" {
		t.Fatalf("snapshot root objects were mutated: %#v %#v", snap.Promotion, snap.Run)
	}
	if snap.Targets[0].Labels["tier"] != "prod" {
		t.Fatalf("target labels were not copied: %#v", snap.Targets[0].Labels)
	}
	if snap.Clusters[0].Labels["region"] != "eu" {
		t.Fatalf("cluster labels were not copied: %#v", snap.Clusters[0].Labels)
	}
	if snap.Substrates[0].Spec.ClassRef.Name != "flux" {
		t.Fatalf("substrate spec was not copied: %#v", snap.Substrates[0].Spec)
	}
	if snap.SubstrateClasses[0].Labels["owner"] != "platform" {
		t.Fatalf("substrate class labels were not copied: %#v", snap.SubstrateClasses[0].Labels)
	}
}

func TestDispatchOperationCarriesInternalRuntimePlacement(t *testing.T) {
	op := DispatchOperation{
		Key:           WorkloadKey{Promotion: "checkout", Run: "checkout-1", PlanRef: "main", Stage: "canary", Target: "eu-canary"},
		SubstrateRef:  "flux",
		ExecutionMode: kaprov1alpha1.ExecutionModeSpokePull,
		Topology: TopologyHint{
			Region:      "eu",
			Tier:        "canary",
			Accelerator: "nvidia-h100",
			GPUCount:    8,
			QueueClass:  QueueClassCanary,
		},
		Shard: ShardAssignment{Name: "eu", IsDefault: false},
	}
	if op.Topology.QueueClass != QueueClassCanary || op.Shard.Name != "eu" {
		t.Fatalf("dispatch runtime hints changed: %#v", op)
	}
}

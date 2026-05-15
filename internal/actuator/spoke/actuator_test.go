package spoke

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
)

func TestApplyDeltaRecordsDesiredVersionsOnMemberCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			DesiredVersions: map[string]string{"worker": "v1"},
			Delivery: kaprov1alpha1.DeliverySpec{
				Mode:       "pull",
				BackendRef: "flux",
				Parameters: map[string]string{
					"ociRepository": "cluster-a-bundle",
				},
			},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			CurrentVersions: map[string]string{"default": "v1"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).Build()
	act := &SpokeFluxActuator{HubClient: c}

	changed, err := act.ApplyDelta(context.Background(), actuator.DeltaApplyRequest{
		Cluster:         mc,
		DesiredVersions: map[string]string{"default": "v2", "api": "v2"},
	})
	if err != nil {
		t.Fatalf("ApplyDelta returned error: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed=%d, want 2", changed)
	}

	var updated kaprov1alpha1.MemberCluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: "cluster-a"}, &updated); err != nil {
		t.Fatalf("get updated MemberCluster: %v", err)
	}
	if updated.Spec.DesiredVersions["default"] != "v2" ||
		updated.Spec.DesiredVersions["api"] != "v2" ||
		updated.Spec.DesiredVersions["worker"] != "v1" {
		t.Fatalf("desiredVersions=%v, want default/api=v2 and worker retained", updated.Spec.DesiredVersions)
	}
	if updated.Spec.DesiredVersion != "v2" || updated.Spec.DesiredAppKey != "default" {
		t.Fatalf("compat desired fields = %q/%q, want v2/default", updated.Spec.DesiredVersion, updated.Spec.DesiredAppKey)
	}
}

func TestIsAllConvergedUsesSpokeReportedStatus(t *testing.T) {
	act := &SpokeFluxActuator{}
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha1.MemberClusterStatus{
			CurrentVersions: map[string]string{"default": "v2", "api": "v2"},
			Health:          kaprov1alpha1.ClusterHealth{AllWorkloadsReady: true},
		},
	}

	converged, err := act.IsAllConverged(context.Background(), mc, map[string]string{"default": "v2", "api": "v2"})
	if err != nil {
		t.Fatalf("IsAllConverged returned error: %v", err)
	}
	if !converged {
		t.Fatal("expected converged from MemberCluster reported status")
	}
}

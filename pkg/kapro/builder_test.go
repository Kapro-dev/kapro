package kapro

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestFleetBuilder(t *testing.T) {
	labels := map[string]string{"env": "dev"}

	fleet := NewFleet("checkout").
		WithBackend("flux").
		WithCluster("kind-dev", labels).
		Build()

	if fleet.APIVersion != kaprov1alpha2.GroupVersion.String() {
		t.Fatalf("APIVersion = %q", fleet.APIVersion)
	}
	if fleet.Kind != "Fleet" {
		t.Fatalf("Kind = %q", fleet.Kind)
	}
	if fleet.Name != "checkout" {
		t.Fatalf("Name = %q", fleet.Name)
	}
	if fleet.Spec.Delivery.BackendRef != "flux" {
		t.Fatalf("backendRef = %q", fleet.Spec.Delivery.BackendRef)
	}
	if got := fleet.Spec.Clusters[0].Labels["env"]; got != "dev" {
		t.Fatalf("cluster label env = %q", got)
	}

	labels["env"] = "prod"
	if got := fleet.Spec.Clusters[0].Labels["env"]; got != "dev" {
		t.Fatalf("builder retained caller map alias, got %q", got)
	}
}

func TestPromotionBuilder(t *testing.T) {
	promotion := NewPromotion("checkout-v123").
		ForFleet("checkout").
		AtVersion("v1.2.3").
		Build()

	if promotion.APIVersion != kaprov1alpha2.GroupVersion.String() {
		t.Fatalf("APIVersion = %q", promotion.APIVersion)
	}
	if promotion.Kind != "Promotion" {
		t.Fatalf("Kind = %q", promotion.Kind)
	}
	if promotion.Name != "checkout-v123" {
		t.Fatalf("Name = %q", promotion.Name)
	}
	if promotion.Spec.FleetRef != "checkout" {
		t.Fatalf("fleetRef = %q", promotion.Spec.FleetRef)
	}
	if promotion.Spec.Version != "v1.2.3" {
		t.Fatalf("version = %q", promotion.Spec.Version)
	}
}

func TestPlanBuilder(t *testing.T) {
	stage := kaprov1alpha2.Stage{
		Name: "dev",
		Selector: metav1.LabelSelector{
			MatchLabels: map[string]string{"env": "dev"},
		},
	}

	plan := NewPlan("progressive").WithStage(stage).Build()

	if plan.APIVersion != kaprov1alpha2.GroupVersion.String() {
		t.Fatalf("APIVersion = %q", plan.APIVersion)
	}
	if plan.Kind != "Plan" {
		t.Fatalf("Kind = %q", plan.Kind)
	}
	if plan.Name != "progressive" {
		t.Fatalf("Name = %q", plan.Name)
	}
	if got := plan.Spec.Stages[0].Selector.MatchLabels["env"]; got != "dev" {
		t.Fatalf("stage label env = %q", got)
	}

	stage.Selector.MatchLabels["env"] = "prod"
	if got := plan.Spec.Stages[0].Selector.MatchLabels["env"]; got != "dev" {
		t.Fatalf("builder retained caller stage alias, got %q", got)
	}
}

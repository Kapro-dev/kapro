package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestFleetDriftReportCurrent(t *testing.T) {
	ctx := context.Background()
	report := fleetDriftReportFixture("all")
	c := fleetDriftReportClient(t,
		report,
		clusterWithCurrentVersion("prod", "default", "v2"),
		targetFixture("checkout-run", "prod", "v2", kaprov1alpha2.TargetPhaseConverged),
	)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "all")

	got := getFleetDriftReport(t, ctx, c, "all")
	if got.Status.Phase != kaprov1alpha2.FleetDriftReportPhaseCurrent {
		t.Fatalf("phase=%s, want Current: %#v", got.Status.Phase, got.Status)
	}
	if got.Status.Summary.TotalTargets != 1 || got.Status.Summary.CurrentTargets != 1 || len(got.Status.Targets) != 0 {
		t.Fatalf("summary=%#v targets=%#v, want one current target and no evidence entries", got.Status.Summary, got.Status.Targets)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != "Current" {
		t.Fatalf("Ready condition=%#v, want True/Current", ready)
	}
}

func TestFleetDriftReportDetectsConvergedVersionDrift(t *testing.T) {
	ctx := context.Background()
	report := fleetDriftReportFixture("prod-drift")
	c := fleetDriftReportClient(t,
		report,
		clusterWithCurrentVersion("prod", "default", "v1"),
		targetFixture("checkout-run", "prod", "v2", kaprov1alpha2.TargetPhaseConverged),
	)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "prod-drift")

	got := getFleetDriftReport(t, ctx, c, "prod-drift")
	if got.Status.Phase != kaprov1alpha2.FleetDriftReportPhaseDrifted {
		t.Fatalf("phase=%s, want Drifted: %#v", got.Status.Phase, got.Status)
	}
	if got.Status.Summary.DriftedTargets != 1 || len(got.Status.Targets) != 1 {
		t.Fatalf("summary=%#v targets=%#v, want one drifted target", got.Status.Summary, got.Status.Targets)
	}
	entry := got.Status.Targets[0]
	if entry.Reason != "VersionDrift" || entry.AppVersions[0].CurrentVersion != "v1" || entry.AppVersions[0].DesiredVersion != "v2" {
		t.Fatalf("entry=%#v, want drift evidence v1 -> v2", entry)
	}
}

func TestFleetDriftReportMarksMissingClusterUnknown(t *testing.T) {
	ctx := context.Background()
	report := fleetDriftReportFixture("unknown")
	c := fleetDriftReportClient(t,
		report,
		targetFixture("checkout-run", "missing", "v2", kaprov1alpha2.TargetPhaseConverged),
	)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "unknown")

	got := getFleetDriftReport(t, ctx, c, "unknown")
	if got.Status.Phase != kaprov1alpha2.FleetDriftReportPhaseUnknown || got.Status.Summary.UnknownTargets != 1 {
		t.Fatalf("status=%#v, want one unknown target", got.Status)
	}
	if got.Status.Targets[0].Reason != "ClusterMissing" {
		t.Fatalf("target reason=%q, want ClusterMissing", got.Status.Targets[0].Reason)
	}
}

func TestFleetDriftReportCountsBackendObjectDrift(t *testing.T) {
	ctx := context.Background()
	target := targetFixture("checkout-run", "prod", "v2", kaprov1alpha2.TargetPhaseConverged)
	target.Status.BackendObjects = []kaprov1alpha2.BackendObjectStatus{{
		APIVersion:     "argoproj.io/v1alpha1",
		Kind:           "Application",
		Namespace:      "argocd",
		Name:           "checkout",
		DesiredVersion: "v2",
		CurrentVersion: "v1",
		SyncStatus:     "OutOfSync",
	}}
	report := fleetDriftReportFixture("backend-drift")
	c := fleetDriftReportClient(t,
		report,
		clusterWithCurrentVersion("prod", "default", "v2"),
		target,
	)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "backend-drift")

	got := getFleetDriftReport(t, ctx, c, "backend-drift")
	if got.Status.Phase != kaprov1alpha2.FleetDriftReportPhaseDrifted {
		t.Fatalf("phase=%s, want Drifted", got.Status.Phase)
	}
	if got.Status.Summary.TotalBackendObjects != 1 || got.Status.Summary.DriftedBackendObjects != 1 {
		t.Fatalf("summary=%#v, want one drifted backend object", got.Status.Summary)
	}
	if len(got.Status.Targets) != 1 || len(got.Status.Targets[0].Objects) != 1 {
		t.Fatalf("targets=%#v, want backend object evidence", got.Status.Targets)
	}
	if got.Status.Targets[0].Reason != "BackendObjectDrift" {
		t.Fatalf("reason=%q, want BackendObjectDrift", got.Status.Targets[0].Reason)
	}
}

func TestFleetDriftReportFiltersByFleetAndTargetSelector(t *testing.T) {
	ctx := context.Background()
	report := fleetDriftReportFixture("payments", func(report *kaprov1alpha2.FleetDriftReport) {
		report.Spec.FleetRef = "checkout"
		report.Spec.TargetSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}
	})
	included := targetFixture("checkout-run", "prod", "v2", kaprov1alpha2.TargetPhaseConverged)
	included.Labels = map[string]string{"tier": "prod"}
	excludedBySelector := targetFixture("checkout-run", "stage", "v2", kaprov1alpha2.TargetPhaseConverged)
	excludedBySelector.Labels = map[string]string{"tier": "stage"}
	excludedByFleet := targetFixture("billing-run", "billing-prod", "v9", kaprov1alpha2.TargetPhaseConverged)
	excludedByFleet.Labels = map[string]string{"tier": "prod"}
	c := fleetDriftReportClient(t,
		report,
		&kaprov1alpha2.PromotionRun{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-run", Labels: map[string]string{promotionFleetLabel: "checkout"}},
		},
		&kaprov1alpha2.PromotionRun{
			ObjectMeta: metav1.ObjectMeta{Name: "billing-run", Labels: map[string]string{promotionFleetLabel: "billing"}},
		},
		clusterWithCurrentVersion("prod", "default", "v2"),
		clusterWithCurrentVersion("stage", "default", "v2"),
		clusterWithCurrentVersion("billing-prod", "default", "v9"),
		included,
		excludedBySelector,
		excludedByFleet,
	)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "payments")

	got := getFleetDriftReport(t, ctx, c, "payments")
	if got.Status.Summary.TotalTargets != 1 || got.Status.Summary.CurrentTargets != 1 {
		t.Fatalf("summary=%#v, want only checkout prod target included", got.Status.Summary)
	}
}

func TestFleetDriftReportCapsAppVersionEvidence(t *testing.T) {
	ctx := context.Background()
	target := targetFixture("checkout-run", "prod", "", kaprov1alpha2.TargetPhaseConverged)
	target.Spec.DesiredVersions = map[string]string{}
	for i := 0; i < maxFleetDriftAppVersions+5; i++ {
		target.Spec.DesiredVersions[fmt.Sprintf("app-%03d", i)] = "v2"
	}
	cluster := clusterWithCurrentVersion("prod", "default", "")
	cluster.Status.Version = ""
	cluster.Status.CurrentVersions = map[string]string{}
	for key := range target.Spec.DesiredVersions {
		cluster.Status.CurrentVersions[key] = "v2"
	}
	report := fleetDriftReportFixture("wide")
	c := fleetDriftReportClient(t, report, cluster, target)
	r := newFleetDriftReportReconciler(c)

	reconcileFleetDriftReport(t, ctx, r, "wide")

	got := getFleetDriftReport(t, ctx, c, "wide")
	if got.Status.Phase != kaprov1alpha2.FleetDriftReportPhaseCurrent {
		t.Fatalf("phase=%s, want Current", got.Status.Phase)
	}
	if got.Status.Summary.CurrentTargets != 1 || len(got.Status.Targets) != 0 {
		t.Fatalf("summary=%#v targets=%#v, want current target with no evidence", got.Status.Summary, got.Status.Targets)
	}

	target.Status.Phase = kaprov1alpha2.TargetPhaseFailed
	if err := c.Update(ctx, target); err != nil {
		t.Fatalf("update target: %v", err)
	}
	reconcileFleetDriftReport(t, ctx, r, "wide")
	got = getFleetDriftReport(t, ctx, c, "wide")
	if len(got.Status.Targets) != 1 || len(got.Status.Targets[0].AppVersions) != maxFleetDriftAppVersions {
		t.Fatalf("appVersions=%d, want cap %d", len(got.Status.Targets[0].AppVersions), maxFleetDriftAppVersions)
	}
	if got.Status.Targets[0].AppVersions[0].AppKey != "app-000" || got.Status.Targets[0].AppVersions[maxFleetDriftAppVersions-1].AppKey != "app-063" {
		t.Fatalf("appVersions not deterministically sorted/capped: first=%q last=%q", got.Status.Targets[0].AppVersions[0].AppKey, got.Status.Targets[0].AppVersions[maxFleetDriftAppVersions-1].AppKey)
	}
}

func newFleetDriftReportReconciler(c client.Client) *FleetDriftReportReconciler {
	return &FleetDriftReportReconciler{
		Client: c,
		Now: func() time.Time {
			return time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
		},
	}
}

func fleetDriftReportClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&kaprov1alpha2.FleetDriftReport{}).
		Build()
}

func fleetDriftReportFixture(name string, mutate ...func(*kaprov1alpha2.FleetDriftReport)) *kaprov1alpha2.FleetDriftReport {
	report := &kaprov1alpha2.FleetDriftReport{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
	}
	for _, fn := range mutate {
		fn(report)
	}
	return report
}

func targetFixture(run, cluster, version string, phase kaprov1alpha2.TargetPhase) *kaprov1alpha2.Target {
	return &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: run + "-" + cluster},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: run,
			Target:          cluster,
			PlanRef:         "primary",
			Plan:            "rollout",
			Stage:           "prod",
			Version:         version,
			AppKey:          "default",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				PromotionRunRef: run,
				Target:          cluster,
				PlanRef:         "primary",
				Plan:            "rollout",
				Stage:           "prod",
				Version:         version,
				AppKey:          "default",
				Phase:           phase,
			},
		},
	}
}

func clusterWithCurrentVersion(name, appKey, version string) *kaprov1alpha2.Cluster {
	return &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
		Status: kaprov1alpha2.ClusterStatus{
			Version:         version,
			CurrentVersions: map[string]string{appKey: version},
			Delivery: map[string]kaprov1alpha2.ClusterDeliveryStatus{
				appKey: {Phase: kaprov1alpha2.DeliveryPhaseConverged, DesiredVersion: version},
			},
		},
	}
}

func reconcileFleetDriftReport(t *testing.T, ctx context.Context, r *FleetDriftReportReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getFleetDriftReport(t *testing.T, ctx context.Context, c client.Client, name string) kaprov1alpha2.FleetDriftReport {
	t.Helper()
	var got kaprov1alpha2.FleetDriftReport
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatalf("get FleetDriftReport: %v", err)
	}
	return got
}

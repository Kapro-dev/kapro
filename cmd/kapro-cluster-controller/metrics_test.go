package main

import (
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

func TestMetricsDisabled(t *testing.T) {
	for _, addr := range []string{"", "off", "OFF", "disabled", "none"} {
		if !metricsDisabled(addr) {
			t.Fatalf("metricsDisabled(%q)=false, want true", addr)
		}
	}
	if metricsDisabled(":8080") {
		t.Fatal("metricsDisabled(:8080)=true, want false")
	}
}

func TestObserveSpokeDeliveryStagingMetrics(t *testing.T) {
	successStaging := []string{"metrics-c2", "oci", string(kaprov1alpha1.DeliveryPhaseStaging), "success"}
	successApplying := []string{"metrics-c2", "oci", string(kaprov1alpha1.DeliveryPhaseApplying), "success"}
	beforeStaging := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(successStaging...))
	beforeApplying := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(successApplying...))

	observeSpokeDelivery("metrics-c2", "oci", spokeprovider.ReconcileResult{
		Phase: kaprov1alpha1.DeliveryPhaseConverged,
		Staging: &kaprov1alpha1.DeliveryStagingStatus{
			StagedObjects:    2,
			CommittedObjects: 2,
		},
	}, time.Second)

	if got := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(successStaging...)) - beforeStaging; got != 1 {
		t.Fatalf("staging success delta=%v, want 1", got)
	}
	if got := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(successApplying...)) - beforeApplying; got != 1 {
		t.Fatalf("applying success delta=%v, want 1", got)
	}

	errorStaging := []string{"metrics-c2", "oci", string(kaprov1alpha1.DeliveryPhaseStaging), "error"}
	beforeError := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(errorStaging...))
	observeSpokeDelivery("metrics-c2", "oci", spokeprovider.ReconcileResult{
		Phase: kaprov1alpha1.DeliveryPhaseFailed,
		Staging: &kaprov1alpha1.DeliveryStagingStatus{
			StagingFailedObjects: 1,
			FailurePhase:         kaprov1alpha1.DeliveryPhaseStaging,
		},
	}, time.Second)
	if got := promtestutil.ToFloat64(spokeDeliveryStagingResults.WithLabelValues(errorStaging...)) - beforeError; got != 1 {
		t.Fatalf("staging error delta=%v, want 1", got)
	}
}

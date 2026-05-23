package gate_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/gate"
)

func TestMaxDriftGatePassesWithinBudget(t *testing.T) {
	report := maxDriftReport("prod", kaprov1alpha2.FleetDriftReportStatus{
		Phase:      kaprov1alpha2.FleetDriftReportPhaseDrifted,
		ObservedAt: ptrMetaTime(time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)),
		Summary: kaprov1alpha2.FleetDriftSummary{
			TotalTargets:          3,
			CurrentTargets:        2,
			DriftedTargets:        1,
			TotalBackendObjects:   4,
			DriftedBackendObjects: 1,
		},
	})
	result := evaluateMaxDrift(t, report, map[string]string{
		"reportRef":                "prod",
		"maxDriftedTargets":        "1",
		"maxDriftedBackendObjects": "1",
		"maxAge":                   "10m",
	})
	if !result.IsPassed() {
		t.Fatalf("phase=%s reason=%s message=%q, want passed", result.Phase, result.Reason, result.Message)
	}
}

func TestMaxDriftGateBlocksWithRetryByDefault(t *testing.T) {
	report := maxDriftReport("prod", kaprov1alpha2.FleetDriftReportStatus{
		Phase:      kaprov1alpha2.FleetDriftReportPhaseDrifted,
		ObservedAt: ptrMetaTime(time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)),
		Summary: kaprov1alpha2.FleetDriftSummary{
			TotalTargets:   1,
			DriftedTargets: 1,
		},
	})
	result := evaluateMaxDrift(t, report, map[string]string{"reportRef": "prod"})
	if !result.IsInconclusive() || result.Reason != "DriftBudgetExceeded" {
		t.Fatalf("phase=%s reason=%s, want inconclusive DriftBudgetExceeded", result.Phase, result.Reason)
	}
}

func TestMaxDriftGateMissingReport(t *testing.T) {
	result := evaluateMaxDrift(t, nil, map[string]string{"reportRef": "missing"})
	if !result.IsInconclusive() || result.Reason != "ReportMissing" {
		t.Fatalf("phase=%s reason=%s, want inconclusive ReportMissing", result.Phase, result.Reason)
	}

	result = evaluateMaxDrift(t, nil, map[string]string{"reportRef": "missing", "allowMissing": "true"})
	if !result.IsPassed() || result.Reason != "ReportMissingAllowed" {
		t.Fatalf("phase=%s reason=%s, want passed ReportMissingAllowed", result.Phase, result.Reason)
	}
}

func TestMaxDriftGateStaleReport(t *testing.T) {
	report := maxDriftReport("prod", kaprov1alpha2.FleetDriftReportStatus{
		Phase:      kaprov1alpha2.FleetDriftReportPhaseCurrent,
		ObservedAt: ptrMetaTime(time.Date(2026, 5, 23, 11, 0, 0, 0, time.UTC)),
		Summary:    kaprov1alpha2.FleetDriftSummary{TotalTargets: 1, CurrentTargets: 1},
	})
	result := evaluateMaxDrift(t, report, map[string]string{"reportRef": "prod", "maxAge": "10m"})
	if !result.IsInconclusive() || result.Reason != "StaleReport" {
		t.Fatalf("phase=%s reason=%s, want inconclusive StaleReport", result.Phase, result.Reason)
	}

	result = evaluateMaxDrift(t, report, map[string]string{"reportRef": "prod", "maxAge": "10m", "allowStale": "true"})
	if !result.IsPassed() || result.Reason != "StaleReportAllowed" {
		t.Fatalf("phase=%s reason=%s, want passed StaleReportAllowed", result.Phase, result.Reason)
	}
}

func TestMaxDriftGateFailsOnInvalidThreshold(t *testing.T) {
	report := maxDriftReport("prod", kaprov1alpha2.FleetDriftReportStatus{
		Phase:      kaprov1alpha2.FleetDriftReportPhaseCurrent,
		ObservedAt: ptrMetaTime(time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)),
	})
	result := evaluateMaxDrift(t, report, map[string]string{"reportRef": "prod", "maxDriftedTargets": "-1"})
	if !result.IsFailed() || result.Reason != "InvalidThreshold" {
		t.Fatalf("phase=%s reason=%s, want failed InvalidThreshold", result.Phase, result.Reason)
	}
}

func evaluateMaxDrift(t *testing.T, report *kaprov1alpha2.FleetDriftReport, params map[string]string) gate.Result {
	t.Helper()
	objects := []client.Object{}
	if report != nil {
		objects = append(objects, report)
	}
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	result, err := (&gate.MaxDriftGate{
		Client: c,
		Now: func() time.Time {
			return time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC)
		},
	}).Evaluate(context.Background(), gate.Request{Parameters: params})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return result
}

func maxDriftReport(name string, status kaprov1alpha2.FleetDriftReportStatus) *kaprov1alpha2.FleetDriftReport {
	return &kaprov1alpha2.FleetDriftReport{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     status,
	}
}

func ptrMetaTime(t time.Time) *metav1.Time {
	meta := metav1.NewTime(t)
	return &meta
}

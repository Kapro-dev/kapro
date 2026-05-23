package gate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	defaultMaxDriftRetryAfter = "30s"
)

// MaxDriftGate evaluates a FleetDriftReport against user-supplied drift
// budgets. It is intentionally read-only: the report controller owns drift
// observation, while this gate only decides whether current drift is acceptable.
type MaxDriftGate struct {
	Client client.Reader
	Now    func() time.Time
}

func (g *MaxDriftGate) Evaluate(ctx context.Context, req Request) (Result, error) {
	if g.Client == nil {
		return Result{}, fmt.Errorf("max-drift gate client is nil")
	}
	params := maxDriftParams(req)
	reportName := firstNonEmpty(params["reportRef"], params["report"])
	if reportName == "" {
		return Result{
			Phase:      kaprov1alpha2.GatePhaseFailed,
			Reason:     "MissingReportRef",
			Message:    "max-drift gate requires reportRef",
			RetryAfter: defaultMaxDriftRetryAfter,
			Evidence:   []Evidence{maxDriftEvidence("missing FleetDriftReport reference", "", "")},
		}, nil
	}

	var report kaprov1alpha2.FleetDriftReport
	if err := g.Client.Get(ctx, types.NamespacedName{Name: reportName}, &report); err != nil {
		if apierrors.IsNotFound(err) && boolParam(params, "allowMissing") {
			return Result{
				Phase:   kaprov1alpha2.GatePhasePassed,
				Reason:  "ReportMissingAllowed",
				Message: fmt.Sprintf("FleetDriftReport %q not found; allowMissing=true", reportName),
				Evidence: []Evidence{maxDriftEvidence(
					"FleetDriftReport missing but explicitly allowed",
					reportName,
					"allowMissing=true",
				)},
			}, nil
		}
		if apierrors.IsNotFound(err) {
			return Result{
				Phase:      kaprov1alpha2.GatePhaseInconclusive,
				Reason:     "ReportMissing",
				Message:    fmt.Sprintf("FleetDriftReport %q was not found", reportName),
				RetryAfter: defaultMaxDriftRetryAfter,
				Evidence:   []Evidence{maxDriftEvidence("FleetDriftReport missing", reportName, "")},
			}, nil
		}
		return Result{}, fmt.Errorf("get FleetDriftReport %q: %w", reportName, err)
	}

	if result, ok, err := g.evaluateFreshness(params, &report); err != nil || ok {
		return result, err
	}

	summary := report.Status.Summary
	thresholds, err := parseMaxDriftThresholds(params, summary)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha2.GatePhaseFailed,
			Reason:     "InvalidThreshold",
			Message:    err.Error(),
			RetryAfter: defaultMaxDriftRetryAfter,
			Evidence:   []Evidence{maxDriftEvidence(err.Error(), reportName, "")},
		}, nil
	}
	for _, threshold := range thresholds {
		if threshold.observed > threshold.max {
			return Result{
				Phase:      kaprov1alpha2.GatePhaseInconclusive,
				Reason:     "DriftBudgetExceeded",
				Message:    fmt.Sprintf("%s=%d exceeds max %d in FleetDriftReport %q", threshold.name, threshold.observed, threshold.max, reportName),
				RetryAfter: defaultMaxDriftRetryAfter,
				Evidence: []Evidence{maxDriftEvidence(
					fmt.Sprintf("%s exceeded drift budget", threshold.name),
					reportName,
					fmt.Sprintf("%s<=%d", threshold.name, threshold.max),
				)},
			}, nil
		}
	}

	return Result{
		Phase:  kaprov1alpha2.GatePhasePassed,
		Reason: "WithinDriftBudget",
		Message: fmt.Sprintf(
			"FleetDriftReport %q within drift budget: driftedTargets=%d driftedBackendObjects=%d phase=%s",
			reportName,
			summary.DriftedTargets,
			summary.DriftedBackendObjects,
			report.Status.Phase,
		),
		Evidence: []Evidence{maxDriftEvidence("FleetDriftReport within drift budget", reportName, thresholdSummary(thresholds))},
	}, nil
}

func (g *MaxDriftGate) evaluateFreshness(params map[string]string, report *kaprov1alpha2.FleetDriftReport) (Result, bool, error) {
	maxAgeRaw := strings.TrimSpace(params["maxAge"])
	if maxAgeRaw == "" {
		return Result{}, false, nil
	}
	maxAge, err := time.ParseDuration(maxAgeRaw)
	if err != nil || maxAge <= 0 {
		return Result{
			Phase:      kaprov1alpha2.GatePhaseFailed,
			Reason:     "InvalidMaxAge",
			Message:    fmt.Sprintf("maxAge %q must be a positive duration", maxAgeRaw),
			RetryAfter: defaultMaxDriftRetryAfter,
			Evidence:   []Evidence{maxDriftEvidence("invalid maxAge", report.Name, maxAgeRaw)},
		}, true, nil
	}
	if report.Status.ObservedAt == nil {
		return staleReportResult(params, report.Name, "FleetDriftReport has no observedAt"), true, nil
	}
	now := time.Now().UTC()
	if g.Now != nil {
		now = g.Now()
	}
	age := now.Sub(report.Status.ObservedAt.Time)
	if age <= maxAge {
		return Result{}, false, nil
	}
	return staleReportResult(params, report.Name, fmt.Sprintf("FleetDriftReport observation age %s exceeds maxAge %s", age.Round(time.Second), maxAge)), true, nil
}

func staleReportResult(params map[string]string, reportName, message string) Result {
	if boolParam(params, "allowStale") {
		return Result{
			Phase:   kaprov1alpha2.GatePhasePassed,
			Reason:  "StaleReportAllowed",
			Message: message + "; allowStale=true",
			Evidence: []Evidence{maxDriftEvidence(
				"stale FleetDriftReport explicitly allowed",
				reportName,
				"allowStale=true",
			)},
		}
	}
	return Result{
		Phase:      kaprov1alpha2.GatePhaseInconclusive,
		Reason:     "StaleReport",
		Message:    message,
		RetryAfter: defaultMaxDriftRetryAfter,
		Evidence:   []Evidence{maxDriftEvidence("stale FleetDriftReport", reportName, "")},
	}
}

type maxDriftThreshold struct {
	name     string
	observed int32
	max      int32
}

func parseMaxDriftThresholds(params map[string]string, summary kaprov1alpha2.FleetDriftSummary) ([]maxDriftThreshold, error) {
	specs := []struct {
		param    string
		name     string
		observed int32
	}{
		{param: "maxDriftedTargets", name: "driftedTargets", observed: summary.DriftedTargets},
		{param: "maxDriftedBackendObjects", name: "driftedBackendObjects", observed: summary.DriftedBackendObjects},
		{param: "maxPendingTargets", name: "pendingTargets", observed: summary.PendingTargets},
		{param: "maxFailedTargets", name: "failedTargets", observed: summary.FailedTargets},
		{param: "maxUnknownTargets", name: "unknownTargets", observed: summary.UnknownTargets},
	}
	thresholds := make([]maxDriftThreshold, 0, len(specs))
	for _, spec := range specs {
		max, err := int32ParamDefault(params, spec.param, 0)
		if err != nil {
			return nil, err
		}
		thresholds = append(thresholds, maxDriftThreshold{
			name:     spec.name,
			observed: spec.observed,
			max:      max,
		})
	}
	return thresholds, nil
}

func maxDriftParams(req Request) map[string]string {
	out := map[string]string{}
	if req.Template != nil {
		for _, arg := range req.Template.Args {
			if arg.Value != "" {
				out[arg.Name] = arg.Value
			}
		}
	}
	for key, value := range req.Parameters {
		out[key] = value
	}
	return out
}

func maxDriftEvidence(reason, report, threshold string) Evidence {
	return Evidence{
		Type:          "max-drift",
		Provider:      "FleetDriftReport",
		Reason:        reason,
		ObservedValue: report,
		Threshold:     threshold,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boolParam(params map[string]string, key string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(params[key]))
	return value
}

func int32ParamDefault(params map[string]string, key string, fallback int32) (int32, error) {
	raw := strings.TrimSpace(params[key])
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s %q must be a non-negative int32", key, raw)
	}
	return int32(value), nil
}

func thresholdSummary(thresholds []maxDriftThreshold) string {
	parts := make([]string, 0, len(thresholds))
	for _, threshold := range thresholds {
		parts = append(parts, fmt.Sprintf("%s<=%d", threshold.name, threshold.max))
	}
	return strings.Join(parts, ",")
}

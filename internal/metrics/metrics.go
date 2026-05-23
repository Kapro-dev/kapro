// Package metrics registers Kapro-specific Prometheus metrics with the
// controller-runtime registry.  Import this package for its side effect:
//
//	import _ "kapro.io/kapro/internal/metrics"
//
// All metrics are registered once at init time and are safe for concurrent use.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

var (
	// ControllerReconciles counts reconcile invocations by controller and result.
	ControllerReconciles = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "controller",
			Name:      "reconciles_total",
			Help:      "Total controller reconcile invocations by controller and result.",
		},
		[]string{"controller", "result"},
	)

	// ControllerReconcileDuration measures end-to-end reconcile latency.
	ControllerReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "controller",
			Name:      "reconcile_duration_seconds",
			Help:      "Controller reconcile duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"controller"},
	)

	// StatusWrites counts status patch/update attempts by resource and result.
	StatusWrites = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "controller",
			Name:      "status_writes_total",
			Help:      "Total status write operations by resource and result.",
		},
		[]string{"resource", "result"},
	)

	// SyncTransitions counts target-rollout FSM phase transitions.
	// Labels: phase (destination), result (success|failed).
	SyncTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "sync",
			Name:      "transitions_total",
			Help:      "Total FSM phase transitions for target rollouts.",
		},
		[]string{"phase", "result"},
	)

	// FSMUnexpectedTransitions counts FSM phase transitions that a
	// controller attempted but the declared AllowedNext metadata
	// (internal/promotion/fsm) did NOT permit. Shared across every
	// FSM in the operator:
	//
	//   - TargetReconciler.transitionTo (D3 — 10-phase target FSM)
	//   - PromotionRunReconciler.setRunPhase    (D2 — 4-phase run FSM)
	//
	// A non-zero rate here is a strong signal that the FSM graph
	// documentation has drifted from the handler code — alertable.
	// TargetPhase and PromotionRunPhase share string values ("Pending",
	// "Failed") so the {from, to} labels alone don't disambiguate
	// which FSM emitted the count; for alerting that's acceptable, for
	// forensics correlate with the accompanying Warning Event on the
	// owning object.
	FSMUnexpectedTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "fsm",
			Name:      "unexpected_transitions_total",
			Help:      "Phase transitions that violated the declared AllowedNext FSM graph (observability, not enforcement). Shared across PromotionTarget + PromotionRun FSMs.",
		},
		[]string{"from", "to"},
	)

	// GateEvaluations counts gate evaluations across all gate types.
	// Labels: gate_type (cel|job|webhook|soak|metrics|approval|verification),
	//         result (passed|failed|inconclusive|error).
	GateEvaluations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "gate",
			Name:      "evaluations_total",
			Help:      "Total gate evaluations by gate type and result.",
		},
		[]string{"gate_type", "result"},
	)

	// StageDuration measures end-to-end stage duration from Pending to Complete.
	StageDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "stage",
			Name:      "duration_seconds",
			Help:      "Duration in seconds from stage Pending to Complete.",
			Buckets:   prometheus.ExponentialBuckets(30, 2, 10), // 30s → ~8h
		},
		[]string{"plan"},
	)

	// ActivePromotionRuns tracks the current number of non-terminal PromotionRuns.
	ActivePromotionRuns = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "promotionrun",
			Name:      "active_total",
			Help:      "Current number of PromotionRuns not in a terminal phase (Complete/Failed).",
		},
	)

	// PromotionRunPruned counts PromotionRun retention delete outcomes.
	// Labels:
	//   outcome - deleted | not_found | forbidden | error
	PromotionRunPruned = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "promotionrun",
			Name:      "pruned_total",
			Help:      "Total PromotionRun retention prune attempts by outcome.",
		},
		[]string{"outcome"},
	)

	// PromotionRunRetained counts PromotionRuns retained by retention passes.
	PromotionRunRetained = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "promotionrun",
			Name:      "retained_total",
			Help:      "Total PromotionRuns retained by retention controller passes.",
		},
	)

	// WaveProgress tracks how many Targets have been successfully
	// promoted per promotionrun stage.
	WaveProgress = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "wave",
			Name:      "environments_promoted_total",
			Help:      "Number of Targets successfully promoted per promotionrun stage.",
		},
		[]string{"promotionrun", "stage"},
	)

	// SpokeReconciles counts cluster-controller reconcile invocations by result.
	SpokeReconciles = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "spoke",
			Name:      "reconciles_total",
			Help:      "Total spoke reconcile invocations by result.",
		},
		[]string{"result"},
	)

	// SpokeReconcilesSkipped counts reconciles skipped because no spec change was detected.
	SpokeReconcilesSkipped = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "spoke",
			Name:      "reconciles_skipped_total",
			Help:      "Total reconciles skipped due to no spec change.",
		},
	)

	// PluginProbeResults counts plugin capability probes by type, result, and reason.
	PluginProbeResults = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "probe_results_total",
			Help:      "Total plugin capability probe results by plugin type, result, and reason.",
		},
		[]string{"type", "result", "reason"},
	)

	// PluginProbeDuration measures plugin capability probe latency.
	PluginProbeDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "probe_duration_seconds",
			Help:      "Plugin capability probe duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"type", "result"},
	)

	// PluginProbeReady reports the latest readiness observed by the capability prober.
	PluginProbeReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "probe_ready",
			Help:      "Latest plugin capability probe readiness by plugin type and registration name.",
		},
		[]string{"type", "name"},
	)

	// PluginRuntimeCalls counts runtime calls issued through registered plugin adapters.
	PluginRuntimeCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "runtime_calls_total",
			Help:      "Total runtime plugin adapter calls by plugin type, plugin name, method, and result.",
		},
		[]string{"type", "name", "method", "result"},
	)

	// PluginRuntimeCallDuration measures runtime call latency through plugin adapters.
	PluginRuntimeCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "runtime_call_duration_seconds",
			Help:      "Runtime plugin adapter call duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"type", "name", "method", "result"},
	)

	// PluginRuntimeRegistered reports plugin runtime registrations by type.
	PluginRuntimeRegistered = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "plugin",
			Name:      "runtime_registered",
			Help:      "Number of plugin adapters registered at operator startup by plugin type.",
		},
		[]string{"type"},
	)

	// ClusterHeartbeatMisses is the current consecutive-miss count per
	// Cluster. Mirrors status.heartbeat.consecutiveMisses. Resets to 0 on
	// every fresh observation. Compared against per-cluster
	// spec.consecutiveFailureThreshold by the reconciler.
	FleetClusterHeartbeatMisses = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "cluster",
			Name:      "heartbeat_misses",
			Help:      "Current consecutive heartbeat misses per Cluster.",
		},
		[]string{"cluster"},
	)

	// ClusterUnreachableTransitions counts transitions to
	// Ready=False reason=Unreachable. Use this for alerting on cluster
	// outages: rate over 5m > 0 = paging signal.
	FleetClusterUnreachableTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "cluster",
			Name:      "unreachable_transitions_total",
			Help:      "Total transitions to Ready=False reason=Unreachable per Cluster.",
		},
		[]string{"cluster"},
	)

	// ClusterRecoveredTransitions counts transitions out of Unreachable
	// back to Ready=True. Inverse signal to ClusterUnreachableTransitions.
	FleetClusterRecoveredTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "cluster",
			Name:      "recovered_transitions_total",
			Help:      "Total transitions from Unreachable back to Ready=True per Cluster.",
		},
		[]string{"cluster"},
	)

	// LifecycleHookInvocations counts Promotion lifecycle dispatcher
	// invocations.
	// Labels:
	//   kind   - Webhook | Event (per-Promotion spec.lifecycle.handlers)
	//            or Sink         (operator-level CloudEvents sink)
	//   phase  - Promotion.status.phase value at emit time
	//            (Pending|Progressing|Paused|...|Terminating). Same
	//            semantics across all kinds so dashboards can sum or
	//            group by phase uniformly.
	//   result - succeeded | failed (lowercase, Prometheus convention).
	// Retries collapse into a single counted invocation per (kind, phase)
	// tuple; intermediate retry attempts are not counted separately.
	LifecycleHookInvocations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "lifecycle",
			Name:      "hook_invocations_total",
			Help:      "Promotion lifecycle dispatcher invocations by kind (Webhook|Event|Sink), Promotion phase, and result (succeeded|failed).",
		},
		[]string{"kind", "phase", "result"},
	)

	// LifecycleHookDuration is the end-to-end wall-clock duration of one
	// dispatch — initial attempt + retries + backoff — regardless of
	// outcome. The {kind, phase} labels mirror LifecycleHookInvocations.
	LifecycleHookDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "lifecycle",
			Name:      "hook_duration_seconds",
			Help:      "End-to-end duration of a single Promotion lifecycle dispatch (Webhook|Event|Sink) by Promotion phase, including retries and backoff, recorded on success and failure.",
			Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
		[]string{"kind", "phase"},
	)

	// FleetDriftReportTargets reports the latest target-count summary for each
	// FleetDriftReport. The state label is bounded to:
	// total|current|drifted|pending|failed|unknown.
	FleetDriftReportTargets = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "fleetdriftreport",
			Name:      "targets",
			Help:      "Latest FleetDriftReport target counts by report and state.",
		},
		[]string{"report", "state"},
	)

	// FleetDriftReportBackendObjects reports backend-native object evidence
	// counts for each FleetDriftReport. The state label is bounded to:
	// total|drifted.
	FleetDriftReportBackendObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "fleetdriftreport",
			Name:      "backend_objects",
			Help:      "Latest FleetDriftReport backend object counts by report and state.",
		},
		[]string{"report", "state"},
	)

	// FleetDriftReportPhase reports the latest phase for each FleetDriftReport
	// as one-hot gauges over the bounded phase set.
	FleetDriftReportPhase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "fleetdriftreport",
			Name:      "phase",
			Help:      "Latest FleetDriftReport phase as one-hot gauges by report and phase.",
		},
		[]string{"report", "phase"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		ControllerReconciles,
		ControllerReconcileDuration,
		StatusWrites,
		SyncTransitions,
		FSMUnexpectedTransitions,
		GateEvaluations,
		StageDuration,
		ActivePromotionRuns,
		PromotionRunPruned,
		PromotionRunRetained,
		WaveProgress,
		SpokeReconciles,
		SpokeReconcilesSkipped,
		PluginProbeResults,
		PluginProbeDuration,
		PluginProbeReady,
		PluginRuntimeCalls,
		PluginRuntimeCallDuration,
		PluginRuntimeRegistered,
		FleetClusterHeartbeatMisses,
		FleetClusterUnreachableTransitions,
		FleetClusterRecoveredTransitions,
		LifecycleHookInvocations,
		LifecycleHookDuration,
		FleetDriftReportTargets,
		FleetDriftReportBackendObjects,
		FleetDriftReportPhase,
	)
}

// ObserveFleetDriftReport records the latest read-model status for a report.
func ObserveFleetDriftReport(report string, status kaprov1alpha2.FleetDriftReportStatus) {
	if report == "" {
		return
	}
	summary := status.Summary
	targetCounts := map[string]int32{
		"total":   summary.TotalTargets,
		"current": summary.CurrentTargets,
		"drifted": summary.DriftedTargets,
		"pending": summary.PendingTargets,
		"failed":  summary.FailedTargets,
		"unknown": summary.UnknownTargets,
	}
	for state, count := range targetCounts {
		FleetDriftReportTargets.WithLabelValues(report, state).Set(float64(count))
	}
	FleetDriftReportBackendObjects.WithLabelValues(report, "total").Set(float64(summary.TotalBackendObjects))
	FleetDriftReportBackendObjects.WithLabelValues(report, "drifted").Set(float64(summary.DriftedBackendObjects))

	phases := []kaprov1alpha2.FleetDriftReportPhase{
		kaprov1alpha2.FleetDriftReportPhasePending,
		kaprov1alpha2.FleetDriftReportPhaseCurrent,
		kaprov1alpha2.FleetDriftReportPhaseDrifted,
		kaprov1alpha2.FleetDriftReportPhaseUnknown,
		kaprov1alpha2.FleetDriftReportPhaseFailed,
	}
	for _, phase := range phases {
		value := 0.0
		if status.Phase == phase {
			value = 1
		}
		FleetDriftReportPhase.WithLabelValues(report, string(phase)).Set(value)
	}
}

// DeleteFleetDriftReport removes metric series for a deleted report.
func DeleteFleetDriftReport(report string) {
	if report == "" {
		return
	}
	for _, state := range []string{"total", "current", "drifted", "pending", "failed", "unknown"} {
		FleetDriftReportTargets.DeleteLabelValues(report, state)
	}
	for _, state := range []string{"total", "drifted"} {
		FleetDriftReportBackendObjects.DeleteLabelValues(report, state)
	}
	for _, phase := range []kaprov1alpha2.FleetDriftReportPhase{
		kaprov1alpha2.FleetDriftReportPhasePending,
		kaprov1alpha2.FleetDriftReportPhaseCurrent,
		kaprov1alpha2.FleetDriftReportPhaseDrifted,
		kaprov1alpha2.FleetDriftReportPhaseUnknown,
		kaprov1alpha2.FleetDriftReportPhaseFailed,
	} {
		FleetDriftReportPhase.DeleteLabelValues(report, string(phase))
	}
}

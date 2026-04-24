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
		[]string{"pipeline"},
	)

	// ActiveReleases tracks the current number of non-terminal Releases.
	ActiveReleases = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "release",
			Name:      "active_total",
			Help:      "Current number of Releases not in a terminal phase (Complete/Failed).",
		},
	)

	// WaveProgress tracks how many Targets have been successfully
	// promoted per release stage.
	WaveProgress = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "wave",
			Name:      "environments_promoted_total",
			Help:      "Number of Targets successfully promoted per release stage.",
		},
		[]string{"release", "stage"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		ControllerReconciles,
		ControllerReconcileDuration,
		StatusWrites,
		SyncTransitions,
		GateEvaluations,
		StageDuration,
		ActiveReleases,
		WaveProgress,
	)
}

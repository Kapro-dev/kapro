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
	// PromotionTransitions counts FSM phase transitions per Promotion.
	// Labels: phase (destination), result (success|failed).
	PromotionTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "promotion",
			Name:      "transitions_total",
			Help:      "Total FSM phase transitions for Promotions.",
		},
		[]string{"phase", "result"},
	)

	// GateEvaluations counts gate evaluations across all gate types.
	// Labels: gate_type (cel|job|webhook|argo-analysis|soak|metrics|approval|verification),
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

	// BatchDuration measures end-to-end batch duration from Pending to Complete.
	BatchDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "batch",
			Name:      "duration_seconds",
			Help:      "Duration in seconds from batch Pending to Complete.",
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

	// WaveProgress tracks how many Environments have been successfully
	// promoted in the current wave.
	WaveProgress = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kapro",
			Subsystem: "wave",
			Name:      "environments_promoted_total",
			Help:      "Number of Environments successfully promoted per wave batch.",
		},
		[]string{"release", "batch"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		PromotionTransitions,
		GateEvaluations,
		BatchDuration,
		ActiveReleases,
		WaveProgress,
	)
}

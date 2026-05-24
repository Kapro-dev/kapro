package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

var (
	spokeDeliveryReconciles = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "spoke_delivery",
			Name:      "reconciles_total",
			Help:      "Total spoke delivery reconciles by cluster, substrate, phase, and result.",
		},
		[]string{"cluster", "substrate", "phase", "result"},
	)
	spokeDeliveryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kapro",
			Subsystem: "spoke_delivery",
			Name:      "reconcile_duration_seconds",
			Help:      "Spoke delivery reconcile duration by cluster, substrate, phase, and result.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"cluster", "substrate", "phase", "result"},
	)
	spokeDeliveryStagingResults = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kapro",
			Subsystem: "spoke_delivery",
			Name:      "staging_results_total",
			Help:      "Total spoke delivery staging/apply phase outcomes by cluster, substrate, phase, and result.",
		},
		[]string{"cluster", "substrate", "phase", "result"},
	)
)

func init() {
	prometheus.MustRegister(spokeDeliveryReconciles, spokeDeliveryDuration, spokeDeliveryStagingResults)
}

func startMetricsServer(ctx context.Context, addr string) error {
	if metricsDisabled(addr) {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Log.WithName("metrics").Error(err, "metrics server stopped")
		}
	}()
	log.Log.WithName("metrics").Info("metrics server listening", "addr", ln.Addr().String())
	return nil
}

func metricsDisabled(addr string) bool {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case "", "off", "disabled", "none":
		return true
	default:
		return false
	}
}

func observeSpokeDelivery(cluster, substrate string, result spokeprovider.ReconcileResult, duration time.Duration) {
	if cluster == "" {
		cluster = "unknown"
	}
	if substrate == "" {
		substrate = "unknown"
	}
	phase := string(result.Phase)
	if phase == "" {
		phase = "Unknown"
	}
	outcome := "pending"
	if result.Err != nil || result.Phase == kaprov1alpha1.DeliveryPhaseFailed {
		outcome = "error"
	} else if result.Phase == kaprov1alpha1.DeliveryPhaseConverged {
		outcome = "success"
	} else if result.Phase == kaprov1alpha1.DeliveryPhaseSkipped {
		outcome = "skipped"
	}
	spokeDeliveryReconciles.WithLabelValues(cluster, substrate, phase, outcome).Inc()
	spokeDeliveryDuration.WithLabelValues(cluster, substrate, phase, outcome).Observe(duration.Seconds())
	observeSpokeDeliveryStaging(cluster, substrate, result.Staging)
}

func observeSpokeDeliveryStaging(cluster, substrate string, staging *kaprov1alpha1.DeliveryStagingStatus) {
	if staging == nil {
		return
	}
	switch staging.FailurePhase {
	case kaprov1alpha1.DeliveryPhaseStaging:
		spokeDeliveryStagingResults.WithLabelValues(cluster, substrate, string(kaprov1alpha1.DeliveryPhaseStaging), "error").Inc()
	case kaprov1alpha1.DeliveryPhaseApplying:
		spokeDeliveryStagingResults.WithLabelValues(cluster, substrate, string(kaprov1alpha1.DeliveryPhaseStaging), "success").Inc()
		spokeDeliveryStagingResults.WithLabelValues(cluster, substrate, string(kaprov1alpha1.DeliveryPhaseApplying), "error").Inc()
	case "":
		if staging.StagedObjects > 0 || staging.CommittedObjects > 0 {
			spokeDeliveryStagingResults.WithLabelValues(cluster, substrate, string(kaprov1alpha1.DeliveryPhaseStaging), "success").Inc()
			spokeDeliveryStagingResults.WithLabelValues(cluster, substrate, string(kaprov1alpha1.DeliveryPhaseApplying), "success").Inc()
		}
	}
}

func deliverySubstrateMetricLabel(profile *kaprov1alpha1.Substrate) string {
	if profile != nil && profile.Spec.SubstrateKind() != "" {
		return profile.Spec.SubstrateKind()
	}
	return "unknown"
}

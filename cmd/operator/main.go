package main

import (
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/internal/gate"
	crdprovider "kapro.io/kapro/internal/provider/crd"
	"kapro.io/kapro/internal/registration"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("kapro-operator")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		LeaderElection:          true,
		LeaderElectionID:        "kapro-operator-leader.kapro.io",
		LeaderElectionNamespace: "kapro-system",
		HealthProbeBindAddress:  ":8081",
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("kapro-operator")

	// Shared actuator + provider — both use the control-plane client.
	actuator := &fluxactuator.FluxActuator{Client: mgr.GetClient()}
	provider := &crdprovider.CRDProvider{Client: mgr.GetClient()}

	// Gate engine — injected into PromotionReconciler.
	approvalGate := &gate.ApprovalGate{Client: mgr.GetClient()}

	if err := (&controller.ReleaseReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create Release controller")
		os.Exit(1)
	}

	if err := (&controller.PromotionReconciler{
		Client:       mgr.GetClient(),
		Recorder:     recorder,
		Actuator:     actuator,
		Provider:     provider,
		SoakGate:     &gate.SoakGate{},
		MetricsGate:  &gate.MetricsGate{},
		ApprovalGate: approvalGate,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create Promotion controller")
		os.Exit(1)
	}

	if err := (&controller.BatchRunReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
		Actuator: actuator,
		Provider: provider,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create BatchRun controller")
		os.Exit(1)
	}

	if err := (&controller.ApprovalReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create Approval controller")
		os.Exit(1)
	}

	if err := (&controller.BootstrapTokenReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create BootstrapToken controller")
		os.Exit(1)
	}

	// Start registration HTTP server — handles /register endpoint for cluster self-registration.
	regServer := &registration.Server{Client: mgr.GetClient()}
	httpServer := &http.Server{
		Addr:         ":9090",
		Handler:      regServer,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "registration server failed")
			os.Exit(1)
		}
	}()
	log.Info("registration server started", "addr", ":9090")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

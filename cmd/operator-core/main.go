// operator-core is a lean Kapro operator binary — zero cosign/ORAS/sigstore deps.
// Suitable for air-gapped or resource-constrained environments.
//
// vs full operator:
//   all controllers via CCM-style registry
//   core gates only (soak, metrics, approval) — no keda/mlflow/shadow/kgateway built-in
//   no cosign VerificationGate, no ORAS OCIService
//   non-core gates arrive via PluginRegistration CRDs
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	internalgate "kapro.io/kapro/internal/gate"
	_ "kapro.io/kapro/internal/metrics" // register custom Prometheus metrics at init
	enginenotifier "kapro.io/kapro/internal/notification/engine"
	"kapro.io/kapro/internal/webhook"
	"kapro.io/kapro/pkg/actuator"
	cm "kapro.io/kapro/pkg/controllermanager"

	_ "kapro.io/kapro/internal/plugin/bridge"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("kapro-operator-core")

	controllersFlag := os.Getenv("KAPRO_CONTROLLERS")
	if controllersFlag == "" {
		controllersFlag = "*"
	}
	selected := cm.ParseControllerNames(controllersFlag)
	log.Info("controller selection", "controllers", controllersFlag)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                true,
		LeaderElectionID:              "kapro-operator-core-leader.kapro.io",
		LeaderElectionNamespace:       "kapro-system",
		LeaderElectionReleaseOnCancel: true,
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
		HealthProbeBindAddress: ":8081",
		Controller: ctrlcfg.Controller{
			RecoverPanic:            ptr.To(true),
			MaxConcurrentReconciles: 5,
		},
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

	recorder := mgr.GetEventRecorderFor("kapro-operator-core")

	// Core gate set — no heavy deps.
	gates := cm.BuildCoreGateSet()
	gates.Approval = &internalgate.ApprovalGate{Client: mgr.GetClient()}

	// Build actuator registry — core binary only ships flux.
	actuatorReg := actuator.NewRegistry()
	if err := actuatorReg.Register("flux", &fluxactuator.FluxActuator{Client: mgr.GetClient()}); err != nil {
		log.Error(err, "failed to register flux actuator")
		os.Exit(1)
	}

	cc := cm.ControllerContext{
		Manager:          mgr,
		Recorder:         recorder,
		ActuatorRegistry: actuatorReg,
		Gates:            gates,
		Notifier: &enginenotifier.Notifier{
			SecretName: "kapro-notifications-secret",
			Namespace:  "kapro-system",
			Client:     mgr.GetClient(),
		},
		// HealthAssessor, OCIService, VerificationGate intentionally nil (pass-through).
		ApprovalSecret: []byte(os.Getenv("KAPRO_APPROVAL_SECRET")),
		ExternalURL:    os.Getenv("KAPRO_EXTERNAL_URL"),
	}

	ctx := context.Background()
	for name, initFn := range cm.Registry {
		if !selected[name] {
			log.Info("controller disabled", "name", name)
			continue
		}
		enabled, err := initFn(ctx, cc)
		if err != nil {
			log.Error(err, "unable to start controller", "name", name)
			os.Exit(1)
		}
		if enabled {
			log.Info("controller started", "name", name)
		}
	}

	approvalAddr := os.Getenv("KAPRO_APPROVAL_ADDR")
	if approvalAddr == "" {
		approvalAddr = ":8091"
	}
	approvalServer := &http.Server{
		Addr:         approvalAddr,
		Handler:      (&webhook.Server{Client: mgr.GetClient(), TokenSecret: []byte(os.Getenv("KAPRO_APPROVAL_SECRET"))}).Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		if err := approvalServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "approval webhook server failed")
		}
	}()
	log.Info("approval webhook server started", "addr", approvalAddr)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

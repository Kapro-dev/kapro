package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/internal/gate"
	crdprovider "kapro.io/kapro/internal/provider/crd"
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
		Scheme: scheme,
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Shared actuator + provider — both use the control-plane client.
	actuator := &fluxactuator.FluxActuator{Client: mgr.GetClient()}
	provider := &crdprovider.CRDProvider{Client: mgr.GetClient()}

	// Gate engine — injected into PromotionReconciler.
	approvalGate := &gate.ApprovalGate{Client: mgr.GetClient()}

	if err := (&controller.ReleaseReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create Release controller")
		os.Exit(1)
	}

	if err := (&controller.PromotionReconciler{
		Client:       mgr.GetClient(),
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
		Actuator: actuator,
		Provider: provider,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create BatchRun controller")
		os.Exit(1)
	}

	if err := (&controller.ApprovalReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create Approval controller")
		os.Exit(1)
	}

	if err := (&controller.BootstrapTokenReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create BootstrapToken controller")
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

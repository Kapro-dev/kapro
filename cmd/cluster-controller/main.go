package main

import (
	"context"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("kapro-cluster-controller")

	environmentRef := os.Getenv("KAPRO_ENVIRONMENT_REF")
	if environmentRef == "" {
		log.Error(nil, "KAPRO_ENVIRONMENT_REF env var required")
		os.Exit(1)
	}

	controlPlaneURL := os.Getenv("KAPRO_CONTROL_PLANE_URL")
	if controlPlaneURL == "" {
		log.Error(nil, "KAPRO_CONTROL_PLANE_URL env var required")
		os.Exit(1)
	}

	log.Info("starting kapro-cluster-controller",
		"environment", environmentRef,
		"controlPlane", controlPlaneURL,
	)

	// Local cluster manager — read-only, watches Flux status
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// TODO: start ClusterRegistration status writer
	// - watches local flux-system Kustomization + HelmRelease
	// - writes ClusterRegistration.status to control plane every 30s
	_ = context.Background()
	_ = time.Second

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

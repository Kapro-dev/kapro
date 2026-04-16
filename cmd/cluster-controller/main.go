package main

import (
	"context"
	"fmt"
	"os"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
	_ = kustomizev1.AddToScheme(scheme)
	_ = helmv2.AddToScheme(scheme)
	_ = sourcev1.AddToScheme(scheme)
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

	fluxNamespace := os.Getenv("KAPRO_FLUX_NAMESPACE")
	if fluxNamespace == "" {
		fluxNamespace = "flux-system"
	}

	log.Info("starting kapro-cluster-controller",
		"environment", environmentRef,
		"controlPlane", controlPlaneURL,
		"fluxNamespace", fluxNamespace,
	)

	// Local cluster client (reads Flux status)
	localCfg := ctrl.GetConfigOrDie()
	localClient, err := client.New(localCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create local cluster client")
		os.Exit(1)
	}

	// Control plane client (writes ClusterRegistration status)
	// Uses the SA token projected into the pod by the control plane's ClusterRegistration secret.
	// For now: uses same kubeconfig (single-cluster dev mode).
	// In multi-cluster: override with KAPRO_CONTROL_PLANE_KUBECONFIG env var.
	controlPlaneCfg := ctrl.GetConfigOrDie()
	cpClient, err := client.New(controlPlaneCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create control plane client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// Heartbeat loop — writes ClusterRegistration.status every 30s
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// First tick immediately
	if err := writeHeartbeat(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
		log.Error(err, "initial heartbeat failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			if err := writeHeartbeat(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
				log.Error(err, "heartbeat write failed")
			}
		}
	}
}

// writeHeartbeat reads local Flux status and writes ClusterRegistration.status to the control plane.
func writeHeartbeat(
	ctx context.Context,
	localClient client.Client,
	cpClient client.Client,
	environmentRef string,
	fluxNamespace string,
) error {
	log := ctrl.Log.WithName("heartbeat")

	// Read Flux kustomizations in flux-system namespace
	var ksList kustomizev1.KustomizationList
	_ = localClient.List(ctx, &ksList, client.InNamespace(fluxNamespace))

	fluxReady := true
	for _, ks := range ksList.Items {
		for _, cond := range ks.Status.Conditions {
			if cond.Type == "Ready" && cond.Status != metav1.ConditionTrue {
				fluxReady = false
				break
			}
		}
	}

	// Read Flux source-controller OCIRepository to get current deployed tag
	var ociRepo sourcev1.OCIRepository
	currentVersion := ""
	if err := localClient.Get(ctx, types.NamespacedName{
		Name:      environmentRef,
		Namespace: fluxNamespace,
	}, &ociRepo); err == nil {
		if ociRepo.Spec.Reference != nil {
			currentVersion = ociRepo.Spec.Reference.Tag
		}
	}

	// Derive cluster phase from Flux readiness
	phase := kaprov1alpha1.ClusterPhaseConverged
	if !fluxReady {
		phase = kaprov1alpha1.ClusterPhaseConverging
	}

	// Find or create ClusterRegistration on control plane
	var reg kaprov1alpha1.ClusterRegistration
	err := cpClient.Get(ctx, types.NamespacedName{Name: environmentRef}, &reg)
	if err != nil {
		// Create it
		reg = kaprov1alpha1.ClusterRegistration{
			ObjectMeta: metav1.ObjectMeta{
				Name: environmentRef,
				Labels: map[string]string{
					"kapro.io/environment": environmentRef,
				},
			},
			Spec: kaprov1alpha1.ClusterRegistrationSpec{
				EnvironmentRef: environmentRef,
			},
		}
		if createErr := cpClient.Create(ctx, &reg); createErr != nil {
			return fmt.Errorf("create ClusterRegistration: %w", createErr)
		}
		log.Info("created ClusterRegistration", "name", environmentRef)
	}

	// Patch status
	patch := client.MergeFrom(reg.DeepCopy())
	reg.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	reg.Status.Healthy = fluxReady
	reg.Status.FluxReady = fluxReady
	reg.Status.CurrentVersion = currentVersion
	reg.Status.Phase = phase

	if patchErr := cpClient.Status().Patch(ctx, &reg, patch); patchErr != nil {
		return fmt.Errorf("patch ClusterRegistration status: %w", patchErr)
	}

	log.Info("heartbeat written",
		"env", environmentRef,
		"phase", phase,
		"version", currentVersion,
		"fluxReady", fluxReady,
	)
	return nil
}

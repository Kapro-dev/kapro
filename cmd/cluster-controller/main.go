package main

import (
	"context"
	"fmt"
	"os"
	"time"

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

	// localClient reads Flux Kustomizations + OCIRepositories on this cluster.
	localCfg := ctrl.GetConfigOrDie()
	localClient, err := client.New(localCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create local cluster client")
		os.Exit(1)
	}

	// cpClient writes ClusterRegistration.status to the control plane.
	// In single-cluster dev mode this is the same kubeconfig.
	// In production: mount a kubeconfig Secret for the control plane.
	controlPlaneCfg := ctrl.GetConfigOrDie()
	cpClient, err := client.New(controlPlaneCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create control plane client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run once immediately on startup.
	if err := reconcile(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
		log.Error(err, "initial reconcile failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			if err := reconcile(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
				log.Error(err, "reconcile failed")
			}
		}
	}
}

// reconcile is the main loop tick:
//  1. Read ClusterRegistration from control plane.
//  2. If spec.desiredVersion has changed → patch local OCIRepository tag.
//  3. Read local Flux status → derive cluster phase.
//  4. Write status back to control plane.
func reconcile(
	ctx context.Context,
	localClient client.Client,
	cpClient client.Client,
	environmentRef string,
	fluxNamespace string,
) error {
	log := ctrl.Log.WithName("reconcile").WithValues("env", environmentRef)

	// ── 1. Ensure ClusterRegistration exists on control plane ──────────────────
	var reg kaprov1alpha1.ClusterRegistration
	if err := cpClient.Get(ctx, types.NamespacedName{Name: environmentRef}, &reg); err != nil {
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
		log.Info("created ClusterRegistration")
	}

	// ── 2. Read the Environment to get the OCIRepository name ──────────────────
	var env kaprov1alpha1.Environment
	ociRepoName := environmentRef // fallback: same name as environment
	if err := cpClient.Get(ctx, types.NamespacedName{Name: environmentRef}, &env); err == nil {
		if env.Spec.Actuator.Flux != nil && env.Spec.Actuator.Flux.OCIRepository != "" {
			ociRepoName = env.Spec.Actuator.Flux.OCIRepository
		}
	}

	// ── 3. Read local OCIRepository for current tag ────────────────────────────
	var ociRepo sourcev1.OCIRepository
	currentTag := ""
	ociFound := false
	if err := localClient.Get(ctx, types.NamespacedName{
		Name:      ociRepoName,
		Namespace: fluxNamespace,
	}, &ociRepo); err == nil {
		ociFound = true
		if ociRepo.Spec.Reference != nil {
			currentTag = ociRepo.Spec.Reference.Tag
		}
	}

	// ── 4. Apply desired version if it has changed ─────────────────────────────
	desiredVersion := reg.Spec.DesiredVersion
	if ociFound && desiredVersion != "" && desiredVersion != currentTag {
		log.Info("desired version differs from current tag — patching OCIRepository",
			"ociRepo", ociRepoName,
			"current", currentTag,
			"desired", desiredVersion,
		)
		if err := patchOCIRepositoryTag(ctx, localClient, &ociRepo, fluxNamespace, desiredVersion); err != nil {
			return fmt.Errorf("patch OCIRepository tag: %w", err)
		}
		// Re-read after patch so phase calculation uses the new tag.
		_ = localClient.Get(ctx, types.NamespacedName{Name: ociRepoName, Namespace: fluxNamespace}, &ociRepo)
		if ociRepo.Spec.Reference != nil {
			currentTag = ociRepo.Spec.Reference.Tag
		}
	}

	// ── 5. Read Flux Kustomization status ──────────────────────────────────────
	var ksList kustomizev1.KustomizationList
	_ = localClient.List(ctx, &ksList, client.InNamespace(fluxNamespace))

	fluxReady := true
	fluxVersion := ""
	for _, ks := range ksList.Items {
		for _, cond := range ks.Status.Conditions {
			if cond.Type == "Ready" && cond.Status != metav1.ConditionTrue {
				fluxReady = false
				break
			}
		}
		// Use revision from the first kustomization as proxy for deployed version.
		if fluxVersion == "" && ks.Status.LastAppliedRevision != "" {
			fluxVersion = ks.Status.LastAppliedRevision
		}
	}

	// ── 6. Derive cluster phase ────────────────────────────────────────────────
	phase := derivePhase(fluxReady, currentTag, desiredVersion)

	// ── 7. Write status to control plane ──────────────────────────────────────
	patch := client.MergeFrom(reg.DeepCopy())
	reg.Status.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	reg.Status.Healthy = fluxReady
	reg.Status.FluxReady = fluxReady
	reg.Status.FluxVersion = fluxVersion
	reg.Status.CurrentVersion = currentTag
	reg.Status.Phase = phase

	if patchErr := cpClient.Status().Patch(ctx, &reg, patch); patchErr != nil {
		return fmt.Errorf("patch ClusterRegistration status: %w", patchErr)
	}

	log.Info("heartbeat written",
		"phase", phase,
		"currentVersion", currentTag,
		"desiredVersion", desiredVersion,
		"fluxReady", fluxReady,
	)
	return nil
}

// patchOCIRepositoryTag sets OCIRepository.spec.reference.tag and annotates
// with reconcile.fluxcd.io/requestedAt to force an immediate Flux reconciliation.
func patchOCIRepositoryTag(
	ctx context.Context,
	localClient client.Client,
	ociRepo *sourcev1.OCIRepository,
	_ string,
	tag string,
) error {
	patch := client.MergeFrom(ociRepo.DeepCopy())

	if ociRepo.Spec.Reference == nil {
		ociRepo.Spec.Reference = &sourcev1.OCIRepositoryRef{}
	}
	ociRepo.Spec.Reference.Tag = tag

	// Force Flux to reconcile immediately instead of waiting for interval.
	if ociRepo.Annotations == nil {
		ociRepo.Annotations = map[string]string{}
	}
	ociRepo.Annotations["reconcile.fluxcd.io/requestedAt"] = time.Now().UTC().Format(time.RFC3339Nano)

	return localClient.Patch(ctx, ociRepo, patch)
}

// derivePhase maps Flux readiness + version state to a ClusterPhase.
func derivePhase(fluxReady bool, currentTag, desiredVersion string) kaprov1alpha1.ClusterPhase {
	if desiredVersion != "" && currentTag != desiredVersion {
		// We know the desired tag, but it hasn't propagated yet.
		return kaprov1alpha1.ClusterPhaseApplying
	}
	if !fluxReady {
		return kaprov1alpha1.ClusterPhaseConverging
	}
	if desiredVersion != "" && currentTag == desiredVersion {
		return kaprov1alpha1.ClusterPhaseConverged
	}
	return kaprov1alpha1.ClusterPhaseConverged
}


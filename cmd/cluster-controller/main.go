package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/bootstrap"
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

	targetName := os.Getenv("KAPRO_TARGET")
	if targetName == "" {
		log.Error(nil, "KAPRO_TARGET env var required")
		os.Exit(1)
	}

	hubURL := os.Getenv("KAPRO_CONTROL_PLANE_URL")
	if hubURL == "" {
		log.Error(nil, "KAPRO_CONTROL_PLANE_URL env var required")
		os.Exit(1)
	}

	fluxNamespace := os.Getenv("KAPRO_FLUX_NAMESPACE")
	if fluxNamespace == "" {
		fluxNamespace = "flux-system"
	}

	hubCAData := decodeCABundle(os.Getenv("KAPRO_CONTROL_PLANE_CA_BUNDLE"))

	log.Info("starting kapro-cluster-controller",
		"target", targetName,
		"controlPlane", hubURL,
		"fluxNamespace", fluxNamespace,
	)

	localCfg := ctrl.GetConfigOrDie()
	localClient, err := client.New(localCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create local cluster client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// Select provider via KAPRO_PROVIDER env var.
	var provider bootstrap.Provider
	switch strings.ToLower(os.Getenv("KAPRO_PROVIDER")) {
	case "gcp":
		provider = bootstrap.NewGCP(hubURL, hubCAData)
		log.Info("using GCP Workload Identity provider")
	default:
		provider = bootstrap.NewGeneric(localClient, hubURL, hubCAData, targetName)
		log.Info("using generic CSR bootstrap provider")
	}

	hubCfg, err := provider.HubConfig(ctx)
	if err != nil {
		log.Error(err, "failed to bootstrap hub credentials")
		os.Exit(1)
	}
	hubClient, err := client.New(hubCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "failed to create hub client")
		os.Exit(1)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Check every 5 minutes — GCP tokens expire in ~1 hour, so this gives enough
	// lead time; Generic certs renew when <30 days remain, so 5-minute checks are fine too.
	renewTicker := time.NewTicker(5 * time.Minute)
	defer renewTicker.Stop()

	// renewResultCh receives a rebuilt hub client after background credential renewal.
	// Buffered so the renewal goroutine never blocks if main loop is busy.
	renewResultCh := make(chan client.Client, 1)

	if err := reconcile(ctx, localClient, hubClient, targetName, fluxNamespace); err != nil {
		log.Error(err, "initial reconcile failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case newHub := <-renewResultCh:
			hubClient = newHub
			log.Info("credentials renewed and hub client updated", "provider", provider.Name())
		case <-renewTicker.C:
			if provider.NeedsRenewal() {
				log.Info("credentials approaching expiry — renewing in background", "provider", provider.Name())
				go func() {
					hubCfg, renewErr := provider.HubConfig(ctx)
					if renewErr != nil {
						log.Error(renewErr, "credential renewal failed — will retry next tick")
						return
					}
					newHub, clientErr := client.New(hubCfg, client.Options{Scheme: scheme})
					if clientErr != nil {
						log.Error(clientErr, "failed to rebuild hub client after renewal")
						return
					}
					renewResultCh <- newHub
				}()
			}
		case <-ticker.C:
			if err := reconcile(ctx, localClient, hubClient, targetName, fluxNamespace); err != nil {
				log.Error(err, "reconcile failed")
			}
		}
	}
}

// decodeCABundle accepts either a raw PEM string or a base64-encoded PEM string.
func decodeCABundle(caBundle string) []byte {
	if caBundle == "" {
		return nil
	}
	if strings.HasPrefix(caBundle, "-----") {
		return []byte(caBundle)
	}
	decoded, err := base64.StdEncoding.DecodeString(caBundle)
	if err != nil {
		return []byte(caBundle)
	}
	return decoded
}

// reconcile is the main heartbeat tick:
//  1. GET MemberCluster from hub to read desired state.
//  2. Read local Flux state (OCIRepository + Kustomization status).
//  3. PATCH MemberCluster/status on hub with current state.
//  4. Apply desired version to local OCIRepository if it has changed.
func reconcile(
	ctx context.Context,
	localClient, hubClient client.Client,
	targetName, fluxNamespace string,
) error {
	log := ctrl.Log.WithName("reconcile").WithValues("cluster", targetName)

	// 1. GET MemberCluster from hub.
	// Retry on NotFound: covers the race where the first heartbeat fires before
	// CSRApprovalReconciler has approved the cluster bootstrap (OCM klusterlet pattern).
	var mc kaprov1alpha1.MemberCluster
	for attempt := 1; attempt <= 3; attempt++ {
		err := hubClient.Get(ctx, types.NamespacedName{Name: targetName}, &mc)
		if err == nil {
			break
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get MemberCluster %q from hub: %w", targetName, err)
		}
		if attempt == 3 {
			return fmt.Errorf("get MemberCluster %q from hub: not found after %d attempts", targetName, attempt)
		}
		log.Info("MemberCluster not yet visible on hub, retrying", "attempt", attempt)
		time.Sleep(5 * time.Second)
	}

	desiredVersion := mc.Spec.DesiredVersion
	appKey := mc.Spec.DesiredAppKey
	if appKey == "" {
		appKey = "default"
	}

	// 2. Read local OCIRepository (name = cluster name).
	ociRepoName := targetName

	var ociRepo sourcev1.OCIRepository
	currentRef := ""
	ociFound := false
	if err := localClient.Get(ctx, types.NamespacedName{Name: ociRepoName, Namespace: fluxNamespace}, &ociRepo); err == nil {
		ociFound = true
		if ociRepo.Spec.Reference != nil {
			if ociRepo.Spec.Reference.Digest != "" {
				currentRef = ociRepo.Spec.Reference.Digest
			} else {
				currentRef = ociRepo.Spec.Reference.Tag
			}
		}
	}

	// 3. Read Flux Kustomization health.
	var ksList kustomizev1.KustomizationList
	_ = localClient.List(ctx, &ksList, client.InNamespace(fluxNamespace))

	fluxReady := true
	fluxVersion := ""
	readyCount, failedCount, totalCount := 0, 0, 0
	for _, ks := range ksList.Items {
		totalCount++
		isReady := true
		for _, cond := range ks.Status.Conditions {
			if cond.Type == "Ready" && cond.Status != metav1.ConditionTrue {
				fluxReady = false
				isReady = false
				break
			}
		}
		if isReady {
			readyCount++
		} else {
			failedCount++
		}
		if fluxVersion == "" && ks.Status.LastAppliedRevision != "" {
			fluxVersion = ks.Status.LastAppliedRevision
		}
	}

	// 4. PATCH ManagedCluster/status on hub.
	phase := derivePhase(fluxReady, currentRef, desiredVersion)
	statusPatch := client.MergeFrom(mc.DeepCopy())
	mc.Status.LastHeartbeat = metav1.Now().UTC().Format(time.RFC3339)
	mc.Status.Phase = phase
	mc.Status.ObservedGeneration = mc.Generation
	mc.Status.DeliverySystem = "flux"
	if mc.Status.CurrentVersions == nil {
		mc.Status.CurrentVersions = map[string]string{}
	}
	mc.Status.CurrentVersions[appKey] = currentRef
	mc.Status.Health = kaprov1alpha1.ClusterHealth{
		AllWorkloadsReady: fluxReady,
		ReadyWorkloads:    readyCount,
		FailedWorkloads:   failedCount,
		TotalWorkloads:    totalCount,
		Message:           fmt.Sprintf("FluxVersion=%s", fluxVersion),
	}
	setMemberClusterReadyCondition(&mc, phase, fluxReady, currentRef, desiredVersion)

	if err := hubClient.Status().Patch(ctx, &mc, statusPatch); err != nil {
		return fmt.Errorf("patch MemberCluster status: %w", err)
	}

	// 5. Apply desired version if it has changed.
	if ociFound && desiredVersion != "" && desiredVersion != currentRef {
		log.Info("desired version differs from current ref — patching OCIRepository",
			"ociRepo", ociRepoName,
			"current", currentRef,
			"desired", desiredVersion,
		)
		if err := patchOCIRepositoryTag(ctx, localClient, &ociRepo, desiredVersion); err != nil {
			return fmt.Errorf("patch OCIRepository %q: %w", ociRepoName, err)
		}
	}

	log.Info("heartbeat written",
		"phase", phase,
		"appKey", appKey,
		"currentVersion", currentRef,
		"desiredVersion", desiredVersion,
		"fluxReady", fluxReady,
	)
	return nil
}

// patchOCIRepositoryTag sets the OCIRepository reference and forces an immediate Flux reconciliation.
// If version contains "@sha256:", it is treated as a digest reference.
func patchOCIRepositoryTag(
	ctx context.Context,
	localClient client.Client,
	ociRepo *sourcev1.OCIRepository,
	version string,
) error {
	patch := client.MergeFrom(ociRepo.DeepCopy())
	if ociRepo.Spec.Reference == nil {
		ociRepo.Spec.Reference = &sourcev1.OCIRepositoryRef{}
	}
	if idx := strings.Index(version, "@sha256:"); idx != -1 {
		ociRepo.Spec.Reference.Digest = version[idx+1:] // "sha256:..."
		ociRepo.Spec.Reference.Tag = ""
	} else {
		ociRepo.Spec.Reference.Tag = version
		ociRepo.Spec.Reference.Digest = ""
	}
	if ociRepo.Annotations == nil {
		ociRepo.Annotations = map[string]string{}
	}
	ociRepo.Annotations["reconcile.fluxcd.io/requestedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	return localClient.Patch(ctx, ociRepo, patch)
}

// derivePhase maps Flux readiness + version state to a ClusterPhase.
func derivePhase(fluxReady bool, currentRef, desiredVersion string) kaprov1alpha1.ClusterPhase {
	if desiredVersion != "" && currentRef != desiredVersion {
		return kaprov1alpha1.ClusterPhaseApplying
	}
	if !fluxReady {
		return kaprov1alpha1.ClusterPhaseConverging
	}
	return kaprov1alpha1.ClusterPhaseConverged
}

func setMemberClusterReadyCondition(mc *kaprov1alpha1.MemberCluster, phase kaprov1alpha1.ClusterPhase, fluxReady bool, currentRef, desiredVersion string) {
	status := metav1.ConditionFalse
	reason := string(phase)
	message := "cluster is progressing"

	switch phase {
	case kaprov1alpha1.ClusterPhaseConverged:
		status = metav1.ConditionTrue
		reason = "Converged"
		message = fmt.Sprintf("cluster converged at version %s", currentRef)
	case kaprov1alpha1.ClusterPhaseApplying:
		message = fmt.Sprintf("applying desired version %s", desiredVersion)
	case kaprov1alpha1.ClusterPhaseConverging:
		if !fluxReady {
			message = "delivery system reports workloads are not yet ready"
		}
	case kaprov1alpha1.ClusterPhaseFailed:
		reason = "Failed"
		message = "cluster reported a failed rollout"
	case kaprov1alpha1.ClusterPhaseUnreachable:
		reason = "Unreachable"
		message = "cluster heartbeat is stale or unreachable"
	}

	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mc.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

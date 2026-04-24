package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
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
	kaprometrics "kapro.io/kapro/internal/metrics"
)

var scheme = runtime.NewScheme()

const heartbeatStatusWriteInterval = time.Minute

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

	// Select bootstrap mode via KAPRO_BOOTSTRAP_MODE env var.
	var provider bootstrap.Provider
	switch strings.ToLower(os.Getenv("KAPRO_BOOTSTRAP_MODE")) {
	case "gcp":
		provider = bootstrap.NewGCP(hubURL, hubCAData)
		log.Info("using GCP Workload Identity bootstrap mode")
	default:
		provider = bootstrap.NewGeneric(localClient, hubURL, hubCAData, targetName)
		log.Info("using generic CSR bootstrap mode")
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
					// Non-blocking send: discard result if channel is full or ctx
					// is done so the goroutine never leaks on shutdown.
					select {
					case renewResultCh <- newHub:
					case <-ctx.Done():
					}
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
	// Bound all API calls and retry sleeps to 25 s so a network partition never
	// causes this goroutine to accumulate across ticks.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

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
		// Cancellable sleep so SIGTERM / deadline unblocks immediately.
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	desiredVersion := mc.Spec.DesiredVersion
	appKey := mc.Spec.DesiredAppKey
	if appKey == "" {
		appKey = "default"
	}

	// Build effective desired versions map: merge legacy single-version with new multi-version.
	effectiveDesiredVersions := make(map[string]string)
	if desiredVersion != "" {
		effectiveDesiredVersions[appKey] = desiredVersion
	}
	for k, v := range mc.Spec.DesiredVersions {
		effectiveDesiredVersions[k] = v // multi-version map takes precedence
	}

	// 3. Read Flux Kustomization health.
	var ksList kustomizev1.KustomizationList
	if err := localClient.List(ctx, &ksList, client.InNamespace(fluxNamespace)); err != nil {
		return fmt.Errorf("list Kustomizations from spoke: %w", err)
	}

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

	currentVersions := copyStringMap(mc.Status.CurrentVersions)
	if currentVersions == nil {
		currentVersions = make(map[string]string)
	}
	desiredRepoStates := make([]desiredRepoState, 0, len(effectiveDesiredVersions))
	for desiredAppKey := range effectiveDesiredVersions {
		repoName, err := resolveOCIRepositoryName(&mc, targetName, desiredAppKey, len(effectiveDesiredVersions) > 1)
		if err != nil {
			desiredRepoStates = append(desiredRepoStates, desiredRepoState{
				appKey:  desiredAppKey,
				repoErr: err,
			})
			continue
		}
		currentRef, found, err := readOCIRepositoryRef(ctx, localClient, fluxNamespace, repoName)
		if err != nil {
			return fmt.Errorf("get OCIRepository %q from spoke: %w", repoName, err)
		}
		if found {
			currentVersions[desiredAppKey] = currentRef
		}
		desiredRepoStates = append(desiredRepoStates, desiredRepoState{
			appKey:     desiredAppKey,
			repoName:   repoName,
			currentRef: currentRef,
			found:      found,
		})
	}

	defaultCurrentRef := currentVersions[appKey]
	if defaultCurrentRef == "" {
		repoName, err := resolveOCIRepositoryName(&mc, targetName, appKey, false)
		if err == nil {
			currentRef, found, err := readOCIRepositoryRef(ctx, localClient, fluxNamespace, repoName)
			if err != nil {
				return fmt.Errorf("get OCIRepository %q from spoke: %w", repoName, err)
			}
			if found {
				defaultCurrentRef = currentRef
				currentVersions[appKey] = currentRef
			}
		}
	}

	// 4. PATCH ManagedCluster/status on hub.
	phase, phaseMessage := derivePhaseFromDesiredVersions(fluxReady, desiredVersion, effectiveDesiredVersions, currentVersions, desiredRepoStates)
	original := mc.DeepCopy()
	statusPatch := client.MergeFrom(original)
	mc.Status.LastHeartbeat = metav1.Now().UTC().Format(time.RFC3339)
	mc.Status.Phase = phase
	mc.Status.ObservedGeneration = mc.Generation
	mc.Status.DeliverySystem = "flux"
	mc.Status.CurrentVersions = currentVersions
	mc.Status.Health = kaprov1alpha1.ClusterHealth{
		AllWorkloadsReady: fluxReady,
		ReadyWorkloads:    readyCount,
		FailedWorkloads:   failedCount,
		TotalWorkloads:    totalCount,
		Message:           fmt.Sprintf("FluxVersion=%s", fluxVersion),
	}
	setMemberClusterReadyCondition(&mc, phase, fluxReady, defaultCurrentRef, desiredVersion, phaseMessage)

	if shouldPatchMemberClusterStatus(original.Status, mc.Status) {
		if err := hubClient.Status().Patch(ctx, &mc, statusPatch); err != nil {
			kaprometrics.StatusWrites.WithLabelValues("membercluster", "error").Inc()
			return fmt.Errorf("patch MemberCluster status: %w", err)
		}
		kaprometrics.StatusWrites.WithLabelValues("membercluster", "success").Inc()
	}

	// 5. Apply desired versions to their mapped OCIRepositories.
	if phase != kaprov1alpha1.ClusterPhaseFailed {
		for _, repoState := range desiredRepoStates {
			dvVersion := effectiveDesiredVersions[repoState.appKey]
			if dvVersion == "" || repoState.repoErr != nil || !repoState.found {
				continue
			}
			currentForKey := mc.Status.CurrentVersions[repoState.appKey]
			if dvVersion == currentForKey {
				continue
			}
			log.Info("desired version differs from current — patching OCIRepository",
				"ociRepo", repoState.repoName,
				"appKey", repoState.appKey,
				"current", currentForKey,
				"desired", dvVersion,
			)
			var ociRepo sourcev1.OCIRepository
			if err := localClient.Get(ctx, types.NamespacedName{Name: repoState.repoName, Namespace: fluxNamespace}, &ociRepo); err != nil {
				return fmt.Errorf("get OCIRepository %q for appKey %s: %w", repoState.repoName, repoState.appKey, err)
			}
			if err := patchOCIRepositoryTag(ctx, localClient, &ociRepo, dvVersion); err != nil {
				return fmt.Errorf("patch OCIRepository %q for appKey %s: %w", repoState.repoName, repoState.appKey, err)
			}
		}
	}

	log.Info("heartbeat written",
		"phase", phase,
		"appKey", appKey,
		"currentVersion", defaultCurrentRef,
		"desiredVersion", desiredVersion,
		"desiredVersions", effectiveDesiredVersions,
		"fluxReady", fluxReady,
		"phaseMessage", phaseMessage,
	)
	return nil
}

type desiredRepoState struct {
	appKey     string
	repoName   string
	currentRef string
	found      bool
	repoErr    error
}

func readOCIRepositoryRef(ctx context.Context, localClient client.Client, fluxNamespace, repoName string) (string, bool, error) {
	var ociRepo sourcev1.OCIRepository
	if err := localClient.Get(ctx, types.NamespacedName{Name: repoName, Namespace: fluxNamespace}, &ociRepo); err == nil {
		if ociRepo.Spec.Reference != nil {
			if ociRepo.Spec.Reference.Digest != "" {
				return ociRepo.Spec.Reference.Digest, true, nil
			}
			return ociRepo.Spec.Reference.Tag, true, nil
		}
		return "", true, nil
	} else if !apierrors.IsNotFound(err) {
		return "", false, err
	}
	return "", false, nil
}

func resolveOCIRepositoryName(mc *kaprov1alpha1.MemberCluster, defaultName, appKey string, requireMapped bool) (string, error) {
	flux := mc.Spec.Actuator.Flux
	if flux != nil && flux.OCIRepositories[appKey] != "" {
		return flux.OCIRepositories[appKey], nil
	}
	if requireMapped && (flux == nil || len(flux.OCIRepositories) == 0) {
		return "", fmt.Errorf("multi-artifact delivery requires spec.actuator.flux.ociRepositories")
	}
	if flux != nil && len(flux.OCIRepositories) > 0 && flux.OCIRepositories[appKey] == "" {
		return "", fmt.Errorf("missing OCIRepository mapping for appKey %q", appKey)
	}
	if flux != nil && flux.OCIRepository != "" {
		return flux.OCIRepository, nil
	}
	return defaultName, nil
}

func derivePhaseFromDesiredVersions(fluxReady bool, desiredVersion string, desiredVersions, currentVersions map[string]string, repoStates []desiredRepoState) (kaprov1alpha1.ClusterPhase, string) {
	for _, repoState := range repoStates {
		if repoState.repoErr != nil {
			return kaprov1alpha1.ClusterPhaseFailed, repoState.repoErr.Error()
		}
		if !repoState.found {
			return kaprov1alpha1.ClusterPhaseFailed, fmt.Sprintf("required OCIRepository %q for appKey %q was not found", repoState.repoName, repoState.appKey)
		}
	}
	for appKey, want := range desiredVersions {
		if want == "" {
			continue
		}
		if currentVersions[appKey] != want {
			return kaprov1alpha1.ClusterPhaseApplying, fmt.Sprintf("applying desired version for appKey %s", appKey)
		}
	}
	if !fluxReady {
		return kaprov1alpha1.ClusterPhaseConverging, "delivery system reports workloads are not yet ready"
	}
	if desiredVersion == "" && len(desiredVersions) == 0 {
		return kaprov1alpha1.ClusterPhaseConverged, "cluster converged with no desired version set"
	}
	return kaprov1alpha1.ClusterPhaseConverged, "cluster converged"
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

func setMemberClusterReadyCondition(mc *kaprov1alpha1.MemberCluster, phase kaprov1alpha1.ClusterPhase, fluxReady bool, currentRef, desiredVersion, phaseMessage string) {
	status := metav1.ConditionFalse
	reason := string(phase)
	message := phaseMessage
	if message == "" {
		message = "cluster is progressing"
	}

	switch phase {
	case kaprov1alpha1.ClusterPhaseConverged:
		status = metav1.ConditionTrue
		reason = "Converged"
		if currentRef != "" {
			message = fmt.Sprintf("cluster converged at version %s", currentRef)
		}
	case kaprov1alpha1.ClusterPhaseApplying:
		if message == "" && desiredVersion != "" {
			message = fmt.Sprintf("applying desired version %s", desiredVersion)
		}
	case kaprov1alpha1.ClusterPhaseConverging:
		if !fluxReady && message == "" {
			message = "delivery system reports workloads are not yet ready"
		}
	case kaprov1alpha1.ClusterPhaseFailed:
		reason = "Failed"
		if message == "" {
			message = "cluster reported a failed rollout"
		}
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

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func shouldPatchMemberClusterStatus(oldStatus, newStatus kaprov1alpha1.MemberClusterStatus) bool {
	if oldStatus.Phase != newStatus.Phase ||
		oldStatus.ObservedGeneration != newStatus.ObservedGeneration ||
		!reflect.DeepEqual(oldStatus.CurrentVersions, newStatus.CurrentVersions) ||
		oldStatus.DeliverySystem != newStatus.DeliverySystem ||
		!reflect.DeepEqual(oldStatus.Health, newStatus.Health) {
		return true
	}

	oldReady := apimeta.FindStatusCondition(oldStatus.Conditions, "Ready")
	newReady := apimeta.FindStatusCondition(newStatus.Conditions, "Ready")
	if !reflect.DeepEqual(oldReady, newReady) {
		return true
	}

	if oldStatus.LastHeartbeat == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, oldStatus.LastHeartbeat)
	if err != nil {
		return true
	}
	return time.Since(t) >= heartbeatStatusWriteInterval
}

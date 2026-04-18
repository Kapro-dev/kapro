package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/registration"
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

	caBundle := os.Getenv("KAPRO_CONTROL_PLANE_CA_BUNDLE")

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

	ctx := ctrl.SetupSignalHandler()

	// Determine control plane token.
	// Priority: KAPRO_BOOTSTRAP_TOKEN (exchange for SA token) > KAPRO_CONTROL_PLANE_TOKEN (direct) > dev mode
	var controlPlaneToken string
	var tokenExpiry time.Time

	bootstrapToken := os.Getenv("KAPRO_BOOTSTRAP_TOKEN")
	if bootstrapToken != "" {
		log.Info("exchanging bootstrap token for SA token")
		token, expiry, exchangeErr := exchangeBootstrapToken(ctx, controlPlaneURL, environmentRef, bootstrapToken, caBundle)
		if exchangeErr != nil {
			log.Error(exchangeErr, "bootstrap token exchange failed")
			os.Exit(1)
		}
		controlPlaneToken = token
		tokenExpiry = expiry
		log.Info("bootstrap exchange successful", "expiresAt", tokenExpiry.Format(time.RFC3339))
	} else {
		controlPlaneToken = os.Getenv("KAPRO_CONTROL_PLANE_TOKEN")
	}

	// Build control plane client.
	var cpClient client.Client
	if controlPlaneToken != "" {
		cpCfg := &rest.Config{
			Host:        controlPlaneURL,
			BearerToken: controlPlaneToken,
		}
		if caBundle != "" {
			caCert, decodeErr := base64.StdEncoding.DecodeString(caBundle)
			if decodeErr != nil {
				log.Error(decodeErr, "invalid KAPRO_CONTROL_PLANE_CA_BUNDLE")
				os.Exit(1)
			}
			cpCfg.TLSClientConfig = rest.TLSClientConfig{CAData: caCert}
		}
		cpClient, err = client.New(cpCfg, client.Options{Scheme: scheme})
	} else {
		log.Info("no control plane token — using local kubeconfig (dev mode)")
		cpClient, err = client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	}
	if err != nil {
		log.Error(err, "unable to create control plane client")
		os.Exit(1)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Schedule token refresh 5 minutes before expiry.
	var refreshTimer <-chan time.Time
	if bootstrapToken != "" && !tokenExpiry.IsZero() {
		refreshIn := time.Until(tokenExpiry) - 5*time.Minute
		if refreshIn < 0 {
			refreshIn = 0
		}
		refreshTimer = time.After(refreshIn)
	}

	// Run once immediately on startup.
	if err := reconcile(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
		log.Error(err, "initial reconcile failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-refreshTimer:
			log.Info("refreshing SA token before expiry")
			token, expiry, refreshErr := exchangeBootstrapToken(ctx, controlPlaneURL, environmentRef, bootstrapToken, caBundle)
			if refreshErr != nil {
				log.Error(refreshErr, "token refresh failed — will retry in 30s")
			} else {
				controlPlaneToken = token
				tokenExpiry = expiry
				// Rebuild cpClient with new token.
				cpCfg := &rest.Config{Host: controlPlaneURL, BearerToken: controlPlaneToken}
				if caBundle != "" {
					caCert, _ := base64.StdEncoding.DecodeString(caBundle)
					cpCfg.TLSClientConfig = rest.TLSClientConfig{CAData: caCert}
				}
				if newClient, newErr := client.New(cpCfg, client.Options{Scheme: scheme}); newErr == nil {
					cpClient = newClient
				}
				// Schedule next refresh.
				refreshIn := time.Until(tokenExpiry) - 5*time.Minute
				if refreshIn < 0 {
					refreshIn = 30 * time.Second
				}
				refreshTimer = time.After(refreshIn)
			}
		case <-ticker.C:
			if err := reconcile(ctx, localClient, cpClient, environmentRef, fluxNamespace); err != nil {
				log.Error(err, "reconcile failed")
			}
		}
	}
}

// exchangeBootstrapToken POSTs the raw bootstrap token to /register and returns the issued SA token.
func exchangeBootstrapToken(ctx context.Context, controlPlaneURL, clusterName, bootstrapToken, caBundle string) (string, time.Time, error) {
	body, err := json.Marshal(registration.RegisterRequest{
		ClusterName: clusterName,
		Token:       bootstrapToken,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal request: %w", err)
	}

	httpClient := buildHTTPClient(caBundle)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlPlaneURL+"/register", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("registration request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("registration failed %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var regResp registration.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return "", time.Time{}, fmt.Errorf("decode response: %w", err)
	}

	expiresAt, _ := time.Parse(time.RFC3339, regResp.ExpiresAt)
	return regResp.SAToken, expiresAt, nil
}

// buildHTTPClient builds an HTTP client with optional CA bundle for TLS verification.
func buildHTTPClient(caBundle string) *http.Client {
	if caBundle == "" {
		return &http.Client{Timeout: 30 * time.Second}
	}
	caCert, err := base64.StdEncoding.DecodeString(caBundle)
	if err != nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCert)
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
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
	ociRepoName := environmentRef
	if err := cpClient.Get(ctx, types.NamespacedName{Name: environmentRef}, &env); err == nil {
		if env.Spec.Actuator.Flux != nil && env.Spec.Actuator.Flux.OCIRepository != "" {
			ociRepoName = env.Spec.Actuator.Flux.OCIRepository
		}
	}

	// ── 3. Read local OCIRepository for current tag ────────────────────────────
	var ociRepo sourcev1.OCIRepository
	currentRef := ""
	ociFound := false
	if err := localClient.Get(ctx, types.NamespacedName{
		Name:      ociRepoName,
		Namespace: fluxNamespace,
	}, &ociRepo); err == nil {
		ociFound = true
		if ociRepo.Spec.Reference != nil {
			if ociRepo.Spec.Reference.Digest != "" {
				currentRef = ociRepo.Spec.Reference.Digest
			} else {
				currentRef = ociRepo.Spec.Reference.Tag
			}
		}
	}

	// ── 4. Apply desired version if it has changed ─────────────────────────────
	desiredVersion := reg.Spec.DesiredVersion
	if ociFound && desiredVersion != "" && desiredVersion != currentRef {
		log.Info("desired version differs from current ref — patching OCIRepository",
			"ociRepo", ociRepoName,
			"current", currentRef,
			"desired", desiredVersion,
		)
		if err := patchOCIRepositoryTag(ctx, localClient, &ociRepo, fluxNamespace, desiredVersion); err != nil {
			return fmt.Errorf("patch OCIRepository tag: %w", err)
		}
		_ = localClient.Get(ctx, types.NamespacedName{Name: ociRepoName, Namespace: fluxNamespace}, &ociRepo)
		if ociRepo.Spec.Reference != nil {
			if ociRepo.Spec.Reference.Digest != "" {
				currentRef = ociRepo.Spec.Reference.Digest
			} else {
				currentRef = ociRepo.Spec.Reference.Tag
			}
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
		if fluxVersion == "" && ks.Status.LastAppliedRevision != "" {
			fluxVersion = ks.Status.LastAppliedRevision
		}
	}

	// ── 6. Derive cluster phase ────────────────────────────────────────────────
	phase := derivePhase(fluxReady, currentRef, desiredVersion)

	// ── 7. Write status to control plane ──────────────────────────────────────
	// Use DesiredAppKey so convergence checks in the operator use the same key.
	// Default to "default" when not set — never hardcode an app name.
	appKey := reg.Spec.DesiredAppKey
	if appKey == "" {
		appKey = "default"
	}
	patch := client.MergeFrom(reg.DeepCopy())
	now := metav1.Now()
	reg.Status.LastHeartbeat = now.UTC().Format(time.RFC3339)
	reg.Status.Health = kaprov1alpha1.ClusterHealth{
		AllWorkloadsReady: fluxReady,
		Message:           fmt.Sprintf("FluxVersion=%s", fluxVersion),
	}
	reg.Status.DeliverySystem = "flux"
	if reg.Status.CurrentVersions == nil {
		reg.Status.CurrentVersions = make(map[string]string)
	}
	reg.Status.CurrentVersions[appKey] = currentRef
	reg.Status.Phase = phase

	if patchErr := cpClient.Status().Patch(ctx, &reg, patch); patchErr != nil {
		return fmt.Errorf("patch ClusterRegistration status: %w", patchErr)
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

// patchOCIRepositoryTag sets OCIRepository.spec.reference and annotates
// with reconcile.fluxcd.io/requestedAt to force an immediate Flux reconciliation.
// If version contains "@sha256:", it is treated as a digest reference and
// OCIRepository.spec.reference.digest is set (not .tag) — preserving the
// immutability guarantee from the Artifact CR.
func patchOCIRepositoryTag(
	ctx context.Context,
	localClient client.Client,
	ociRepo *sourcev1.OCIRepository,
	_ string,
	version string,
) error {
	patch := client.MergeFrom(ociRepo.DeepCopy())
	if ociRepo.Spec.Reference == nil {
		ociRepo.Spec.Reference = &sourcev1.OCIRepositoryRef{}
	}
	if idx := strings.Index(version, "@sha256:"); idx != -1 {
		// Digest reference: registry.io/repo@sha256:abc123
		// Set .digest and clear .tag so Flux pins by digest, not mutable tag.
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

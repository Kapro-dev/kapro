package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	hubCredentialsSecret  = "kapro-hub-credentials"
	credentialsCertKey    = "tls.crt"
	credentialsKeyKey     = "tls.key"
	spokeKaproNamespace   = "kapro-system"
	certRenewalThreshold  = 30 * 24 * time.Hour
	csrPollInterval       = 5 * time.Second
	csrPollTimeout        = 5 * time.Minute
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
		"environment", environmentRef,
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

	hubClient, err := loadOrBootstrapHubClient(ctx, localClient, environmentRef, hubURL, hubCAData)
	if err != nil {
		log.Error(err, "failed to bootstrap hub credentials")
		os.Exit(1)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	renewTicker := time.NewTicker(time.Hour)
	defer renewTicker.Stop()

	// renewResultCh receives a rebuilt hub client after background cert renewal.
	// Buffered so the renewal goroutine never blocks if main loop is busy.
	renewResultCh := make(chan client.Client, 1)

	if err := reconcile(ctx, localClient, hubClient, environmentRef, fluxNamespace); err != nil {
		log.Error(err, "initial reconcile failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case newHub := <-renewResultCh:
			// Background renewal completed — swap in the new hub client.
			hubClient = newHub
			log.Info("cert renewed and hub client updated")
		case <-renewTicker.C:
			certPEM, keyPEM, loadErr := loadLocalCredentials(ctx, localClient)
			if loadErr == nil && certExpiresSoon(certPEM) {
				log.Info("cert expiring soon — renewing via CSR in background")
				// Run renewal in a goroutine so heartbeats are never blocked.
				go func(cp, kp []byte) {
					newCertPEM, newKeyPEM, renewErr := renewWithCSR(ctx, hubURL, hubCAData, cp, kp, environmentRef)
					if renewErr != nil {
						log.Error(renewErr, "cert renewal failed — will retry next hour")
						return
					}
					if storeErr := storeLocalCredentials(ctx, localClient, newCertPEM, newKeyPEM); storeErr != nil {
						log.Error(storeErr, "failed to persist renewed credentials (non-fatal)")
					}
					hubCfg := buildHubConfig(hubURL, hubCAData, newCertPEM, newKeyPEM)
					newHub, clientErr := client.New(hubCfg, client.Options{Scheme: scheme})
					if clientErr != nil {
						log.Error(clientErr, "failed to rebuild hub client after renewal")
						return
					}
					renewResultCh <- newHub
				}(certPEM, keyPEM)
			}
		case <-ticker.C:
			if err := reconcile(ctx, localClient, hubClient, environmentRef, fluxNamespace); err != nil {
				log.Error(err, "reconcile failed")
			}
		}
	}
}

// loadOrBootstrapHubClient returns a hub client backed by a valid mTLS cert.
// If no cert exists locally → first bootstrap via KAPRO_BOOTSTRAP_KUBECONFIG_PATH.
// If cert is expiring soon → renew via CSR using the existing cert.
func loadOrBootstrapHubClient(ctx context.Context, localClient client.Client, envRef, hubURL string, hubCAData []byte) (client.Client, error) {
	certPEM, keyPEM, err := loadLocalCredentials(ctx, localClient)
	if err == nil && !certExpiresSoon(certPEM) {
		return client.New(buildHubConfig(hubURL, hubCAData, certPEM, keyPEM), client.Options{Scheme: scheme})
	}

	var newCertPEM, newKeyPEM []byte
	if err == nil {
		// Cert found but expiring — renew using existing cert.
		newCertPEM, newKeyPEM, err = renewWithCSR(ctx, hubURL, hubCAData, certPEM, keyPEM, envRef)
	} else {
		// No cert — first bootstrap via kubeconfig SA token.
		newCertPEM, newKeyPEM, err = firstBootstrapWithCSR(ctx, envRef)
	}
	if err != nil {
		return nil, err
	}

	if storeErr := storeLocalCredentials(ctx, localClient, newCertPEM, newKeyPEM); storeErr != nil {
		ctrl.Log.Error(storeErr, "warning: failed to persist hub credentials (non-fatal)")
	}

	return client.New(buildHubConfig(hubURL, hubCAData, newCertPEM, newKeyPEM), client.Options{Scheme: scheme})
}

// firstBootstrapWithCSR submits a CSR using the bootstrap kubeconfig SA token (one-time).
func firstBootstrapWithCSR(ctx context.Context, envRef string) (certPEM, keyPEM []byte, err error) {
	kubeconfigPath := os.Getenv("KAPRO_BOOTSTRAP_KUBECONFIG_PATH")
	if kubeconfigPath == "" {
		return nil, nil, fmt.Errorf("KAPRO_BOOTSTRAP_KUBECONFIG_PATH is required for first-time bootstrap (no existing hub credentials found)")
	}
	bootstrapCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load bootstrap kubeconfig from %q: %w", kubeconfigPath, err)
	}
	return submitAndWaitForCSR(ctx, bootstrapCfg, envRef)
}

// renewWithCSR submits a CSR using the existing client cert for authentication.
func renewWithCSR(ctx context.Context, hubURL string, hubCAData, certPEM, keyPEM []byte, envRef string) ([]byte, []byte, error) {
	existingCfg := buildHubConfig(hubURL, hubCAData, certPEM, keyPEM)
	return submitAndWaitForCSR(ctx, existingCfg, envRef)
}

// submitAndWaitForCSR generates an RSA-2048 key, submits a CSR to the hub,
// and polls until the CSR is approved and a certificate is issued.
func submitAndWaitForCSR(ctx context.Context, cfg *rest.Config, envRef string) (certPEM, keyPEM []byte, err error) {
	log := ctrl.Log.WithName("csr-bootstrap")

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   "kapro-cluster:" + envRef,
			Organization: []string{"kapro:cluster-controllers"},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR template: %w", err)
	}
	csrPEMBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	csrName := fmt.Sprintf("kapro-cluster-%s-%d", strings.ToLower(envRef), time.Now().UnixMilli())

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kube client for CSR: %w", err)
	}

	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: csrName},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEMBytes,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	if _, err := kubeClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{}); err != nil {
		return nil, nil, fmt.Errorf("create CSR %q: %w", csrName, err)
	}

	log.Info("CSR submitted, waiting for hub approval", "csr", csrName, "cluster", envRef)

	deadline := time.Now().Add(csrPollTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("CSR %q not approved within %v — check hub BootstrapToken and CSR approval controller", csrName, csrPollTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(csrPollInterval):
		}

		approved, pollErr := kubeClient.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
		if pollErr != nil {
			log.Error(pollErr, "polling CSR (will retry)", "csr", csrName)
			continue
		}
		for _, c := range approved.Status.Conditions {
			if c.Type == certificatesv1.CertificateDenied {
				return nil, nil, fmt.Errorf("CSR %q denied: %s", csrName, c.Message)
			}
		}
		if len(approved.Status.Certificate) == 0 {
			continue
		}

		keyPEM = pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
		})
		log.Info("CSR approved, certificate issued", "csr", csrName)
		return approved.Status.Certificate, keyPEM, nil
	}
}

// loadLocalCredentials reads the hub mTLS cert+key from the local spoke cluster.
func loadLocalCredentials(ctx context.Context, localClient client.Client) (certPEM, keyPEM []byte, err error) {
	var secret corev1.Secret
	if err := localClient.Get(ctx, types.NamespacedName{
		Namespace: spokeKaproNamespace,
		Name:      hubCredentialsSecret,
	}, &secret); err != nil {
		return nil, nil, err
	}
	certPEM = secret.Data[credentialsCertKey]
	keyPEM = secret.Data[credentialsKeyKey]
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, fmt.Errorf("credentials secret %s/%s is missing cert or key", spokeKaproNamespace, hubCredentialsSecret)
	}
	return certPEM, keyPEM, nil
}

// storeLocalCredentials saves the hub mTLS cert+key to a local Secret on the spoke.
func storeLocalCredentials(ctx context.Context, localClient client.Client, certPEM, keyPEM []byte) error {
	secret := &corev1.Secret{}
	err := localClient.Get(ctx, types.NamespacedName{
		Namespace: spokeKaproNamespace,
		Name:      hubCredentialsSecret,
	}, secret)

	if apierrors.IsNotFound(err) {
		return localClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hubCredentialsSecret,
				Namespace: spokeKaproNamespace,
				Labels:    map[string]string{"kapro.io/role": "hub-credentials"},
			},
			Data: map[string][]byte{
				credentialsCertKey: certPEM,
				credentialsKeyKey:  keyPEM,
			},
		})
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(secret.DeepCopy())
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[credentialsCertKey] = certPEM
	secret.Data[credentialsKeyKey] = keyPEM
	return localClient.Patch(ctx, secret, patch)
}

// certExpiresSoon returns true if the certificate in certPEM expires within certRenewalThreshold.
func certExpiresSoon(certPEM []byte) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return time.Until(cert.NotAfter) < certRenewalThreshold
}

// buildHubConfig constructs a rest.Config for mTLS connections to the hub kube-apiserver.
func buildHubConfig(hubURL string, hubCAData, certPEM, keyPEM []byte) *rest.Config {
	return &rest.Config{
		Host: hubURL,
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   hubCAData,
			CertData: certPEM,
			KeyData:  keyPEM,
		},
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
//  1. GET ManagedCluster from hub to read desired state.
//  2. Read local Flux state (OCIRepository + Kustomization status).
//  3. PATCH ManagedCluster/status on hub with current state.
//  4. Apply desired version to local OCIRepository if it has changed.
func reconcile(
	ctx context.Context,
	localClient, hubClient client.Client,
	environmentRef, fluxNamespace string,
) error {
	log := ctrl.Log.WithName("reconcile").WithValues("env", environmentRef)

	// 1. GET ManagedCluster from hub.
	var mc kaprov1alpha1.ManagedCluster
	if err := hubClient.Get(ctx, types.NamespacedName{Name: environmentRef}, &mc); err != nil {
		return fmt.Errorf("get ManagedCluster %q from hub: %w", environmentRef, err)
	}

	desiredVersion := mc.Spec.DesiredVersion
	appKey := mc.Spec.DesiredAppKey
	if appKey == "" {
		appKey = "default"
	}

	// 2. Read local OCIRepository (name = EnvironmentRef, or environmentRef as fallback).
	ociRepoName := mc.Spec.EnvironmentRef
	if ociRepoName == "" {
		ociRepoName = environmentRef
	}

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

	if err := hubClient.Status().Patch(ctx, &mc, statusPatch); err != nil {
		return fmt.Errorf("patch ManagedCluster status: %w", err)
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


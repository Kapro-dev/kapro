// Package bootstrap provides Provider implementations for kapro-cluster-controller
// to authenticate to the hub Kubernetes API server.
//
// Two implementations live side-by-side in this file for easy comparison:
//
//   - Generic — Kubernetes-native CSR bootstrap (any distribution, any cloud)
//   - GCP     — GKE Workload Identity token from GCE metadata server (GKE only)
//
// Select at runtime via the KAPRO_PROVIDER environment variable:
//
//	"" or "generic" → Generic (default)
//	"gcp"           → GCP
package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ── Provider interface ────────────────────────────────────────────────────────

// Provider abstracts spoke→hub credential acquisition for the cluster-controller.
//
// Implementations are constructed once with all dependencies, then called
// repeatedly. HubConfig caches credentials internally and refreshes only
// when NeedsRenewal returns true.
//
// All methods are safe for concurrent use.
type Provider interface {
	// HubConfig returns a *rest.Config ready to talk to the hub K8s API.
	// Returns a cached config when credentials are still valid; refreshes otherwise.
	HubConfig(ctx context.Context) (*rest.Config, error)

	// NeedsRenewal returns true when credentials are approaching expiry and
	// the caller should schedule a background HubConfig refresh.
	NeedsRenewal() bool

	// Name returns the provider identifier used in log messages ("generic" or "gcp").
	Name() string
}

// ── Generic provider (Kubernetes CSR) ────────────────────────────────────────

const (
	hubCredentialsSecret = "kapro-hub-credentials"
	credentialsCertKey   = "tls.crt"
	credentialsKeyKey    = "tls.key"
	spokeNamespace       = "kapro-system"
	certRenewalThreshold = 30 * 24 * time.Hour
	csrPollInterval      = 5 * time.Second
	csrPollTimeout       = 5 * time.Minute
)

// Generic is the Kubernetes-native bootstrap provider.
// It uses a bootstrap SA token to perform a CertificateSigningRequest (CSR)
// dance with the hub, then stores the issued mTLS cert in a local Secret so
// pod restarts do not require re-bootstrapping.
//
// The bootstrap token is read from KAPRO_BOOTSTRAP_TOKEN (preferred) or
// KAPRO_BOOTSTRAP_KUBECONFIG_PATH (backward-compat fallback).
type Generic struct {
	localClient client.Client
	hubURL      string
	hubCAData   []byte
	envRef      string

	mu      sync.RWMutex
	certPEM []byte
	keyPEM  []byte
}

// NewGeneric returns a Generic provider. All four parameters are required.
func NewGeneric(localClient client.Client, hubURL string, hubCAData []byte, envRef string) *Generic {
	return &Generic{
		localClient: localClient,
		hubURL:      hubURL,
		hubCAData:   hubCAData,
		envRef:      envRef,
	}
}

// HubConfig returns a *rest.Config backed by the spoke's mTLS cert.
//   - Fast path: returns cached in-memory cert if still valid.
//   - Pod-restart path: loads cert from the local hub-credentials Secret.
//   - Bootstrap path: performs CSR dance if no cert exists.
//   - Renewal path: re-issues cert via CSR if existing cert is expiring.
func (g *Generic) HubConfig(ctx context.Context) (*rest.Config, error) {
	// Fast path: in-memory cert still valid.
	g.mu.RLock()
	cached := g.certPEM
	g.mu.RUnlock()
	if len(cached) > 0 && !certExpiresSoon(cached) {
		g.mu.RLock()
		cert, key := g.certPEM, g.keyPEM
		g.mu.RUnlock()
		return buildHubConfig(g.hubURL, g.hubCAData, cert, key), nil
	}

	// Load from local Secret (source of truth across pod restarts).
	certPEM, keyPEM, err := g.loadLocalCredentials(ctx)
	if err == nil && !certExpiresSoon(certPEM) {
		g.mu.Lock()
		g.certPEM, g.keyPEM = certPEM, keyPEM
		g.mu.Unlock()
		return buildHubConfig(g.hubURL, g.hubCAData, certPEM, keyPEM), nil
	}

	// Bootstrap or renew via CSR.
	var newCert, newKey []byte
	if err == nil {
		// Cert exists but is expiring — renew using the existing cert as auth.
		newCert, newKey, err = g.renewWithCSR(ctx, certPEM, keyPEM)
	} else {
		// No cert at all — first bootstrap via KAPRO_BOOTSTRAP_TOKEN.
		newCert, newKey, err = g.firstBootstrapWithCSR(ctx)
	}
	if err != nil {
		return nil, err
	}

	if storeErr := g.storeLocalCredentials(ctx, newCert, newKey); storeErr != nil {
		ctrl.Log.Error(storeErr, "failed to persist hub credentials (non-fatal)")
	}

	g.mu.Lock()
	g.certPEM, g.keyPEM = newCert, newKey
	g.mu.Unlock()

	return buildHubConfig(g.hubURL, g.hubCAData, newCert, newKey), nil
}

// NeedsRenewal returns true when the in-memory cert is within the renewal window.
func (g *Generic) NeedsRenewal() bool {
	g.mu.RLock()
	cert := g.certPEM
	g.mu.RUnlock()
	return certExpiresSoon(cert)
}

// Name implements Provider.
func (g *Generic) Name() string { return "generic" }

func (g *Generic) firstBootstrapWithCSR(ctx context.Context) ([]byte, []byte, error) {
	if token := os.Getenv("KAPRO_BOOTSTRAP_TOKEN"); token != "" {
		cfg := &rest.Config{
			Host:            g.hubURL,
			BearerToken:     token,
			TLSClientConfig: rest.TLSClientConfig{CAData: g.hubCAData},
		}
		return g.submitAndWaitForCSR(ctx, cfg)
	}
	kubeconfigPath := os.Getenv("KAPRO_BOOTSTRAP_KUBECONFIG_PATH")
	if kubeconfigPath == "" {
		return nil, nil, fmt.Errorf("no bootstrap credentials: set KAPRO_BOOTSTRAP_TOKEN or KAPRO_BOOTSTRAP_KUBECONFIG_PATH")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load bootstrap kubeconfig from %q: %w", kubeconfigPath, err)
	}
	return g.submitAndWaitForCSR(ctx, cfg)
}

func (g *Generic) renewWithCSR(ctx context.Context, certPEM, keyPEM []byte) ([]byte, []byte, error) {
	return g.submitAndWaitForCSR(ctx, buildHubConfig(g.hubURL, g.hubCAData, certPEM, keyPEM))
}

func (g *Generic) submitAndWaitForCSR(ctx context.Context, cfg *rest.Config) (certPEM, keyPEM []byte, err error) {
	log := ctrl.Log.WithName("csr-bootstrap")

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   "kapro-cluster:" + g.envRef,
			Organization: []string{"kapro:cluster-controllers"},
		},
	}, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}

	csrName := fmt.Sprintf("kapro-cluster-%s-%d", strings.ToLower(g.envRef), time.Now().UnixMilli())

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kube client for CSR: %w", err)
	}

	csrObj := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: csrName},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:           pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
			SignerName:        "kubernetes.io/kube-apiserver-client",
			Usages:            []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
			ExpirationSeconds: int32Ptr(365 * 24 * 60 * 60),
		},
	}
	if _, err := kubeClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csrObj, metav1.CreateOptions{}); err != nil {
		return nil, nil, fmt.Errorf("create CSR %q: %w", csrName, err)
	}

	log.Info("CSR submitted, waiting for hub approval", "csr", csrName, "cluster", g.envRef)

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

		log.Info("CSR approved, certificate issued", "csr", csrName)
		return approved.Status.Certificate, pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
		}), nil
	}
}

func (g *Generic) loadLocalCredentials(ctx context.Context) (certPEM, keyPEM []byte, err error) {
	var secret corev1.Secret
	if err := g.localClient.Get(ctx, types.NamespacedName{
		Namespace: spokeNamespace,
		Name:      hubCredentialsSecret,
	}, &secret); err != nil {
		return nil, nil, err
	}
	cert, key := secret.Data[credentialsCertKey], secret.Data[credentialsKeyKey]
	if len(cert) == 0 || len(key) == 0 {
		return nil, nil, fmt.Errorf("credentials secret %s/%s missing cert or key", spokeNamespace, hubCredentialsSecret)
	}
	return cert, key, nil
}

func (g *Generic) storeLocalCredentials(ctx context.Context, certPEM, keyPEM []byte) error {
	secret := &corev1.Secret{}
	err := g.localClient.Get(ctx, types.NamespacedName{
		Namespace: spokeNamespace,
		Name:      hubCredentialsSecret,
	}, secret)
	if apierrors.IsNotFound(err) {
		return g.localClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hubCredentialsSecret,
				Namespace: spokeNamespace,
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
	return g.localClient.Patch(ctx, secret, patch)
}

// certExpiresSoon returns true if the certificate expires within certRenewalThreshold.
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

// buildHubConfig constructs a mTLS rest.Config for the hub K8s API.
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

func int32Ptr(v int32) *int32 { return &v }

// ── GCP provider (GKE Workload Identity) ─────────────────────────────────────

const (
	gcpMetadataURL   = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	gcpHTTPTimeout   = 5 * time.Second
	gcpRenewalLeeway = 10 * time.Minute
)

type gcpTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"` // seconds until token expiry
	TokenType   string `json:"token_type"`
}

// GCP authenticates to the hub GKE API using an OAuth2 access token obtained
// from the GCE metadata server via Workload Identity.
//
// Prerequisites on the hub:
//   - ClusterRole + ClusterRoleBinding for the GSA email (created by
//     `kapro cluster join --gcp-service-account` or `kapro spoke install --gcp-service-account`)
//
// Prerequisites on the spoke:
//   - KSA annotated with iam.gke.io/gcp-service-account
//   - GSA bound to KSA via Workload Identity
//   - GSA has roles/container.developer on the hub GKE project
type GCP struct {
	hubURL    string
	hubCAData []byte

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

// NewGCP returns a GCP provider for the given hub URL and CA bundle.
func NewGCP(hubURL string, hubCAData []byte) *GCP {
	return &GCP{
		hubURL:    hubURL,
		hubCAData: hubCAData,
	}
}

// HubConfig returns a *rest.Config using a GCP OAuth2 access token.
// Returns the cached token if still valid; fetches a fresh one when expiring or absent.
func (g *GCP) HubConfig(ctx context.Context) (*rest.Config, error) {
	// Fast path: cached token still has enough remaining life.
	g.mu.RLock()
	if g.token != "" && time.Now().Add(gcpRenewalLeeway).Before(g.expiresAt) {
		cfg := g.buildConfig(g.token)
		g.mu.RUnlock()
		return cfg, nil
	}
	g.mu.RUnlock()

	token, expiresAt, err := fetchGCPToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("GCP provider: %w", err)
	}

	g.mu.Lock()
	g.token = token
	g.expiresAt = expiresAt
	g.mu.Unlock()

	ctrl.Log.WithName("gcp-provider").Info("access token refreshed",
		"expiresAt", expiresAt.Format(time.RFC3339))
	return g.buildConfig(token), nil
}

// NeedsRenewal returns true when the cached token is within the renewal window.
func (g *GCP) NeedsRenewal() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.token == "" || time.Now().Add(gcpRenewalLeeway).After(g.expiresAt)
}

// Name implements Provider.
func (g *GCP) Name() string { return "gcp" }

func (g *GCP) buildConfig(token string) *rest.Config {
	return &rest.Config{
		Host:            g.hubURL,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{CAData: g.hubCAData},
	}
}

func fetchGCPToken(ctx context.Context) (token string, expiresAt time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gcpMetadataURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build metadata request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := (&http.Client{Timeout: gcpHTTPTimeout}).Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("metadata server unreachable (not running on GCP? unset KAPRO_PROVIDER=gcp): %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		// handled below
	case http.StatusNotFound:
		return "", time.Time{}, fmt.Errorf("metadata server 404 — no service account bound to pod (check iam.gke.io/gcp-service-account annotation on KSA)")
	case http.StatusForbidden:
		return "", time.Time{}, fmt.Errorf("metadata server 403 — Workload Identity misconfigured (check KSA annotation and GSA IAM binding)")
	default:
		return "", time.Time{}, fmt.Errorf("metadata server returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tr gcpTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("parse metadata token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("metadata server returned empty access_token")
	}

	return tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second), nil
}

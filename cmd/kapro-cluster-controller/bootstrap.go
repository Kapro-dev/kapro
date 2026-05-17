package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// BootstrapClient is the kubeconfig used for the FIRST CSR submission.
// After the cert is issued the spoke switches to cert-based auth.
type BootstrapClient struct {
	RestConfig *rest.Config
	SourcePath string
}

// certManagerOptions is the bundle of configuration handed to startCertManager.
// Kept separate from cmd Config so this file can be tested in isolation.
type certManagerOptions struct {
	Template            *x509.CertificateRequest
	SignerName          string
	Usages              []certificatesv1.KeyUsage
	BootstrapClient     *BootstrapClient
	HubAPIURL           string
	HubCAData           []byte
	Store               *secretStore
	RequestedCertTTL    time.Duration
	WaitForFirstCert    time.Duration
	WaitForCertInterval time.Duration
}

// certManager owns the spoke's hub identity (cert + key) and rotates it
// before expiry by submitting renewal CSRs using the current cert as auth.
//
// This is a deliberately simpler alternative to client-go's
// certificate.Manager: ~200 lines vs the transport-wrapper plumbing required
// to make Manager.Current() flow into a controller-runtime client. For our
// scope (one cluster, one cert, one rotation loop) the manual approach is
// clearer and easier to reason about.
type certManager struct {
	opts    certManagerOptions
	store   *secretStore
	stopCh  chan struct{}
	stopped chan struct{}

	mu   sync.RWMutex
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	// renewBefore is the fraction of cert lifetime at which we renew. 0.5
	// matches kubelet (renew at half-life).
	renewBefore float64
}

// startCertManager submits a CSR, waits for it to be signed, persists the
// cert+key to the local Secret store, and spawns a rotation goroutine. Blocks
// until either a cert is issued or WaitForFirstCert elapses.
func startCertManager(ctx context.Context, opts certManagerOptions) (*certManager, error) {
	if err := validateCertOptions(&opts); err != nil {
		return nil, err
	}
	cm := &certManager{
		opts:        opts,
		store:       opts.Store,
		stopCh:      make(chan struct{}),
		stopped:     make(chan struct{}),
		renewBefore: 0.5,
	}

	// 1. Attempt to load an existing cert from the local Secret.
	if cert, key, ok, err := cm.store.Load(ctx); err != nil {
		return nil, fmt.Errorf("load cert from local Secret: %w", err)
	} else if ok {
		if cm.acceptIfValid(cert, key) {
			log.Log.WithName("cert").Info("loaded existing cert from local Secret", "notAfter", cert.NotAfter.Format(time.RFC3339))
			go cm.runRotation(ctx)
			return cm, nil
		}
		log.Log.WithName("cert").Info("ignored expired/expiring cert from local Secret — will bootstrap a new one")
	}

	// 2. No usable cert — submit the first CSR using the bootstrap kubeconfig.
	if opts.BootstrapClient == nil || opts.BootstrapClient.RestConfig == nil {
		return nil, fmt.Errorf("no usable cert in local Secret and no bootstrap kubeconfig; set KAPRO_BOOTSTRAP_KUBECONFIG_PATH")
	}
	cert, key, err := cm.submitAndWaitForCert(ctx, opts.BootstrapClient.RestConfig)
	if err != nil {
		return nil, fmt.Errorf("first bootstrap CSR: %w", err)
	}
	if err := cm.persist(ctx, cert, key); err != nil {
		return nil, fmt.Errorf("persist cert: %w", err)
	}
	cm.mu.Lock()
	cm.cert, cm.key = cert, key
	cm.mu.Unlock()

	go cm.runRotation(ctx)
	return cm, nil
}

// Stop signals the rotation goroutine to exit and waits for it.
func (m *certManager) Stop() {
	select {
	case <-m.stopCh:
		// already stopped
	default:
		close(m.stopCh)
	}
	<-m.stopped
}

// Current returns the current cert PEM + key PEM. Safe for concurrent use.
func (m *certManager) Current() (certPEM, keyPEM []byte) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil || m.key == nil {
		return nil, nil
	}
	return encodeCert(m.cert), encodeKey(m.key)
}

// CurrentNotAfter returns the expiry of the current cert, or zero Time.
func (m *certManager) CurrentNotAfter() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return time.Time{}
	}
	return m.cert.NotAfter
}

// runRotation is the rotation goroutine. Wakes every minute; if the cert is
// past its renew-before deadline, submits a renewal CSR using the current
// cert as auth and atomically swaps the in-memory + persisted cert.
func (m *certManager) runRotation(ctx context.Context) {
	defer close(m.stopped)
	logger := log.Log.WithName("cert-rotation")
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			if !m.shouldRenew() {
				continue
			}
			logger.Info("cert approaching expiry, submitting renewal CSR", "expires", m.CurrentNotAfter())
			cfg := m.currentRestConfig()
			cert, key, err := m.submitAndWaitForCert(ctx, cfg)
			if err != nil {
				logger.Error(err, "renewal CSR failed, will retry next tick")
				continue
			}
			if err := m.persist(ctx, cert, key); err != nil {
				logger.Error(err, "persist renewed cert failed")
				continue
			}
			m.mu.Lock()
			m.cert, m.key = cert, key
			m.mu.Unlock()
			logger.Info("cert renewed", "newExpiry", cert.NotAfter)
		}
	}
}

func (m *certManager) shouldRenew() bool {
	m.mu.RLock()
	cert := m.cert
	m.mu.RUnlock()
	if cert == nil {
		return false
	}
	now := time.Now()
	lifetime := cert.NotAfter.Sub(cert.NotBefore)
	renewAt := cert.NotBefore.Add(time.Duration(float64(lifetime) * m.renewBefore))
	return now.After(renewAt)
}

func (m *certManager) currentRestConfig() *rest.Config {
	certPEM, keyPEM := m.Current()
	return buildCertConfig(m.opts.HubAPIURL, m.opts.HubCAData, certPEM, keyPEM)
}

// submitAndWaitForCert generates a fresh keypair, sends a CSR via the given
// rest.Config (bootstrap or current-cert), and polls until status.certificate
// is populated AND the CertificateApproved condition is set.
func (m *certManager) submitAndWaitForCert(ctx context.Context, cfg *rest.Config) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, m.opts.Template, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR request: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kube client for CSR: %w", err)
	}

	csrName := fmt.Sprintf("kapro-cluster-%s-%d", strings.ToLower(sanitizeName(m.opts.Template.Subject.CommonName)), time.Now().UnixMilli())
	csrObj := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: csrName},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:           pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
			SignerName:        m.opts.SignerName,
			Usages:            m.opts.Usages,
			ExpirationSeconds: int32Ptr(int32(m.opts.RequestedCertTTL.Seconds())),
		},
	}
	if _, err := clientset.CertificatesV1().CertificateSigningRequests().Create(ctx, csrObj, metav1.CreateOptions{}); err != nil {
		return nil, nil, fmt.Errorf("create CSR %q: %w", csrName, err)
	}

	logger := log.Log.WithName("csr").WithValues("csr", csrName)
	logger.Info("CSR submitted, waiting for approver")

	wait := wait.Backoff{
		Steps:    int(m.opts.WaitForFirstCert / m.opts.WaitForCertInterval),
		Duration: m.opts.WaitForCertInterval,
		Cap:      m.opts.WaitForFirstCert,
	}
	deadline := time.Now().Add(m.opts.WaitForFirstCert)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(wait.Step()):
		}
		got, err := clientset.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
		if err != nil {
			logger.Info("polling CSR (will retry)", "error", err.Error())
			continue
		}
		denied := false
		approved := false
		for _, c := range got.Status.Conditions {
			switch c.Type {
			case certificatesv1.CertificateDenied:
				if c.Status == corev1.ConditionTrue {
					denied = true
				}
			case certificatesv1.CertificateApproved:
				if c.Status == corev1.ConditionTrue {
					approved = true
				}
			}
		}
		if denied {
			return nil, nil, fmt.Errorf("CSR %q denied by hub approver", csrName)
		}
		if !approved || len(got.Status.Certificate) == 0 {
			continue
		}
		block, _ := pem.Decode(got.Status.Certificate)
		if block == nil {
			return nil, nil, fmt.Errorf("CSR %q returned non-PEM certificate", csrName)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse signed cert from CSR %q: %w", csrName, err)
		}
		logger.Info("CSR approved and signed", "notAfter", cert.NotAfter.Format(time.RFC3339))
		return cert, key, nil
	}
	return nil, nil, fmt.Errorf("CSR %q not approved within %s", csrName, m.opts.WaitForFirstCert)
}

func (m *certManager) acceptIfValid(cert *x509.Certificate, key *ecdsa.PrivateKey) bool {
	if cert == nil || key == nil {
		return false
	}
	if time.Until(cert.NotAfter) < 10*time.Minute {
		return false
	}
	m.mu.Lock()
	m.cert, m.key = cert, key
	m.mu.Unlock()
	return true
}

func (m *certManager) persist(ctx context.Context, cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	return m.store.Save(ctx, encodeCert(cert), encodeKey(key))
}

// HubClient is a rotation-aware client.Client. Internally it tracks the
// fingerprint of the cert it was built with; on Client() it transparently
// rebuilds when certManager has rotated to a fresh cert. Cheap — the
// fingerprint comparison is a byte-compare on the PEM, the rebuild happens
// once per rotation (~half cert lifetime).
//
// Without this wrapper, heartbeat + status loops would keep using a client
// wired to the bootstrap cert; once that cert expires (at default 1y
// lifetime, after ~180d of pod uptime) every API call would 401.
type HubClient struct {
	certMgr *certManager
	hubURL  string
	caData  []byte

	mu       sync.Mutex
	current  client.Client
	curCert  []byte
}

// Client returns a fresh controller-runtime client when cert has rotated;
// otherwise returns the cached one. Safe for concurrent use.
//
// Static-mode HubClients (built via newHubClientFromStatic for tests) skip
// the certManager check and always return the injected client.
func (h *HubClient) Client() (client.Client, error) {
	if h.certMgr == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.current == nil {
			return nil, fmt.Errorf("hub client: no static client configured")
		}
		return h.current, nil
	}
	certPEM, keyPEM := h.certMgr.Current()
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, fmt.Errorf("hub client: certManager has no current cert")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil && bytesEqual(h.curCert, certPEM) {
		return h.current, nil
	}
	cfg := buildCertConfig(h.hubURL, h.caData, certPEM, keyPEM)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("hub client: build: %w", err)
	}
	h.current = c
	h.curCert = certPEM
	return h.current, nil
}

func newHubClient(certMgr *certManager, hubURL string, caData []byte) *HubClient {
	return &HubClient{certMgr: certMgr, hubURL: hubURL, caData: caData}
}

// newHubClientFromStatic exposes a HubClient that always returns the given
// underlying client without any rotation logic. Test-only: lets unit tests
// inject a fake controller-runtime client without needing a real certManager.
func newHubClientFromStatic(c client.Client) *HubClient {
	return &HubClient{
		current: c,
		curCert: []byte("test-static-cert-no-rotation"),
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func buildCertConfig(hubURL string, caData, certPEM, keyPEM []byte) *rest.Config {
	return &rest.Config{
		Host: hubURL,
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   caData,
			CertData: certPEM,
			KeyData:  keyPEM,
		},
	}
}

// inClusterConfig wraps rest.InClusterConfig with a readable error when run
// outside a pod (during local dev). Used by buildLocalClient when no
// KUBECONFIG is configured.
func inClusterConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("not running in a pod and no KUBECONFIG configured: %w", err)
	}
	return cfg, nil
}

func validateCertOptions(opts *certManagerOptions) error {
	if opts.Template == nil || opts.Template.Subject.CommonName == "" {
		return fmt.Errorf("cert options: Template with CN required")
	}
	if opts.SignerName == "" {
		return fmt.Errorf("cert options: SignerName required")
	}
	if opts.HubAPIURL == "" {
		return fmt.Errorf("cert options: HubAPIURL required")
	}
	if opts.Store == nil {
		return fmt.Errorf("cert options: Store required")
	}
	if opts.WaitForFirstCert <= 0 {
		opts.WaitForFirstCert = 5 * time.Minute
	}
	if opts.WaitForCertInterval <= 0 {
		opts.WaitForCertInterval = 5 * time.Second
	}
	if opts.RequestedCertTTL <= 0 {
		opts.RequestedCertTTL = 365 * 24 * time.Hour
	}
	return nil
}

func loadBootstrapKubeconfig(path string) (*BootstrapClient, error) {
	if path == "" {
		return nil, nil
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("parse bootstrap kubeconfig at %q: %w", path, err)
	}
	return &BootstrapClient{RestConfig: cfg, SourcePath: path}, nil
}

func encodeCert(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func encodeKey(k *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func decodeKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("decode PEM key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func decodeCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("decode PEM cert")
	}
	return x509.ParseCertificate(block.Bytes)
}

// secretStore is the Secret-backed persistence for the issued cert+key.
// Lives in the SPOKE cluster (locally) so pod restarts don't lose the cert.
type secretStore struct {
	client    client.Client
	namespace string
	name      string
}

// ensureNamespace creates the credential namespace if missing. Cheap; only
// runs once at startup.
func (s *secretStore) ensureNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: s.namespace}}
	if err := s.client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %q: %w", s.namespace, err)
	}
	return nil
}

// Load returns the persisted cert+key. ok=false (no error) when the Secret
// is absent — caller treats this as "no cert yet, do bootstrap".
func (s *secretStore) Load(ctx context.Context) (*x509.Certificate, *ecdsa.PrivateKey, bool, error) {
	sec := &corev1.Secret{}
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.name}, sec)
	if apierrors.IsNotFound(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	certPEM := sec.Data["tls.crt"]
	keyPEM := sec.Data["tls.key"]
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, false, nil
	}
	cert, err := decodeCert(certPEM)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode stored cert: %w", err)
	}
	key, err := decodeKey(keyPEM)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode stored key: %w", err)
	}
	return cert, key, true, nil
}

// Save upserts the cert+key Secret. Idempotent.
func (s *secretStore) Save(ctx context.Context, certPEM, keyPEM []byte) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.name,
			Namespace: s.namespace,
			Labels:    map[string]string{"kapro.io/role": "hub-credentials"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
	existing := &corev1.Secret{}
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.name}, existing)
	if apierrors.IsNotFound(err) {
		return s.client.Create(ctx, sec)
	}
	if err != nil {
		return err
	}
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Data = sec.Data
	existing.Type = sec.Type
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range sec.Labels {
		existing.Labels[k] = v
	}
	return s.client.Patch(ctx, existing, patch)
}

func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c-'A'+'a')
		case c == '-' || c == '.':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func int32Ptr(v int32) *int32 { return &v }

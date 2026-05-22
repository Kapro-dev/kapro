package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	crwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	argoactuator "kapro.io/kapro/internal/actuator/argo"
	fluxopactuator "kapro.io/kapro/internal/actuator/fluxoperator"
	pullactuator "kapro.io/kapro/internal/actuator/pull"
	"kapro.io/kapro/internal/hubgateway"
	_ "kapro.io/kapro/internal/metrics" // register custom Prometheus metrics at init
	enginenotifier "kapro.io/kapro/internal/notification/engine"
	pluginadapter "kapro.io/kapro/internal/plugin/adapter"
	kaproSecret "kapro.io/kapro/internal/secret"
	"kapro.io/kapro/internal/version"
	"kapro.io/kapro/internal/webhook"
	kaploadmission "kapro.io/kapro/internal/webhook/admission"
	kaplconversion "kapro.io/kapro/internal/webhook/conversion"
	"kapro.io/kapro/pkg/actuator"
	cm "kapro.io/kapro/pkg/controllermanager"
	"kapro.io/kapro/pkg/gate"
	"kapro.io/kapro/pkg/planner"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha2.AddToScheme(scheme)
}

func defaultLeaderElectionID(shardName string) string {
	const base = "kapro-operator-leader.kapro.io"
	if shardName == "" {
		return base
	}
	sum := sha256.Sum256([]byte(shardName))
	return fmt.Sprintf("kapro-operator-leader-%x.kapro.io", sum[:8])
}

// Options configures the composable Kapro operator server.
type Options struct {
	Config                 *rest.Config
	LeaderElect            bool
	MetricsBindAddress     string
	HealthProbeBindAddress string
	WebhookPort            int
	DevMode                bool
	ZapOptions             zap.Options
}

// OptionsFromEnv returns the same defaults used by the reference binary,
// derived from environment variables only. It does NOT touch the flag
// system or call flag.Parse, so this package can be safely embedded in
// binaries that use Cobra/pflag/etc. Callers that want CLI overrides
// should additionally call (*Options).BindFlags on a FlagSet they own,
// then parse it themselves.
func OptionsFromEnv() Options {
	devMode := os.Getenv("KAPRO_DEV_MODE") == "1"
	return Options{
		LeaderElect:            !devMode,
		MetricsBindAddress:     ":8080",
		HealthProbeBindAddress: ":8081",
		WebhookPort:            9443,
		DevMode:                devMode,
		ZapOptions:             zap.Options{Development: true},
	}
}

// BindFlags registers Kapro server flags on the given FlagSet. It is
// optional: a binary that doesn't want CLI overrides can omit it. The
// FlagSet is supplied by the caller so embedding in cobra/pflag programs
// or test harnesses doesn't require touching flag.CommandLine.
func (o *Options) BindFlags(fs *flag.FlagSet) {
	o.ZapOptions.BindFlags(fs)
	fs.BoolVar(&o.LeaderElect, "leader-elect", o.LeaderElect, "Enable leader election for controller manager.")
	fs.StringVar(&o.MetricsBindAddress, "metrics-bind-address", o.MetricsBindAddress, "The address the metric endpoint binds to.")
	fs.StringVar(&o.HealthProbeBindAddress, "health-probe-bind-address", o.HealthProbeBindAddress, "The address the health probe endpoint binds to.")
	fs.IntVar(&o.WebhookPort, "webhook-port", o.WebhookPort, "The port the admission webhook server binds to.")
}

// Server is the composable promotion engine. Adopters construct one in their
// own main package and register custom gates or actuators before Run.
type Server struct {
	Manager   ctrl.Manager
	Gates     *gate.Registry
	Actuators *actuator.Registry
	Planner   *planner.Framework
}

// Run blocks until ctx is cancelled or the controller-runtime manager exits.
func (s *Server) Run(ctx context.Context) error {
	if s == nil || s.Manager == nil {
		return fmt.Errorf("kapro server is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.Manager.Start(ctx)
}

// Manager-level RBAC requirements (leases, events, secrets, subject-access
// reviews, kapro.io/policies) live as +kubebuilder:rbac markers in
// cmd/operator/main.go so controller-gen picks them up via its standard
// ./cmd/... path. Duplicating them here would be ignored by the
// generator and would drift over time.

func New(opts Options) (*Server, error) {
	if opts.MetricsBindAddress == "" {
		opts.MetricsBindAddress = ":8080"
	}
	if opts.HealthProbeBindAddress == "" {
		opts.HealthProbeBindAddress = ":8081"
	}
	if opts.WebhookPort == 0 {
		opts.WebhookPort = 9443
	}
	if opts.DevMode {
		opts.LeaderElect = false
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts.ZapOptions)))
	log := ctrl.Log.WithName("kapro-operator")
	log.Info("starting kapro-operator", "version", version.Version, "commit", version.Commit, "date", version.Date)

	// KAPRO_CONTROLLERS selects which controllers to run (CCM-style).
	controllersFlag := os.Getenv("KAPRO_CONTROLLERS")
	if controllersFlag == "" {
		controllersFlag = cm.DefaultControllersFlag()
	}
	selected := cm.ParseControllerNames(controllersFlag)
	unknownControllers := cm.UnknownControllerNames(selected)
	if len(unknownControllers) > 0 {
		log.Info("unknown controllers requested; skipping", "controllers", unknownControllers)
	}
	log.Info(
		"controller selection",
		"requested", controllersFlag,
		"selected", cm.SelectedControllerNames(selected),
		"notSelected", cm.DisabledControllerNames(selected),
		"unknown", unknownControllers,
	)

	// POD_NAMESPACE is projected from the downward API in both the Helm chart
	// and the dev kustomize manifest. It drives leader election, notification
	// secret lookup, and the trusted SA identity for admission webhooks.
	podNS := os.Getenv("POD_NAMESPACE")
	if podNS == "" {
		podNS = "kapro-system"
	}
	shardName := os.Getenv("KAPRO_SHARD")
	leaderElectionNS := os.Getenv("KAPRO_LEADER_ELECTION_NAMESPACE")
	if leaderElectionNS == "" {
		leaderElectionNS = podNS
	}
	leaderElectionID := os.Getenv("KAPRO_LEADER_ELECTION_ID")
	if leaderElectionID == "" {
		leaderElectionID = defaultLeaderElectionID(shardName)
	}

	cfg := opts.Config
	if cfg == nil {
		var err error
		cfg, err = ctrl.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("get Kubernetes config: %w", err)
		}
	}

	webhookCertDir := os.Getenv("KAPRO_WEBHOOK_CERT_DIR")
	if opts.DevMode && webhookCertDir == "" {
		dir, err := ensureDevWebhookCerts()
		if err != nil {
			return nil, fmt.Errorf("ensure dev webhook certs: %w", err)
		}
		webhookCertDir = dir
		log.Info("dev mode: auto-generated webhook TLS certs", "dir", webhookCertDir)
	}
	if opts.DevMode {
		log.Info("dev mode: leader election disabled, self-signed webhook certs")
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                opts.LeaderElect,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       leaderElectionNS,
		LeaderElectionReleaseOnCancel: true,
		Metrics: metricsserver.Options{
			BindAddress: opts.MetricsBindAddress,
		},
		HealthProbeBindAddress: opts.HealthProbeBindAddress,
		Controller: ctrlcfg.Controller{
			RecoverPanic:            ptr.To(true),
			MaxConcurrentReconciles: 5,
		},
		WebhookServer: crwebhook.NewServer(crwebhook.Options{
			CertDir: webhookCertDir,
			Port:    opts.WebhookPort,
		}),
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		return nil, fmt.Errorf("start manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to add healthz check")
		return nil, fmt.Errorf("add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to add readyz check")
		return nil, fmt.Errorf("add readyz check: %w", err)
	}

	recorder := mgr.GetEventRecorderFor("kapro-operator") //nolint:staticcheck // migrate to GetEventRecorder when controller-runtime drops this

	// Build actuator registry — resolves per-target actuator at apply time.
	actuatorReg := actuator.NewRegistry()
	foAct := &fluxopactuator.FluxOperatorActuator{Client: mgr.GetClient()}
	if err := actuatorReg.Register("push/flux", foAct); err != nil {
		log.Error(err, "failed to register push/flux actuator")
		return nil, fmt.Errorf("register push/flux actuator: %w", err)
	}
	// Pull-mode delivery records desired versions on Cluster; spoke-side
	// agents own applying those versions to their local backend.
	pullAct := &pullactuator.PullActuator{HubClient: mgr.GetClient()}
	if err := actuatorReg.Register("pull/flux", pullAct); err != nil {
		log.Error(err, "failed to register pull/flux actuator")
		return nil, fmt.Errorf("register pull/flux actuator: %w", err)
	}
	if err := actuatorReg.Register("pull/oci", pullAct); err != nil {
		log.Error(err, "failed to register pull/oci actuator")
		return nil, fmt.Errorf("register pull/oci actuator: %w", err)
	}
	if err := actuatorReg.Register("push/argo", &argoactuator.Actuator{Client: mgr.GetClient()}); err != nil {
		log.Error(err, "failed to register push/argo actuator")
		return nil, fmt.Errorf("register push/argo actuator: %w", err)
	}
	if err := actuatorReg.Register("pull/argo", pullAct); err != nil {
		log.Error(err, "failed to register pull/argo actuator")
		return nil, fmt.Errorf("register pull/argo actuator: %w", err)
	}

	gateRegistry, err := cm.BuildGateRegistry(mgr.GetClient())
	if err != nil {
		log.Error(err, "failed to register built-in gates")
		return nil, fmt.Errorf("register built-in gates: %w", err)
	}
	plannerFramework := planner.NewDefaultFramework()

	ctx := context.Background()
	if pluginadapter.EnabledFromEnv() {
		registered, err := pluginadapter.Registrar{}.RegisterReady(ctx, mgr.GetAPIReader(), actuatorReg, gateRegistry, plannerFramework)
		if err != nil {
			log.Error(err, "failed to register plugin gateway adapters")
			return nil, fmt.Errorf("register plugin gateway adapters: %w", err)
		}
		log.Info("plugin gateway enabled", "registered", registered)
	}

	// Typed Kubernetes clients for verbs not exposed by controller-runtime's
	// generic client: ServiceAccounts/token TokenRequest and CSR UpdateApproval.
	// Used by the Cluster bootstrap controller. Cheap to construct; safe to
	// share across reconcilers.
	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Error(err, "unable to build typed Kubernetes client")
		return nil, fmt.Errorf("build typed Kubernetes client: %w", err)
	}

	cc := cm.ControllerContext{
		Manager:          mgr,
		Recorder:         recorder,
		ActuatorRegistry: actuatorReg,
		GateRegistry:     gateRegistry,
		Notifier: &enginenotifier.Notifier{
			SecretName: "kapro-notifications-secret",
			Namespace:  podNS,
			Client:     mgr.GetClient(),
		},
		Planner:            plannerFramework,
		ApprovalSecret:     loadApprovalSecret(cfg, podNS, log),
		ExternalURL:        os.Getenv("KAPRO_EXTERNAL_URL"),
		HubAPIURL:          os.Getenv("KAPRO_HUB_API_URL"),
		HubCAData:          loadHubCAData(mgr.GetConfig()),
		HeartbeatNamespace: podNS,
		ShardName:          shardName,
		ShardIsDefault:     strings.EqualFold(os.Getenv("KAPRO_SHARD_DEFAULT"), "true"),
		KubeClient:         kubeClient,
		CertClient:         kubeClient.CertificatesV1(),
		PodNamespace:       podNS,
	}

	if cc.ShardName != "" {
		log.Info("controller sharding enabled", "shard", cc.ShardName, "default", cc.ShardIsDefault, "leaderElectionID", leaderElectionID)
	}

	// ApprovalSecret is loaded by ensureApprovalSecret — it either reads the existing
	// K8s Secret, creates one with a random key, or falls back to env var/file mount.
	if len(cc.ApprovalSecret) == 0 {
		log.Error(nil, "approval-secret could not be loaded or generated")
		return nil, fmt.Errorf("approval-secret could not be loaded or generated")
	}

	// Register admission webhooks unless KAPRO_DISABLE_WEBHOOKS=true (used in local dev / kind).
	if os.Getenv("KAPRO_DISABLE_WEBHOOKS") != "true" {
		decoder := admission.NewDecoder(mgr.GetScheme())
		mgr.GetWebhookServer().Register("/convert", kaplconversion.NewIdentityHandler())

		// Build the trusted SA identity from the pod's own namespace + SA name.
		// podNS is defined at the top of main(); POD_SERVICE_ACCOUNT is projected
		// via the downward API in both the Helm chart and dev manifest.
		podSA := os.Getenv("POD_SERVICE_ACCOUNT")
		if podSA == "" {
			podSA = "kapro-operator"
		}
		trustedSA := "system:serviceaccount:" + podNS + ":" + podSA

		mgr.GetWebhookServer().Register(
			"/mutate-kapro-io-v1alpha2-approval",
			&crwebhook.Admission{Handler: kaploadmission.NewApprovalMutator(decoder, trustedSA)},
		)
		mgr.GetWebhookServer().Register(
			"/mutate-kapro-io-v1alpha2-cluster",
			&crwebhook.Admission{Handler: kaploadmission.NewFleetClusterMutator(decoder)},
		)
		// Use APIReader (uncached, direct to apiserver) for the Cluster
		// admission webhook so a cold-start informer cache cannot produce a
		// spurious Backend-not-found rejection. Matches the pattern
		// already used by the plugin gateway registration above.
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-cluster",
			&crwebhook.Admission{Handler: kaploadmission.NewFleetClusterValidator(decoder, mgr.GetAPIReader())},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-promotionrun",
			&crwebhook.Admission{Handler: kaploadmission.NewPromotionRunValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-plan",
			&crwebhook.Admission{Handler: kaploadmission.NewPromotionPlanValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-gateexpression",
			&crwebhook.Admission{Handler: kaploadmission.NewGateExpressionValidator(decoder, mgr.GetAPIReader())},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-approval",
			&crwebhook.Admission{Handler: kaploadmission.NewApprovalValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha2-trigger",
			&crwebhook.Admission{Handler: kaploadmission.NewPromotionTriggerValidator(decoder)},
		)
	}

	for _, name := range cm.KnownControllers() {
		if !selected[name] {
			log.Info("controller disabled", "name", name)
			continue
		}
		initFn := cm.Registry[name]
		enabled, err := initFn(ctx, cc)
		if err != nil {
			log.Error(err, "unable to start controller", "name", name)
			return nil, fmt.Errorf("start controller %s: %w", name, err)
		}
		if !enabled {
			log.Info("controller skipped", "name", name)
			continue
		}
		log.Info("controller started", "name", name)
	}

	if os.Getenv("KAPRO_DISABLE_APPROVAL_SERVER") != "true" {
		// Register mutating HTTP servers as leader-only runnables.
		// controller-runtime calls NeedLeaderElection()=true runnables only on the
		// elected leader — prevents split-brain when running 2+ replicas.
		approvalAddr := os.Getenv("KAPRO_APPROVAL_ADDR")
		if approvalAddr == "" {
			approvalAddr = ":8091"
		}
		decisionAPIEnabled := strings.EqualFold(os.Getenv("KAPRO_ENABLE_DECISION_API"), "true")
		var decisionAuthenticator webhook.DecisionAuthenticator
		var decisionAuthorizer webhook.DecisionAuthorizer
		if decisionAPIEnabled {
			kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
			if err != nil {
				log.Error(err, "unable to create Kubernetes auth client for Decision API")
				return nil, fmt.Errorf("create Kubernetes auth client for Decision API: %w", err)
			}
			decisionAuthenticator = webhook.KubernetesDecisionAuthenticator{Client: kubeClient.AuthenticationV1()}
			decisionAuthorizer = webhook.KubernetesDecisionAuthorizer{Client: kubeClient.AuthorizationV1()}
		}
		approvalHandler := (&webhook.Server{
			Client:                mgr.GetClient(),
			DecisionReader:        mgr.GetAPIReader(),
			TokenSecret:           cc.ApprovalSecret, // reuse already-validated secret
			OperatorNamespace:     podNS,
			DecisionAPIEnabled:    decisionAPIEnabled,
			DecisionAuthenticator: decisionAuthenticator,
			DecisionAuthorizer:    decisionAuthorizer,
		}).Handler()
		if err := mgr.Add(leaderOnlyHTTP(approvalAddr, approvalHandler, 10*time.Second)); err != nil {
			log.Error(err, "unable to add approval server")
			return nil, fmt.Errorf("add approval server: %w", err)
		}
	} else {
		log.Info("approval/decision API server disabled")
	}

	if os.Getenv("KAPRO_DISABLE_HUB_GATEWAY") != "true" {
		gatewayAddr := os.Getenv("KAPRO_HUB_GATEWAY_ADDR")
		if gatewayAddr == "" {
			gatewayAddr = ":8092"
		}
		if err := mgr.Add(leaderOnlyHTTP(gatewayAddr, hubgateway.NewHandler(mgr.GetClient(), cc.ApprovalSecret), 10*time.Second)); err != nil {
			log.Error(err, "unable to add hub gateway")
			return nil, fmt.Errorf("add hub gateway: %w", err)
		}
	} else {
		log.Info("hub gateway disabled")
	}

	return &Server{
		Manager:   mgr,
		Gates:     gateRegistry,
		Actuators: actuatorReg,
		Planner:   plannerFramework,
	}, nil
}

// loadApprovalSecret loads the HMAC approval secret with this priority:
//  1. Mounted file at /etc/kapro/secrets/approval-secret (Helm production path)
//  2. KAPRO_APPROVAL_SECRET env var (local dev)
//  3. K8s Secret "kapro-approval-hmac" — auto-generated if missing, watched for deletion
//
// Uses the cert-manager DynamicAuthority pattern: generate on first run, persist as
// K8s Secret, informer watch recreates if deleted, mutex for concurrent replicas.
func loadApprovalSecret(cfg *rest.Config, namespace string, log logr.Logger) []byte {
	// 1. Try mounted file (production Helm path).
	if data, err := os.ReadFile("/etc/kapro/secrets/approval-secret"); err == nil {
		v := strings.TrimSpace(string(data))
		if v != "" {
			return []byte(v)
		}
	}

	// 2. Try env var (local dev).
	if v := os.Getenv("KAPRO_APPROVAL_SECRET"); v != "" {
		return []byte(v)
	}

	// 3. Bootstrap from K8s Secret with informer watch (cert-manager pattern).
	provider := &kaproSecret.HMACKeyProvider{
		Namespace: namespace,
		Log:       log.WithName("approval-hmac"),
	}
	key, err := provider.Bootstrap(cfg)
	if err != nil {
		log.Error(err, "failed to bootstrap approval HMAC secret")
		return nil
	}
	return key
}

// loadHubCAData returns the PEM-encoded CA certificate for the hub kube-apiserver.
// rest.Config.CAData is inline PEM; if empty, reads from CAFile (kubeconfig path-based config).
func loadHubCAData(cfg *rest.Config) []byte {
	if len(cfg.CAData) > 0 {
		return cfg.CAData
	}
	if cfg.CAFile != "" {
		if data, err := os.ReadFile(cfg.CAFile); err == nil {
			return data
		}
	}
	return nil
}

// The server only starts when this instance holds the leader lock.
func leaderOnlyHTTP(addr string, handler http.Handler, timeout time.Duration) *httpRunnable {
	return &httpRunnable{
		server: &http.Server{
			Addr:         addr,
			Handler:      handler,
			ReadTimeout:  timeout,
			WriteTimeout: timeout,
		},
	}
}

// httpRunnable makes an http.Server into a controller-runtime Runnable.
type httpRunnable struct {
	server *http.Server
}

// NeedLeaderElection returns true so controller-runtime only starts this
// server on the elected leader — not on every standby replica.
func (h *httpRunnable) NeedLeaderElection() bool { return true }

func (h *httpRunnable) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.server.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// mcpRunnable and its methods have been removed — MCP server is not part of MVP.

// ensureDevWebhookCerts generates self-signed TLS certs for local dev mode.
// Returns the directory containing tls.crt and tls.key, or an error that
// the caller must propagate so dev-mode startup fails fast on bad TLS
// material instead of producing an unreachable webhook.
func ensureDevWebhookCerts() (string, error) {
	dir := os.TempDir() + "/kapro-dev-webhook-certs"
	certPath := dir + "/tls.crt"
	keyPath := dir + "/tls.key"

	// Reuse only when BOTH files exist; otherwise regenerate so a partial
	// previous attempt can't poison subsequent runs.
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		return dir, nil
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create dev webhook cert dir: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), crypto_rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate dev webhook key: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kapro-dev-webhook"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(crypto_rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", fmt.Errorf("create dev webhook certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal dev webhook private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return "", fmt.Errorf("write dev webhook cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", fmt.Errorf("write dev webhook key: %w", err)
	}
	return dir, nil
}

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crypto_rand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
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

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	argoactuator "kapro.io/kapro/internal/actuator/argo"
	fluxopactuator "kapro.io/kapro/internal/actuator/fluxoperator"
	spokeactuator "kapro.io/kapro/internal/actuator/spoke"
	"kapro.io/kapro/internal/hubgateway"
	_ "kapro.io/kapro/internal/metrics" // register custom Prometheus metrics at init
	enginenotifier "kapro.io/kapro/internal/notification/engine"
	pluginadapter "kapro.io/kapro/internal/plugin/adapter"
	kaproSecret "kapro.io/kapro/internal/secret"
	"kapro.io/kapro/internal/version"
	"kapro.io/kapro/internal/webhook"
	kaploadmission "kapro.io/kapro/internal/webhook/admission"
	"kapro.io/kapro/pkg/actuator"
	cm "kapro.io/kapro/pkg/controllermanager"
	"kapro.io/kapro/pkg/planner"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

// Manager-level RBAC requirements not tied to a specific controller.
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=kapro.io,resources=agentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=agentpolicies/status,verbs=get;update;patch

func main() {
	devMode := os.Getenv("KAPRO_DEV_MODE") == "1"
	leaderElect := !devMode
	metricsAddr := ":8080"
	healthProbeAddr := ":8081"
	webhookPort := 9443

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.BoolVar(&leaderElect, "leader-elect", leaderElect, "Enable leader election for controller manager.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", metricsAddr, "The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", healthProbeAddr, "The address the health probe endpoint binds to.")
	flag.IntVar(&webhookPort, "webhook-port", webhookPort, "The port the admission webhook server binds to.")
	flag.Parse()
	if devMode {
		leaderElect = false
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("kapro-operator")
	log.Info("starting kapro-operator", "version", version.Version, "commit", version.Commit, "date", version.Date)

	// KAPRO_CONTROLLERS selects which controllers to run (CCM-style).
	controllersFlag := os.Getenv("KAPRO_CONTROLLERS")
	if controllersFlag == "" {
		controllersFlag = "*"
	}
	selected := cm.ParseControllerNames(controllersFlag)
	log.Info("controller selection", "controllers", controllersFlag)

	// POD_NAMESPACE is projected from the downward API in both the Helm chart
	// and the dev kustomize manifest. It drives leader election, notification
	// secret lookup, and the trusted SA identity for admission webhooks.
	podNS := os.Getenv("POD_NAMESPACE")
	if podNS == "" {
		podNS = "kapro-system"
	}
	leaderElectionNS := os.Getenv("KAPRO_LEADER_ELECTION_NAMESPACE")
	if leaderElectionNS == "" {
		leaderElectionNS = podNS
	}

	cfg := ctrl.GetConfigOrDie()

	webhookCertDir := os.Getenv("KAPRO_WEBHOOK_CERT_DIR")
	if devMode && webhookCertDir == "" {
		webhookCertDir = ensureDevWebhookCerts()
		log.Info("dev mode: auto-generated webhook TLS certs", "dir", webhookCertDir)
	}
	if devMode {
		log.Info("dev mode: leader election disabled, self-signed webhook certs")
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                leaderElect,
		LeaderElectionID:              "kapro-operator-leader.kapro.io",
		LeaderElectionNamespace:       leaderElectionNS,
		LeaderElectionReleaseOnCancel: true,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: healthProbeAddr,
		Controller: ctrlcfg.Controller{
			RecoverPanic:            ptr.To(true),
			MaxConcurrentReconciles: 5,
		},
		WebhookServer: crwebhook.NewServer(crwebhook.Options{
			CertDir: webhookCertDir,
			Port:    webhookPort,
		}),
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("kapro-operator") //nolint:staticcheck // migrate to GetEventRecorder when controller-runtime drops this

	// Build actuator registry — resolves per-target actuator at apply time.
	actuatorReg := actuator.NewRegistry()
	foAct := &fluxopactuator.FluxOperatorActuator{Client: mgr.GetClient()}
	if err := actuatorReg.Register("push/flux", foAct); err != nil {
		log.Error(err, "failed to register push/flux actuator")
		os.Exit(1)
	}
	// Pull-mode delivery records desired versions on MemberCluster; spoke-side
	// agents own applying those versions to their local backend.
	spokeAct := &spokeactuator.DesiredStateActuator{HubClient: mgr.GetClient()}
	if err := actuatorReg.Register("pull/flux", spokeAct); err != nil {
		log.Error(err, "failed to register pull/flux actuator")
		os.Exit(1)
	}
	if err := actuatorReg.Register("push/argo", &argoactuator.Actuator{Client: mgr.GetClient()}); err != nil {
		log.Error(err, "failed to register push/argo actuator")
		os.Exit(1)
	}
	if err := actuatorReg.Register("pull/argo", spokeAct); err != nil {
		log.Error(err, "failed to register pull/argo actuator")
		os.Exit(1)
	}

	gateRegistry, err := cm.BuildGateRegistry(mgr.GetClient())
	if err != nil {
		log.Error(err, "failed to register built-in gates")
		os.Exit(1)
	}
	plannerFramework := planner.NewDefaultFramework()

	ctx := context.Background()
	if pluginadapter.EnabledFromEnv() {
		registered, err := pluginadapter.Registrar{}.RegisterReady(ctx, mgr.GetAPIReader(), actuatorReg, gateRegistry, plannerFramework)
		if err != nil {
			log.Error(err, "failed to register plugin gateway adapters")
			os.Exit(1)
		}
		log.Info("plugin gateway enabled", "registered", registered)
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
		ShardName:          os.Getenv("KAPRO_SHARD"),
	}

	if cc.ShardName != "" {
		log.Info("controller sharding enabled", "shard", cc.ShardName)
	}

	// ApprovalSecret is loaded by ensureApprovalSecret — it either reads the existing
	// K8s Secret, creates one with a random key, or falls back to env var/file mount.
	if len(cc.ApprovalSecret) == 0 {
		log.Error(nil, "approval-secret could not be loaded or generated")
		os.Exit(1)
	}

	// Register admission webhooks unless KAPRO_DISABLE_WEBHOOKS=true (used in local dev / kind).
	if os.Getenv("KAPRO_DISABLE_WEBHOOKS") != "true" {
		decoder := admission.NewDecoder(mgr.GetScheme())

		// Build the trusted SA identity from the pod's own namespace + SA name.
		// podNS is defined at the top of main(); POD_SERVICE_ACCOUNT is projected
		// via the downward API in both the Helm chart and dev manifest.
		podSA := os.Getenv("POD_SERVICE_ACCOUNT")
		if podSA == "" {
			podSA = "kapro-operator"
		}
		trustedSA := "system:serviceaccount:" + podNS + ":" + podSA

		mgr.GetWebhookServer().Register(
			"/mutate-kapro-io-v1alpha1-approval",
			&crwebhook.Admission{Handler: kaploadmission.NewApprovalMutator(decoder, trustedSA)},
		)
		mgr.GetWebhookServer().Register(
			"/mutate-kapro-io-v1alpha1-membercluster",
			&crwebhook.Admission{Handler: kaploadmission.NewMemberClusterMutator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha1-membercluster",
			&crwebhook.Admission{Handler: kaploadmission.NewMemberClusterValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha1-release",
			&crwebhook.Admission{Handler: kaploadmission.NewReleaseValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha1-pipeline",
			&crwebhook.Admission{Handler: kaploadmission.NewPipelineValidator(decoder)},
		)
		mgr.GetWebhookServer().Register(
			"/validate-kapro-io-v1alpha1-approval",
			&crwebhook.Admission{Handler: kaploadmission.NewApprovalValidator(decoder)},
		)
	}

	for name, initFn := range cm.Registry {
		if !selected[name] {
			log.Info("controller disabled", "name", name)
			continue
		}
		enabled, err := initFn(ctx, cc)
		if err != nil {
			log.Error(err, "unable to start controller", "name", name)
			os.Exit(1)
		}
		if enabled {
			log.Info("controller started", "name", name)
		}
	}

	if os.Getenv("KAPRO_DISABLE_APPROVAL_SERVER") != "true" {
		// Register mutating HTTP servers as leader-only runnables.
		// controller-runtime calls NeedLeaderElection()=true runnables only on the
		// elected leader — prevents split-brain when running 2+ replicas.
		approvalAddr := os.Getenv("KAPRO_APPROVAL_ADDR")
		if approvalAddr == "" {
			approvalAddr = ":8091"
		}
		approvalHandler := (&webhook.Server{
			Client:            mgr.GetClient(),
			TokenSecret:       cc.ApprovalSecret, // reuse already-validated secret
			OperatorNamespace: podNS,
		}).Handler()
		if err := mgr.Add(leaderOnlyHTTP(approvalAddr, approvalHandler, 10*time.Second)); err != nil {
			log.Error(err, "unable to add approval server")
			os.Exit(1)
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
			os.Exit(1)
		}
	} else {
		log.Info("hub gateway disabled")
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
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
// Returns the directory containing tls.crt and tls.key.
func ensureDevWebhookCerts() string {
	dir := os.TempDir() + "/kapro-dev-webhook-certs"
	certPath := dir + "/tls.crt"
	keyPath := dir + "/tls.key"

	// Reuse if already exists.
	if _, err := os.Stat(certPath); err == nil {
		return dir
	}

	_ = os.MkdirAll(dir, 0700)

	// Generate self-signed cert using crypto/x509.
	import_key, _ := ecdsa.GenerateKey(elliptic.P256(), crypto_rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kapro-dev-webhook"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	certDER, _ := x509.CreateCertificate(crypto_rand.Reader, template, template, &import_key.PublicKey, import_key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(import_key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	_ = os.WriteFile(certPath, certPEM, 0600)
	_ = os.WriteFile(keyPath, keyPEM, 0600)
	return dir
}

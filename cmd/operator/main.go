package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	kserveactuator "kapro.io/kapro/internal/actuator/kserve"
	internalgate "kapro.io/kapro/internal/gate"
	gitopshealth "kapro.io/kapro/internal/health/gitops"
	_ "kapro.io/kapro/internal/metrics" // register custom Prometheus metrics at init
	"kapro.io/kapro/internal/mcp"
	enginenotifier "kapro.io/kapro/internal/notification/engine"
	orasoci "kapro.io/kapro/internal/oci/oras"
	"kapro.io/kapro/internal/registration"
	cosignverifier "kapro.io/kapro/internal/verification/cosign"
	kaploadmission "kapro.io/kapro/internal/webhook/admission"
	"kapro.io/kapro/internal/webhook"
	"kapro.io/kapro/pkg/actuator"
	cm "kapro.io/kapro/pkg/controllermanager"
	crwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("kapro-operator")

	// KAPRO_CONTROLLERS selects which controllers to run (CCM-style).
	controllersFlag := os.Getenv("KAPRO_CONTROLLERS")
	if controllersFlag == "" {
		controllersFlag = "*"
	}
	selected := cm.ParseControllerNames(controllersFlag)
	log.Info("controller selection", "controllers", controllersFlag)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                true,
		LeaderElectionID:              "kapro-operator-leader.kapro.io",
		LeaderElectionNamespace:       "kapro-system",
		LeaderElectionReleaseOnCancel: true,
		Metrics: metricsserver.Options{
			BindAddress: ":8080", // scraped by Prometheus; expose on /metrics
		},
		HealthProbeBindAddress: ":8081",
		// Recover from reconciler panics instead of crashing the whole manager.
		Controller: ctrlcfg.Controller{
			RecoverPanic:            ptr.To(true),
			MaxConcurrentReconciles: 5,
		},
		WebhookServer: crwebhook.NewServer(crwebhook.Options{
			// TLS certs loaded from the kapro-webhook-tls Secret via cert-manager or manual mount.
			CertDir: os.Getenv("KAPRO_WEBHOOK_CERT_DIR"),
			Port:    9443,
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

	recorder := mgr.GetEventRecorderFor("kapro-operator")

	gates := cm.BuildFullGateSet()
	gates.Approval = &internalgate.ApprovalGate{Client: mgr.GetClient()}
	gates.Verification = &internalgate.VerificationGate{
		Verifier:  &cosignverifier.Verifier{},
		KeyReader: &internalgate.ClientSecretKeyReader{Client: mgr.GetClient()},
	}

	// Build actuator registry — resolves per-Environment actuator at apply time.
	actuatorReg := actuator.NewRegistry()
	if err := actuatorReg.Register("flux", &fluxactuator.FluxActuator{Client: mgr.GetClient()}); err != nil {
		log.Error(err, "failed to register flux actuator")
		os.Exit(1)
	}
	if err := actuatorReg.Register("kserve", &kserveactuator.Actuator{}); err != nil {
		log.Error(err, "failed to register kserve actuator")
		os.Exit(1)
	}

	cc := cm.ControllerContext{
		Manager:         mgr,
		Recorder:        recorder,
		ActuatorRegistry: actuatorReg,
		Gates:           gates,
		HealthAssessor:  &gitopshealth.Assessor{Client: mgr.GetClient()},
		Notifier: &enginenotifier.Notifier{
			SecretName: "kapro-notifications-secret",
			Namespace:  "kapro-system",
			Client:     mgr.GetClient(),
		},
		OCIService:     &orasoci.Service{},
		ApprovalSecret: []byte(os.Getenv("KAPRO_APPROVAL_SECRET")),
		ExternalURL:    os.Getenv("KAPRO_EXTERNAL_URL"),
	}

	// Register mutating admission webhook: Approval.spec.approvedBy ← real k8s username.
	// Admission webhooks run on ALL replicas (not leader-only) — kube-apiserver
	// load-balances across them. They only mutate the object being admitted, not cluster state.
	decoder := admission.NewDecoder(mgr.GetScheme())
	mgr.GetWebhookServer().Register(
		"/mutate-kapro-io-v1alpha1-approval",
		&crwebhook.Admission{Handler: kaploadmission.NewApprovalMutator(decoder)},
	)

	// Register validating admission webhooks for core CRDs.
	mgr.GetWebhookServer().Register(
		"/validate-kapro-io-v1alpha1-environment",
		&crwebhook.Admission{Handler: kaploadmission.NewEnvironmentValidator(decoder)},
	)
	mgr.GetWebhookServer().Register(
		"/validate-kapro-io-v1alpha1-release",
		&crwebhook.Admission{Handler: kaploadmission.NewReleaseValidator(decoder)},
	)
	mgr.GetWebhookServer().Register(
		"/validate-kapro-io-v1alpha1-pipeline",
		&crwebhook.Admission{Handler: kaploadmission.NewPipelineValidator(decoder)},
	)

	ctx := context.Background()

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

	// Register mutating HTTP servers as leader-only runnables.
	// controller-runtime calls NeedLeaderElection()=true runnables only on the
	// elected leader — prevents split-brain when running 2+ replicas.

	regServer := &registration.Server{Client: mgr.GetClient()}
	if err := mgr.Add(leaderOnlyHTTP(":9090", regServer, 10*time.Second)); err != nil {
		log.Error(err, "unable to add registration server")
		os.Exit(1)
	}

	mcpAddr := os.Getenv("KAPRO_MCP_ADDR")
	if mcpAddr == "" {
		mcpAddr = ":8090"
	}
	mcpServer := mcp.New(mgr.GetClient())
	if err := mgr.Add(&mcpRunnable{server: mcpServer, addr: mcpAddr}); err != nil {
		log.Error(err, "unable to add MCP server")
		os.Exit(1)
	}

	approvalAddr := os.Getenv("KAPRO_APPROVAL_ADDR")
	if approvalAddr == "" {
		approvalAddr = ":8091"
	}
	approvalHandler := (&webhook.Server{
		Client:      mgr.GetClient(),
		TokenSecret: []byte(os.Getenv("KAPRO_APPROVAL_SECRET")),
	}).Handler()
	if err := mgr.Add(leaderOnlyHTTP(approvalAddr, approvalHandler, 10*time.Second)); err != nil {
		log.Error(err, "unable to add approval server")
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// leaderOnlyHTTP wraps an http.Handler as a leader-election-gated manager.Runnable.
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

// mcpRunnable wraps the MCP server as a leader-election-gated Runnable.
type mcpRunnable struct {
	server *mcp.Server
	addr   string
}

func (m *mcpRunnable) NeedLeaderElection() bool { return true }

func (m *mcpRunnable) Start(ctx context.Context) error {
	return m.server.Start(ctx, m.addr)
}

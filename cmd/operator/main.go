package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	fluxactuator "kapro.io/kapro/internal/actuator/flux"
	_ "kapro.io/kapro/internal/metrics" // register custom Prometheus metrics at init
	enginenotifier "kapro.io/kapro/internal/notification/engine"
	"kapro.io/kapro/internal/version"
	"kapro.io/kapro/internal/webhook"
	kaploadmission "kapro.io/kapro/internal/webhook/admission"
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

// Manager-level RBAC requirements not tied to a specific controller.
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func main() {
	opts := zap.Options{Development: true}
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

	cfg := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		LeaderElection:                true,
		LeaderElectionID:              "kapro-operator-leader.kapro.io",
		LeaderElectionNamespace:       podNS,
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

	// Build actuator registry — resolves per-target actuator at apply time.
	actuatorReg := actuator.NewRegistry()
	fluxAct := &fluxactuator.FluxActuator{Client: mgr.GetClient()}

	// Actuator-specific preflight: each backend checks its own prerequisites.
	// This replaces the old global requireFlux() — when ArgoCD is added, only
	// its actuator checks for ArgoCD CRDs, not the entire operator.
	if err := fluxAct.Preflight(cfg); err != nil {
		log.Error(err, "Flux actuator preflight failed — install Flux or remove the Flux actuator")
		os.Exit(1)
	}

	if err := actuatorReg.Register("flux", fluxAct); err != nil {
		log.Error(err, "failed to register flux actuator")
		os.Exit(1)
	}

	gateRegistry, err := cm.BuildGateRegistry(mgr.GetClient())
	if err != nil {
		log.Error(err, "failed to register built-in gates")
		os.Exit(1)
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
		ApprovalSecret: loadSecret("approval-secret"),
		ExternalURL:    os.Getenv("KAPRO_EXTERNAL_URL"),
		HubAPIURL:      os.Getenv("KAPRO_HUB_API_URL"),
		HubCAData:      loadHubCAData(mgr.GetConfig()),
	}

	// Fail hard if the HMAC secret is missing — a zero-length secret means any token
	// passes verification, which is a critical security hole.
	if len(cc.ApprovalSecret) == 0 {
		log.Error(nil, "approval-secret is empty; set the approval-secret Kubernetes Secret or KAPRO_APPROVAL_SECRET env var")
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

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// loadSecret reads a secret value from a mounted file (preferred) or falls back to an env var.
// The mounted-file path is /etc/kapro/secrets/<name> — injected by Helm via secretKeyRef volume.
// This is the production path; the env var fallback is for local dev only.
func loadSecret(name string) []byte {
	path := "/etc/kapro/secrets/" + name
	if data, err := os.ReadFile(path); err == nil {
		return []byte(strings.TrimSpace(string(data)))
	}
	return []byte(os.Getenv(strings.ToUpper(strings.ReplaceAll("KAPRO_"+name, "-", "_"))))
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
